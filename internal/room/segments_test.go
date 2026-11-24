package room

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/ciranda-dev/kolo-pin"
)

// segLen is the number of entries in the segment the admin of r writes now.
func segLen(r *Room) uint64 {
	r.segMu.Lock()
	defer r.segMu.Unlock()
	if r.seg == nil {
		return 0
	}
	return r.seg.log.Len()
}

func hasStored(rec *recorder, id MessageID) bool {
	for _, ev := range rec.events() {
		if ev.Kind == Stored && ev.ID == id && ev.Err == nil {
			return true
		}
	}
	return false
}

func TestAdminStoresEveryMessageInASegmentAndMembersHoldIt(t *testing.T) {
	gr := newGroup(t, 3, Limits{})
	gr.waitMesh(2)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	var ids []MessageID
	for i := range 20 {
		id, err := gr.rooms[1+i%2].Send(ctx, []byte(fmt.Sprintf("m%d", i)), MessageID{})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	waitFor(t, 30*time.Second, "the admin's segment", func() bool { return segLen(gr.rooms[0]) == 20 })
	for _, id := range ids[:2] {
		waitFor(t, 30*time.Second, "a stored receipt", func() bool { return hasStored(gr.recs[1], id) || hasStored(gr.recs[2], id) })
	}
	// A member follows the segment its admin opened and holds its 20 entries.
	waitFor(t, 30*time.Second, "a member's replica of the segment", func() bool {
		for _, l := range gr.rooms[2].snapshot() {
			if !l.ctl && l.view.Len() == 20 {
				return true
			}
		}
		return false
	})
	if got := gr.rooms[1].Status().Receipts; got != 0 {
		waitFor(t, 30*time.Second, "no receipts outstanding", func() bool { return gr.rooms[1].Status().Receipts == 0 })
	}
}

// A member that is away while the room moves on three epochs reads the Commits and the messages it missed from the
// admin's logs, and none is lost.
func TestMemberAwayCatchesUpFromTheAdmin(t *testing.T) {
	gr := newGroup(t, 3, Limits{RepublishAfter: time.Second})
	gr.waitMesh(2)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	before := gr.rooms[2].Status().Epoch
	gr.stop(2)
	sent := map[string]bool{}
	send := func(from int, n int, tag string) {
		for i := range n {
			body := fmt.Sprintf("%s-%d", tag, i)
			if _, err := gr.rooms[from].Send(ctx, []byte(body), MessageID{}); err != nil {
				t.Fatal(err)
			}
			sent[body] = true
		}
	}
	send(1, 10, "e0")
	for e := 1; e <= 3; e++ {
		gr.join(ctx, gr.newNode())
		send(e%2, 10, fmt.Sprintf("e%d", e))
	}
	want := gr.rooms[0].Status().Epoch
	if want != before+3 {
		t.Fatalf("epoch %d, want %d", want, before+3)
	}
	r := gr.restart(2)
	rec := gr.recs[2]
	defer func() {
		if t.Failed() {
			dump(t, r, rec)
			dump(t, gr.rooms[0], gr.recs[0])
			dump(t, gr.rooms[1], gr.recs[1])
			t.Logf("receipts at 1: %d", gr.rooms[1].Status().Receipts)
		}
	}()
	waitFor(t, 60*time.Second, "the away member at the last epoch", func() bool { return r.Status().Epoch == want })
	ok := func() bool {
		for b := range sent {
			if !rec.has(b) {
				return false
			}
		}
		return true
	}
	waitFor(t, 20*time.Second, "every message", func() bool {
		if ok() {
			return true
		}
		return false
	})

	for _, ev := range rec.events() {
		if ev.Err != nil {
			t.Fatalf("an event with an error: %+v", ev)
		}
	}
	a0, a2 := authenticatorOf(gr.rooms[0]), authenticatorOf(r)
	if string(a0) != string(a2) {
		t.Fatal("another epoch authenticator")
	}
}

func dump(t testing.TB, r *Room, rec *recorder) {
	for _, l := range r.snapshot() {
		r.logMu.Lock()
		t.Logf("log owner %x ctl=%v own=%v epoch=%d len=%d cursor=%d closed=%v size=%d keyed=%d applied=%d", l.owner[:3], l.ctl, l.own, l.epoch, l.view.Len(), l.cursor, l.closed, l.size, l.keyed, l.applied)
		r.logMu.Unlock()
	}
	for _, ev := range rec.events() {
		if ev.Err != nil {
			t.Logf("event %v: %v", ev.Kind, ev.Err)
		}
	}
	t.Logf("epoch %d, %d messages", r.Status().Epoch, len(rec.messages()))
}

// pinned returns a group of three whose admin pushes to two pins, the member 2 away for three epochs and what was
// sent while it was away.
func pinnedGroup(t *testing.T, lim Limits) (gr *group, sent map[string]bool, ps []*pinServer) {
	ps = []*pinServer{newPin(t), newPin(t)}
	peers := []pin.Peer{ps[0].peer, ps[1].peer}
	for _, d := range []struct {
		v   *time.Duration
		def time.Duration
	}{{&lim.PinGap, 50 * time.Millisecond}, {&lim.PinWait, 300 * time.Millisecond}, {&lim.RepublishAfter, time.Second}} {
		if *d.v == 0 {
			*d.v = d.def
		}
	}
	gr = newGroupPins(t, 3, lim, peers, nil)
	gr.waitMesh(2)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	gr.stop(2)
	sent = map[string]bool{}
	for e := range 4 {
		if e > 0 {
			gr.join(ctx, gr.newNode())
		}
		for i := range 10 {
			body := fmt.Sprintf("e%d-%d", e, i)
			if _, err := gr.rooms[0].Send(ctx, []byte(body), MessageID{}); err != nil {
				t.Fatal(err)
			}
			sent[body] = true
		}
	}
	return gr, sent, ps
}

func TestMemberCatchesUpFromThePinsWhenEveryoneElseIsAway(t *testing.T) {
	gr, sent, ps := pinnedGroup(t, Limits{})
	admin := gr.rooms[0]
	want := admin.Status().Epoch
	waitFor(t, 30*time.Second, "the admin to have pushed its logs", func() bool {
		return admin.Status().PinError == nil && !admin.dirtyAny()
	})
	for _, l := range admin.snapshot() {
		pinsHold(t, []pin.Peer{ps[0].peer, ps[1].peer}, gr.nodes[0].dev, l.id, l.log.Len())
	}
	for i := range gr.nodes {
		if i != 2 {
			gr.stop(i)
		}
	}
	r := gr.restart(2)
	rec := gr.recs[2]
	defer func() {
		if t.Failed() {
			dump(t, r, rec)
			r.pinMu.Lock()
			t.Logf("pin conns %d since %v", len(r.pinConns), r.pinSince)
			r.pinMu.Unlock()
			t.Logf("admins online %d need %v", r.adminsOnline(), r.catchingUp())
		}
	}()
	waitFor(t, 20*time.Second, "the epochs from the pins", func() bool { return r.Status().Epoch == want })
	waitFor(t, 60*time.Second, "every message from the pins", func() bool {
		for b := range sent {
			if !rec.has(b) {
				return false
			}
		}
		return true
	})
}

func TestPromoteMakesASecondAdminThatWritesSegments(t *testing.T) {
	gr := newGroup(t, 3, Limits{RepublishAfter: time.Second})
	gr.waitMesh(2)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if err := gr.rooms[0].Promote(ctx, gr.nodes[1].key(), Admin); err != nil {
		for i := range gr.rooms {
			dump(t, gr.rooms[i], gr.recs[i])
		}
		t.Fatal(err)
	}
	waitFor(t, 30*time.Second, "every member at the new epoch", func() bool { return gr.rooms[2].Status().Epoch == gr.rooms[0].Status().Epoch })
	waitFor(t, 30*time.Second, "the second admin to run", func() bool { return gr.rooms[1].isAdmin() && gr.rooms[1].Status().Epoch == gr.rooms[0].Status().Epoch })
	for i, r := range gr.rooms {
		var admins int
		for _, m := range r.Members() {
			if m.Role == Admin || m.Role == Settler {
				admins++
			}
		}
		if admins != 2 {
			t.Fatalf("member %d sees %d admins", i, admins)
		}
	}
	for i := range 10 {
		if _, err := gr.rooms[2].Send(ctx, []byte(fmt.Sprintf("after-%d", i)), MessageID{}); err != nil {
			t.Fatal(err)
		}
	}
	waitFor(t, 30*time.Second, "both admins to store the messages", func() bool { return segLen(gr.rooms[0]) >= 10 && segLen(gr.rooms[1]) >= 10 })
	// The second admin commits too: its Add settles and the first admin applies it.
	nd := gr.newNode()
	kp, err := nd.n.KeyPackage()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := gr.rooms[1].Add(ctx, kp); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 30*time.Second, "the first admin at the new epoch", func() bool { return gr.rooms[0].Status().Epoch == gr.rooms[1].Status().Epoch })
}

