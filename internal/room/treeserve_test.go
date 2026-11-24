package room

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"os"
	"slices"
	"strconv"
	"sync"
	"testing"
	"time"

	kololog "github.com/ciranda-dev/kolo-log"
	"github.com/ciranda-dev/kolo-pin"
)

// treeLogOf returns the log of the sealed tree that r holds, own or replica.
func treeLogOf(r *Room, own bool) *roomLog {
	for _, l := range r.snapshot() {
		if l.tree && l.own == own {
			return l
		}
	}
	return nil
}

// unused counts the devices whose Welcome this admin has not seen used.
func unused(r *Room) int {
	r.wmu.Lock()
	defer r.wmu.Unlock()
	return len(r.welcomed)
}

func ownTreeLogs(r *Room) (n int) {
	for _, l := range r.snapshot() {
		if l.tree && l.own {
			n++
		}
	}
	return n
}

// holdsTree waits until r holds a whole replica of the tree log that inv names.
func holdsTree(t *testing.T, r *Room, inv *invite) {
	t.Helper()
	hd, err := kololog.DecodeLogHeader(inv.TreeHeader)
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, 30*time.Second, "a replica of the tree log", func() bool {
		r.logMu.Lock()
		defer r.logMu.Unlock()
		l := r.logs[keyOf([32]byte(hd.LogKey), hd.LogID)]
		return l != nil && l.view.Len() >= inv.TreeEntries
	})
}

// kpOf returns a key package of nd.
func kpOf(t *testing.T, nd *tnode) []byte {
	t.Helper()
	kp, err := nd.n.KeyPackage()
	if err != nil {
		t.Fatal(err)
	}
	return kp
}

// addFor makes an Add of nd's key package by admin and returns the Welcome and its invite.
func addFor(t *testing.T, ctx context.Context, admin *Room, nd *tnode) ([]byte, *invite) {
	t.Helper()
	kp, err := nd.n.KeyPackage()
	if err != nil {
		t.Fatal(err)
	}
	w, err := admin.Add(ctx, kp)
	if err != nil {
		t.Fatal(err)
	}
	inv, err := decodeInvite(w, admin.n.limits.MaxTreeBytes)
	if err != nil {
		t.Fatal(err)
	}
	return w, inv
}

// A member that serves trees keeps the tree log of an Add and answers a joiner with it when the admin is away.
func TestOptedInMembersServeTheTreeWhenTheAdminIsAway(t *testing.T) {
	gr := newGroupServe(t, 3, Limits{PinWait: 300 * time.Millisecond}, nil, true, nil)
	gr.waitMesh(2)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	nd := gr.newNode()
	w, inv := addFor(t, ctx, gr.rooms[0], nd)
	holdsTree(t, gr.rooms[1], inv)
	holdsTree(t, gr.rooms[2], inv)
	gr.stop(0)
	r, err := nd.n.Join(ctx, w)
	if err != nil {
		t.Fatal(err)
	}
	if got := len(r.Members()); got != 4 {
		t.Fatalf("the joiner sees %d members", got)
	}
}

// Members that do not serve trees (the default) hold no replica of a tree log and send no byte of it; the admin and a member
// that serves do.
func TestMembersThatDoNotServeHoldAndSendNoTree(t *testing.T) {
	gr := newGroup(t, 3, Limits{})
	gr.waitMesh(2)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	server := newTNode(t, tnodeOpts{server: gr.server, serve: true})
	gr.join(ctx, server)
	gr.waitMesh(2)
	nd := gr.newNode()
	_, inv := addFor(t, ctx, gr.rooms[0], nd)
	holdsTree(t, gr.rooms[3], inv)
	gr.waitEpoch(gr.rooms[0].Status().Epoch)
	time.Sleep(300 * time.Millisecond)
	for i := 1; i <= 2; i++ {
		if l := treeLogOf(gr.rooms[i], false); l != nil {
			t.Fatalf("member %d holds a replica of a tree log", i)
		}
	}
	before := make([]uint64, len(gr.nodes))
	for i, g := range gr.nodes {
		before[i] = g.n.sync.Stats().BytesSent
	}
	sealed, err := nd.n.fetchTree(ctx, inv)
	if err != nil || len(sealed) == 0 {
		t.Fatalf("fetchTree: %d bytes, %v", len(sealed), err)
	}
	sent := func(i int) uint64 { return gr.nodes[i].n.sync.Stats().BytesSent - before[i] }
	if sent(0) < uint64(len(sealed)) {
		t.Fatalf("the admin sent %d bytes of a tree of %d", sent(0), len(sealed))
	}
	for i := 1; i <= 2; i++ {
		if n := sent(i); n != 0 {
			t.Fatalf("member %d, which does not serve trees, sent %d bytes", i, n)
		}
	}
}

