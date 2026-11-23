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

// With the admin and every pin away, a joiner pulls its tree from the members that replicated the admin's tree log.
func TestJoinerGetsTheTreeFromMembersWhenTheAdminIsAway(t *testing.T) {
	gr := newGroup(t, 3, Limits{})
	gr.waitMesh(2)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
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

// Joiners of a large group pull the sealed tree from the three devices that serve it, which hold no pin: the tree of a group
// of KOLO_GROUP_LEAVES leaves, 100 joiners at 30,000 leaves and 16 below.
func TestManyJoinersFetchALargeTreeFromThreeDevices(t *testing.T) {
	if testing.Short() {
		t.Skip("many devices")
	}
	leaves := largeLeaves(t)
	joiners := 16
	if leaves >= 30000 {
		joiners = 100
	}
	if s := os.Getenv("KOLO_GROUP_JOINERS"); s != "" {
		joiners, _ = strconv.Atoi(s)
	}
	lg := buildLargeGroup(t, leaves)
	gr := newGroup(t, 3, Limits{})
	gr.waitMesh(2)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	var nds []*tnode
	var kps [][]byte
	for range joiners {
		nd := gr.newNode()
		kp, err := nd.n.KeyPackage()
		if err != nil {
			t.Fatal(err)
		}
		nds, kps = append(nds, nd), append(kps, kp)
	}
	admin := gr.rooms[0]
	if _, err := admin.Add(ctx, kps...); err != nil {
		t.Fatal(err)
	}
	// The admin serves the large tree as one more tree log, of the epoch after: the joiners are devices of the room now.
	header, entries, err := admin.writeTree(admin.view.Load().epoch+1, lg.sealed)
	if err != nil {
		t.Fatal(err)
	}
	inv := &invite{TreeHeader: header, TreeEntries: entries, TreeHash: sha256.Sum256(lg.sealed), Secret: admin.secret}
	holdsTree(t, gr.rooms[1], inv)
	holdsTree(t, gr.rooms[2], inv)
	sent := func() (n uint64) {
		for _, nd := range gr.nodes {
			n += nd.n.sync.Stats().BytesSent
		}
		return n
	}
	before, start := sent(), time.Now()
	took := make([]time.Duration, joiners)
	var wg sync.WaitGroup
	// Eight at a time: one process runs every device, and a lookup that finds many dials at once can wait 20 s for its
	// connection (kolo-dht's random punch interval).
	sem := make(chan struct{}, 8)
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
	slices.Sort(took)
	total := sent() - before
	t.Logf("%d leaves: %d joiners pulled a sealed tree of %d bytes in %d pieces from 3 devices, no pin: the last in %v, the median in %v; the servers sent %d bytes (%.1f copies); the group took %v to build",
		leaves, joiners, len(lg.sealed), entries, took[len(took)-1].Round(time.Millisecond), took[len(took)/2].Round(time.Millisecond), total, float64(total)/float64(len(lg.sealed)), lg.build.Round(time.Millisecond))
}