// Every device stopped and started again reopens its room, at the same epoch with the same epoch authenticator, goes on
// reading and writing, and an admin goes on with the segment it was writing.
func TestRestartOfEveryNodeReopensEveryRoom(t *testing.T) {
	gr := newGroup(t, 4, Limits{RepublishAfter: time.Second})
	gr.waitMesh(2)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	for i := range 6 {
		if _, err := gr.rooms[1+i%3].Send(ctx, []byte(fmt.Sprintf("before-%d", i)), MessageID{}); err != nil {
			t.Fatal(err)
		}
	}
	waitFor(t, 30*time.Second, "the admin's segment", func() bool { return segLen(gr.rooms[0]) == 6 })
	want, epoch := gr.authenticators(), gr.rooms[0].Status().Epoch
	seg := gr.rooms[0].seg.id
	for i := range gr.nodes {
		gr.stop(i)
	}
	for i := range gr.nodes {
		gr.restart(i)
	}
	got := gr.authenticators()
	for i := range got {
		if string(got[i]) != string(want[i]) || gr.rooms[i].Status().Epoch != epoch {
			t.Fatalf("member %d reopened at another state", i)
		}
	}
	if gr.rooms[0].seg == nil || gr.rooms[0].seg.id != seg || segLen(gr.rooms[0]) != 6 {
		t.Fatal("the admin did not go on with its segment")
	}
	gr.waitMesh(2)
	for i := range 4 {
		if _, err := gr.rooms[1+i%3].Send(ctx, []byte(fmt.Sprintf("after-%d", i)), MessageID{}); err != nil {
			t.Fatal(err)
		}
	}
	waitFor(t, 30*time.Second, "the admin to store what came after", func() bool { return segLen(gr.rooms[0]) == 10 })
	for i := 1; i < 4; i++ {
		waitFor(t, 30*time.Second, "the messages after the restart", func() bool {
			for j := range 4 {
				if !gr.recs[i].has(fmt.Sprintf("after-%d", j)) {
					return false
				}
			}
			return true
		})
	}
}

