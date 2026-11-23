package room

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/ciranda-dev/kolo-pin"
)

// waitBodies waits until the recorder of a room shows total distinct messages, polling slowly: it copies them.
func waitBodies(t testing.TB, rec *recorder, total int, d time.Duration, what string) map[string]struct{} {
	t.Helper()
	deadline := time.Now().Add(d)
	for {
		set := rec.bodySet()
		if len(set) >= total {
			return set
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out after %v waiting for %s: %d of %d messages", d, what, len(set), total)
		}
		time.Sleep(250 * time.Millisecond)
	}
}

// scaleCatchUp runs a room of n devices with two admins and two pins on 127.0.0.1 over real connections. Three members
// are away while the room moves through three epochs and sends total messages, an admin is killed in the middle of one
// of them and started again, and then each member that was away returns, once with the admins present, once with every
// other member away (from the pins only), and once with the pins down (from members and admins).
func scaleCatchUp(t *testing.T, n, total int) {
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Minute)
	defer cancel()
	ps := []*pinServer{newPin(t), newPin(t)}
	peers := []pin.Peer{ps[0].peer, ps[1].peer}
	lim := Limits{Rate: 1000, Burst: 1000, PinGap: 500 * time.Millisecond, PinWait: 2 * time.Second, RepublishAfter: 15 * time.Second, CatchUpWait: time.Minute}
	const nAway, nLate = 3, 3
	server := newServer(t)
	first := newTNode(t, tnodeOpts{server: server, limits: lim, pins: peers})
	r, err := first.n.Create(ctx, "scale", Policy{})
	if err != nil {
		t.Fatal(err)
	}
	away1, away2, away3 := 10, 11, 12
	gr := &group{t: t, server: server, nodes: []*tnode{first}, rooms: []*Room{r}, lim: lim, pins: peers, light: true,
		keep: map[int]bool{0: true, 1: true, away1: true, away2: true, away3: true}, away: map[int]bool{}}
	gr.recs = []*recorder{recordWith(r, false)}
	var rest, late []*tnode
	for range n - 1 - nLate {
		rest = append(rest, gr.newNode())
	}
	for range nLate {
		late = append(late, gr.newNode())
	}
	start := time.Now()
	gr.joinAll(ctx, rest, 8)
	// The recorders of the members that keep everything are full; the others count.
	for _, i := range []int{1, away1, away2, away3} {
		gr.recs[i] = recordWith(gr.rooms[i], false)
	}
	t.Logf("%d devices joined in %v", len(gr.nodes), time.Since(start).Round(time.Second))
	gr.waitMesh(2)
	if err := gr.rooms[0].Promote(ctx, gr.nodes[1].key(), Admin); err != nil {
		t.Fatal(err)
	}
	gr.waitEpoch(gr.rooms[0].Status().Epoch)
	waitFor(t, time.Minute, "the second admin", func() bool { return gr.rooms[1].isAdmin() })
	for _, i := range []int{away1, away2, away3} {
		gr.stop(i)
	}
	firstEpoch := gr.rooms[0].Status().Epoch

	senders := []int{2, 3, 4, 5, 6, 7, 8, 9}
	sent := 0
	send := func(count int) {
		var wg sync.WaitGroup
		base := sent
		for s, from := range senders {
			wg.Go(func() {
				for k := s; k < count; k += len(senders) {
					if _, err := gr.rooms[from].Send(ctx, []byte(fmt.Sprintf("m%05d", base+k)), MessageID{}); err != nil {
						t.Error(err)
						return
					}
					time.Sleep(40 * time.Millisecond)
				}
			})
		}
		wg.Wait()
		sent += count
	}
	quarter := total / 4
	start = time.Now()
	send(quarter)
	gr.join(ctx, late[0])
	send(quarter)
	gr.join(ctx, late[1])
	send(quarter / 2)
	gr.stop(1) // an admin is killed in the middle of an epoch
	send(quarter / 2)
	gr.restart(1)
	waitFor(t, time.Minute, "the admin to run again", func() bool { return gr.rooms[1].isAdmin() })
	gr.join(ctx, late[2])
	send(total - sent)
	t.Logf("%d messages sent through 3 epochs in %v", sent, time.Since(start).Round(time.Second))
	finalEpoch := gr.rooms[0].Status().Epoch
	if finalEpoch != firstEpoch+3 {
		t.Fatalf("epoch %d, want %d", finalEpoch, firstEpoch+3)
	}
	waitFor(t, 10*time.Minute, "every message to be stored", func() bool {
		for _, s := range senders {
			if gr.rooms[s].Status().Receipts != 0 {
				return false
			}
		}
		return len(storedIDs(gr.rooms[0], gr.rooms[1])) == total
	})
	t.Logf("every message is in a segment of an admin")

	check := func(i int, what string) {
		t.Helper()
		start := time.Now()
		waitFor(t, 10*time.Minute, what+": the epoch", func() bool { return gr.rooms[i].Status().Epoch == finalEpoch })
		set := waitBodies(t, gr.recs[i], total, 15*time.Minute, what)
		for k := range total {
			if _, ok := set[fmt.Sprintf("m%05d", k)]; !ok {
				t.Fatalf("%s: message %d is missing", what, k)
			}
		}
		if errs := catchUpErrors(gr.recs[i]); len(errs) > 0 {
			t.Fatalf("%s: %d reports, the first: %v", what, len(errs), errs[0].Err)
		}
		a0, ai := authenticatorOf(gr.rooms[0]), authenticatorOf(gr.rooms[i])
		if gr.rooms[0].removed.Load() == false && string(a0) != string(ai) && !gr.away[0] {
			t.Fatalf("%s: another epoch authenticator", what)
		}
		t.Logf("%s: caught up on 3 epochs and %d messages in %v", what, total, time.Since(start).Round(time.Millisecond))
	}

	// 1. With the admins present.
	gr.restart(away1)
	check(away1, "from the admins")

	// 2. With every other member away: the pins only.
	waitFor(t, 5*time.Minute, "the admins to push everything", func() bool {
		return !gr.rooms[0].dirtyAny() && !gr.rooms[1].dirtyAny()
	})
	for _, i := range []int{0, 1} {
		for _, l := range gr.rooms[i].snapshot() {
			if l.own {
				pinsHold(t, peers, gr.nodes[i].dev, l.id, l.log.Len())
			}
		}
	}
	for i := range gr.nodes {
		if i != away2 && !gr.away[i] {
			gr.stop(i)
		}
	}
	gr.restart(away2)
	check(away2, "from the pins only")

	// 3. With the pins down: from the members and admins that hold the logs.
	ps[0].down()
	ps[1].down()
	gr.stop(away2)
	for _, i := range []int{0, 1, away1} {
		gr.restart(i)
	}
	gr.waitPeers(1)
	gr.restart(away3)
	check(away3, "with the pins down")
}

func TestCatchUpWithSixteenNodes(t *testing.T) {
	if testing.Short() {
		t.Skip("16 devices")
	}
	scaleCatchUp(t, 16, 800)
}

// TestCatchUpWithSixtyFourNodes is the acceptance run of the segments: 64 devices, 2 admins, 2 pins, 10,000 messages.
func TestCatchUpWithSixtyFourNodes(t *testing.T) {
	if testing.Short() {
		t.Skip("64 devices")
	}
	scaleCatchUp(t, 64, 10000)
}