// With no admin and no pin, a joiner that no device serves waits and retries until its context ends.
func TestJoinerWithNoServerWaitsUntilItsContextEnds(t *testing.T) {
	gr := newGroup(t, 3, Limits{PinWait: 300 * time.Millisecond})
	gr.waitMesh(2)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	nd := gr.newNode()
	w, _ := addFor(t, ctx, gr.rooms[0], nd)
	gr.waitEpoch(gr.rooms[0].Status().Epoch)
	time.Sleep(500 * time.Millisecond)
	gr.stop(0)
	jctx, jcancel := context.WithTimeout(ctx, 3*time.Second)
	defer jcancel()
	start := time.Now()
	_, err := nd.n.Join(jctx, w)
	if !errors.Is(err, ErrTree) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("a join with no server: %v", err)
	}
	if took := time.Since(start); took < 2*time.Second {
		t.Fatalf("the join gave up after %v", took)
	}
}

// A joiner that finds no server joins once the admin is back.
func TestJoinerWaitsForTheAdminToReturn(t *testing.T) {
	gr := newGroupWith(t, 2, Limits{PinWait: 300 * time.Millisecond}, func(o *tnodeOpts) { o.dir = t.TempDir() })
	gr.waitMesh(1)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	nd := gr.newNode()
	w, _ := addFor(t, ctx, gr.rooms[0], nd)
	gr.stop(0)
	done := make(chan error, 1)
	go func() {
		_, err := nd.n.Join(ctx, w)
		done <- err
	}()
	select {
	case err := <-done:
		t.Fatalf("the join ended with the admin away: %v", err)
	case <-time.After(time.Second):
	}
	gr.restart(0)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

// A member that served trees and is restarted without the option drops the replicas it kept.
func TestRestartWithoutServeTreeDropsTheReplica(t *testing.T) {
	gr := newGroup(t, 2, Limits{})
	gr.waitMesh(1)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	server := newTNode(t, tnodeOpts{server: gr.server, serve: true, dir: t.TempDir()})
	gr.join(ctx, server)
	gr.waitMesh(1)
	_, inv := addFor(t, ctx, gr.rooms[0], gr.newNode())
	holdsTree(t, gr.rooms[2], inv)
	server.opts.serve = false
	r := gr.restart(2)
	time.Sleep(300 * time.Millisecond)
	if treeLogOf(r, false) != nil {
		t.Fatal("a member that does not serve trees reopened a replica of a tree log")
	}
}

// With no device of the room online, a joiner pulls its tree from the pins that the admin pushed the tree log to.
func TestJoinerGetsTheTreeFromAPinWhenNoDeviceServes(t *testing.T) {
	ps := []*pinServer{newPin(t), newPin(t)}
	peers := []pin.Peer{ps[0].peer, ps[1].peer}
	gr := newGroupPins(t, 2, Limits{PinGap: 20 * time.Millisecond, PinWait: 300 * time.Millisecond}, peers, nil)
	gr.waitMesh(1)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	nd := gr.newNode()
	kp, err := nd.n.KeyPackage()
	if err != nil {
		t.Fatal(err)
	}
	w, err := gr.rooms[0].Add(ctx, kp)
	if err != nil {
		t.Fatal(err)
	}
	inv, err := decodeInvite(w, gr.rooms[0].n.limits.MaxTreeBytes)
	if err != nil {
		t.Fatal(err)
	}
	hd, _ := kololog.DecodeLogHeader(inv.TreeHeader)
	pinsHold(t, peers, gr.nodes[0].dev, hd.LogID, inv.TreeEntries)
	gr.stop(0)
	gr.stop(1)
	if _, err := nd.n.Join(ctx, w); err != nil {
		t.Fatal(err)
	}
}

// With the admin away and members that do not serve, a joiner pulls its tree from the pins.
func TestJoinerGetsTheTreeFromPinsWhenTheAdminIsAway(t *testing.T) {
	ps := []*pinServer{newPin(t), newPin(t)}
	peers := []pin.Peer{ps[0].peer, ps[1].peer}
	gr := newGroupPins(t, 3, Limits{PinGap: 20 * time.Millisecond, PinWait: 300 * time.Millisecond}, peers, nil)
	gr.waitMesh(2)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	nd := gr.newNode()
	w, inv := addFor(t, ctx, gr.rooms[0], nd)
	hd, _ := kololog.DecodeLogHeader(inv.TreeHeader)
	pinsHold(t, peers, gr.nodes[0].dev, hd.LogID, inv.TreeEntries)
	gr.stop(0)
	if _, err := nd.n.Join(ctx, w); err != nil {
		t.Fatal(err)
	}
	if ps[0].served()+ps[1].served() == 0 {
		t.Fatal("no pin served the tree")
	}
}

// With the pins down, a joiner pulls its tree from the admin, which does not wait for them.
func TestJoinerGetsTheTreeFromTheAdminWhenThePinsAreDown(t *testing.T) {
	ps := []*pinServer{newPin(t), newPin(t)}
	peers := []pin.Peer{ps[0].peer, ps[1].peer}
	gr := newGroupPins(t, 2, Limits{PinGap: 20 * time.Millisecond, PinWait: 300 * time.Millisecond}, peers, nil)
	gr.waitMesh(1)
	ps[0].down()
	ps[1].down()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	nd := gr.newNode()
	w, _ := addFor(t, ctx, gr.rooms[0], nd)
	if _, err := nd.n.Join(ctx, w); err != nil {
		t.Fatal(err)
	}
}

// A tree that does not hash to what the invite says, or that the Welcome does not open, never makes a room.
func TestJoinerRefusesAWrongTree(t *testing.T) {
	gr := newGroup(t, 2, Limits{})
	gr.waitMesh(1)
	admin := gr.rooms[0]
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	invite := func(nd *tnode) *invite {
		kp, err := nd.n.KeyPackage()
		if err != nil {
			t.Fatal(err)
		}
		w, err := admin.Add(ctx, kp)
		if err != nil {
			t.Fatal(err)
		}
		inv, err := decodeInvite(w, admin.n.limits.MaxTreeBytes)
		if err != nil {
			t.Fatal(err)
		}
		return inv
	}
	liar, odd := gr.newNode(), gr.newNode()
	// The invite names a hash that the tree does not have.
	inv := invite(liar)
	inv.TreeHash[0] ^= 1
	if _, err := liar.n.Join(ctx, inv.encode()); !errors.Is(err, ErrTree) {
		t.Fatalf("a tree against another hash: %v", err)
	}
	// The log holds bytes that hash as the invite says and are not the Welcome's tree.
	inv = invite(odd)
	junk := randomBytes(t, 4000)
	header, entries, err := admin.writeTree(admin.view.Load().epoch+7, junk)
	if err != nil {
		t.Fatal(err)
	}
	inv.TreeHeader, inv.TreeEntries, inv.TreeHash = header, entries, sha256.Sum256(junk)
	if _, err := odd.n.Join(ctx, inv.encode()); err == nil {
		t.Fatal("a tree that is not the Welcome's was accepted")
	}
	for _, nd := range []*tnode{liar, odd} {
		if n := len(nd.n.Rooms()); n != 0 {
			t.Fatalf("a refused join left %d rooms", n)
		}
	}
}

// A Welcome that is not used keeps its tree on the admin through later epochs and a joiner of it still joins; once it is
// used, its tree goes when the room has moved on.
func TestTreeLogGoesWithItsUsedWelcome(t *testing.T) {
	gr := newGroup(t, 2, Limits{})
	gr.waitMesh(1)
	admin := gr.rooms[0]
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if _, err := gr.rooms[1].Send(ctx, []byte("first"), MessageID{}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 30*time.Second, "the first member's Welcome used", func() bool { return unused(admin) == 0 })
	late := gr.newNode()
	kp, err := late.n.KeyPackage()
	if err != nil {
		t.Fatal(err)
	}
	old, err := admin.Add(ctx, kp)
	if err != nil {
		t.Fatal(err)
	}
	for range 3 {
		gr.join(ctx, gr.newNode())
		gr.waitMesh(1)
		if _, err := gr.rooms[len(gr.rooms)-1].Send(ctx, []byte("here"), MessageID{}); err != nil {
			t.Fatal(err)
		}
	}
	defer func() {
		if t.Failed() {
			for _, l := range admin.snapshot() {
				t.Logf("log tree=%v own=%v epoch=%d len=%d; unused welcomes %d; epoch %d", l.tree, l.own, l.epoch, l.view.Len(), unused(admin), admin.Status().Epoch)
			}
		}
	}()
	waitFor(t, 30*time.Second, "the trees of used Welcomes to go", func() bool { return ownTreeLogs(admin) == 2 })
	time.Sleep(300 * time.Millisecond)
	if n := ownTreeLogs(admin); n != 2 {
		t.Fatalf("the admin keeps %d tree logs, want the unused Welcome's and the latest", n)
	}
	r, err := late.n.Join(ctx, old)
	if err != nil {
		t.Fatalf("a joiner of an old Welcome: %v", err)
	}
	waitFor(t, 30*time.Second, "the late joiner at the latest epoch", func() bool { return r.Status().Epoch == admin.Status().Epoch })
	if _, err := r.Send(ctx, []byte("late"), MessageID{}); err != nil {
		t.Fatal(err)
	}
	gr.join(ctx, gr.newNode())
	waitFor(t, 30*time.Second, "the tree of the used Welcome to go", func() bool { return ownTreeLogs(admin) == 1 })
}

// Joiners of a large group (KOLO_GROUP_LEAVES leaves, 100 joiners at 30,000 and 16 below, or KOLO_GROUP_JOINERS) pull its
// sealed tree, first with the admin up and then with the admin away, when only the two pins serve it. The members serve no
// byte of it. KOLO_GROUP_FETCHERS joiners (8) fetch at a time.
func TestManyJoinersFetchALargeTreeFromTheAdminAndThePins(t *testing.T) {
	if testing.Short() {
		t.Skip("many devices")
	}
	leaves := largeLeaves(t)
	joiners := 16
	if leaves >= 30000 {
		joiners = 100
	}
	envInt := func(name string, def int) int {
		if s := os.Getenv(name); s != "" {
			def, _ = strconv.Atoi(s)
		}
		return def
	}
	joiners, fetchers := envInt("KOLO_GROUP_JOINERS", joiners), envInt("KOLO_GROUP_FETCHERS", 8)
	lg := buildLargeGroup(t, leaves)
	ps := []*pinServer{newPin(t), newPin(t)}
	peers := []pin.Peer{ps[0].peer, ps[1].peer}
	gr := newGroupPins(t, 3, Limits{PinGap: 20 * time.Millisecond}, peers, nil)
	gr.waitMesh(2)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()
	admin := gr.rooms[0]
	nds := make([]*tnode, joiners)
	kps := make([][]byte, joiners)
	for i := range nds {
		nds[i] = newTNode(t, tnodeOpts{server: gr.server, pins: peers})
		kps[i] = kpOf(t, nds[i])
	}
	if _, err := admin.Add(ctx, kps...); err != nil {
		t.Fatal(err)
	}
	header, entries, err := admin.writeTree(admin.view.Load().epoch+1, lg.sealed)
	if err != nil {
		t.Fatal(err)
	}
	inv := &invite{TreeHeader: header, TreeEntries: entries, TreeHash: sha256.Sum256(lg.sealed), Secret: admin.secret}
	hd, _ := kololog.DecodeLogHeader(header)
	pinsHold(t, peers, gr.nodes[0].dev, hd.LogID, entries)
	run := func(nds []*tnode) (took []time.Duration, total time.Duration) {
		took = make([]time.Duration, joiners)
		var wg sync.WaitGroup
		// One process runs every device, and a lookup that finds many dials at once can wait 20 s for its connection
		// (kolo-dht's random punch interval).
		sem := make(chan struct{}, fetchers)
		start := time.Now()
		for i, nd := range nds {
			sem <- struct{}{}
			wg.Go(func() {
				defer func() { <-sem }()
				got, err := nd.n.fetchTree(ctx, inv)
				if err != nil || !bytes.Equal(got, lg.sealed) {
					t.Errorf("joiner %d: %d bytes, %v", i, len(got), err)
				}
				took[i] = time.Since(start)
			})
		}
		wg.Wait()
		total = time.Since(start)
		slices.Sort(took)
		return took, total
	}
	sentBy := func() (out []uint64) {
		for _, g := range gr.nodes {
			out = append(out, g.n.sync.Stats().BytesSent)
		}
		return append(out, ps[0].served(), ps[1].served())
	}
	diff := func(a, b []uint64) (out []uint64) {
		for i := range a {
			out = append(out, a[i]-b[i])
		}
		return out
	}
	sealed := uint64(len(lg.sealed))

	before := sentBy()
	took, total := run(nds)
	d1 := diff(sentBy(), before)
	t.Logf("%d leaves, %d joiners, admin up: %d at a time, the last done after %v, the median after %v; bytes sent: admin %d (%.1f copies), members %d and %d, pins %d and %d",
		leaves, joiners, fetchers, total.Round(time.Millisecond), took[len(took)/2].Round(time.Millisecond), d1[0], float64(d1[0])/float64(sealed), d1[1], d1[2], d1[3], d1[4])
	// After PinWait a joiner asks every device, and one that holds no tree answers not found in 63 bytes.
	if d1[0]+d1[3]+d1[4] < uint64(joiners)*sealed || d1[1] > uint64(joiners)*200 || d1[2] > uint64(joiners)*200 {
		t.Fatalf("the admin and the pins must serve the tree: %v", d1)
	}

	// A joiner whose replica stays in the store would not ask again, since the admin that wrote the log runs in this process.
	gr.stop(0)
	for i := range nds {
		nds[i] = newTNode(t, tnodeOpts{server: gr.server, limits: Limits{PinWait: 300 * time.Millisecond}, pins: peers})
	}
	before = sentBy()
	took, total = run(nds)
	d2 := diff(sentBy(), before)
	t.Logf("%d leaves, %d joiners, admin away: %d at a time, all done in %v (%.2f joiners a second, %.0f MB/s), the median after %v; bytes sent: pins %d (%.1f copies) and %d (%.1f copies), members %d and %d",
		leaves, joiners, fetchers, total.Round(time.Millisecond), float64(joiners)/total.Seconds(), float64(d2[3]+d2[4])/1e6/total.Seconds(), took[len(took)/2].Round(time.Millisecond),
		d2[3], float64(d2[3])/float64(sealed), d2[4], float64(d2[4])/float64(sealed), d2[1], d2[2])
	if d2[3]+d2[4] < uint64(joiners)*sealed {
		t.Fatalf("the pins sent %d bytes", d2[3]+d2[4])
	}
	for _, n := range d2[1:3] {
		if n > uint64(joiners)*200 {
			t.Fatalf("a member that does not serve sent %d bytes", n)
		}
	}
}