func bodiesIn(rec *recorder, prefix string, n int) bool {
	for i := range n {
		if !rec.has(fmt.Sprintf("%s-%d", prefix, i)) {
			return false
		}
	}
	return true
}

// An admin killed in the middle of an epoch and started again loses no message: the other admin stored what it did not
// see, it reads those messages again from the other admin's segments, and a member that was away reads all of them from
// the restarted admin alone, which holds the other admin's logs too.
func TestAdminKilledMidEpochLosesNoMessage(t *testing.T) {
	gr := newGroup(t, 4, Limits{RepublishAfter: time.Second})
	gr.waitMesh(2)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	if err := gr.rooms[0].Promote(ctx, gr.nodes[1].key(), Admin); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 30*time.Second, "the second admin", func() bool { return gr.rooms[1].isAdmin() })
	gr.waitEpoch(gr.rooms[0].Status().Epoch)
	gr.stop(3)
	send := func(from []int, tag string, n int) {
		for i := range n {
			if _, err := gr.rooms[from[i%len(from)]].Send(ctx, []byte(fmt.Sprintf("%s-%d", tag, i)), MessageID{}); err != nil {
				t.Fatal(err)
			}
		}
	}
	send([]int{2, 0}, "a", 10)
	waitFor(t, 30*time.Second, "both admins to store the first ten", func() bool { return segLen(gr.rooms[0]) >= 10 && segLen(gr.rooms[1]) >= 10 })
	gr.stop(1)
	send([]int{2, 0}, "b", 10)
	waitFor(t, 30*time.Second, "the first admin to store them", func() bool { return len(storedIDs(gr.rooms[0])) == 20 })
	r1 := gr.restart(1)
	if !r1.isAdmin() {
		t.Fatal("the restarted admin does not run as an admin")
	}
	gr.waitPeers(2)
	send([]int{2, 0}, "c", 10)
	defer func() {
		if t.Failed() {
			dump(t, gr.rooms[0], gr.recs[0])
			dump(t, gr.rooms[1], gr.recs[1])
			t.Logf("receipts %d %d", gr.rooms[2].Status().Receipts, gr.rooms[0].Status().Receipts)
		}
	}()
	waitFor(t, 60*time.Second, "the restarted admin to read what it missed", func() bool {
		return bodiesIn(gr.recs[1], "b", 10) && bodiesIn(gr.recs[1], "c", 10)
	})
	waitFor(t, 30*time.Second, "the segments to hold the last ten", func() bool { return len(storedIDs(gr.rooms[0], gr.rooms[1])) == 30 })
	// The first admin goes away for good; the member that was away reads everything from the second.
	gr.stop(0)
	gr.restart(3)
	waitFor(t, 60*time.Second, "the member that was away to read everything", func() bool {
		return bodiesIn(gr.recs[3], "a", 10) && bodiesIn(gr.recs[3], "b", 10) && bodiesIn(gr.recs[3], "c", 10)
	})
}

// storedIDs returns the ids of the messages that the segments the rooms hold contain.
func storedIDs(rooms ...*Room) map[MessageID]bool {
	out := map[MessageID]bool{}
	for _, r := range rooms {
		for _, l := range r.snapshot() {
			if l.ctl {
				continue
			}
			for i := uint64(0); i < l.view.Len(); i++ {
				b, err := l.view.Get(i)
				if err != nil {
					continue
				}
				if st, err := decodeStored(b); err == nil {
					out[messageID(r.gid[:], st.Device[:], st.Seq, st.Body)] = true
				}
			}
		}
	}
	return out
}

// The state of a device on disk survives a restart as the state in memory does.
func TestRestartOverPebbleStores(t *testing.T) {
	dir := t.TempDir()
	gr := newGroupWith(t, 3, Limits{}, func(o *tnodeOpts) { o.dir = filepath.Join(dir, "admin") })
	gr.waitMesh(2)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	for i := range 5 {
		if _, err := gr.rooms[1].Send(ctx, []byte(fmt.Sprintf("m%d", i)), MessageID{}); err != nil {
			t.Fatal(err)
		}
	}
	waitFor(t, 30*time.Second, "the admin's segment", func() bool { return segLen(gr.rooms[0]) == 5 })
	want := authenticatorOf(gr.rooms[0])
	seg := gr.rooms[0].seg.id
	gr.stop(0)
	r := gr.restart(0)
	got := authenticatorOf(r)
	if string(got) != string(want) || r.seg == nil || r.seg.id != seg || segLen(r) != 5 || !r.isAdmin() {
		t.Fatal("the admin did not reopen as it was")
	}
	gr.waitPeers(2)
	if _, err := r.Send(ctx, []byte("after"), MessageID{}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 30*time.Second, "the message", func() bool { return gr.recs[1].has("after") && gr.recs[2].has("after") })
}

// A pin that accepts connections and never answers does not keep a device from reading the logs from its peers: a pull from
// a pin is given a time, so the replica it holds is free again for the follower.
func TestPinThatNeverAnswersDoesNotBlockTheOtherPeers(t *testing.T) {
	// The device asks the pins before its peers have connected, and the pins accept the connection and never answer.
	gr, sent, ps := pinnedGroup(t, Limits{PinWait: 20 * time.Millisecond})
	want := gr.rooms[0].Status().Epoch
	waitFor(t, 30*time.Second, "the admin to have pushed its logs", func() bool { return gr.rooms[0].Status().PinError == nil && !gr.rooms[0].dirtyAny() })
	ps[0].down()
	ps[1].down()
	r := gr.restart(2)
	rec := gr.recs[2]
	waitFor(t, 30*time.Second, "the epochs from the admin", func() bool { return r.Status().Epoch == want })
	waitFor(t, 30*time.Second, "every message from the admin", func() bool {
		for b := range sent {
			if !rec.has(b) {
				return false
			}
		}
		return true
	})
}
