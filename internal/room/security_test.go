package room

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ciranda-dev/kolo-mls/settle"
)

// A member that may not post under the current policy cannot post by sealing under the key of an epoch before the
// restriction: every validator refuses it and no admin stores it.
func TestBackdatedPostByAFormerPosterIsRefused(t *testing.T) {
	p0 := ident(t)
	gr := newGroupWith(t, 2, Limits{PastEpoch: 200 * time.Millisecond}, func(o *tnodeOpts) { o.person = p0 })
	gr.waitMesh(1)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	admin, member := gr.rooms[0], gr.rooms[1]
	old := member.Status().Epoch
	if err := admin.SetPolicy(ctx, Policy{Posters: []ed25519.PublicKey{p0.PublicKey()}}); err != nil {
		t.Fatal(err)
	}
	gr.waitEpoch(old + 1)
	time.Sleep(400 * time.Millisecond)
	m := sealAs(t, member, gr.nodes[1], old, member.seq.Add(1), "backdated")
	for i, r := range gr.rooms {
		if err := r.check(m); !errors.Is(err, ErrNotPoster) {
			t.Fatalf("validator %d on a post sealed under epoch %d (current %d): %v", i, old, r.Status().Epoch, err)
		}
	}
	before := segLen(admin)
	rawPublish(t, member, m)
	if _, err := admin.Send(ctx, []byte("after"), MessageID{}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 20*time.Second, "the poster's message", func() bool { return gr.recs[1].has("after") })
	if gr.recs[0].has("backdated") || gr.recs[1].has("backdated") {
		t.Fatal("a backdated post was shown")
	}
	if got := segLen(admin); got != before+1 {
		t.Fatalf("the admin's segment grew from %d to %d entries", before, got)
	}
}

// A device cannot post into an epoch it was not a leaf of, even with the key of that epoch.
func TestPostIntoAnEpochBeforeTheAuthorJoinedIsRefused(t *testing.T) {
	gr := newGroup(t, 2, Limits{PastEpoch: 200 * time.Millisecond})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	before := gr.rooms[0].Status().Epoch
	gr.join(ctx, gr.newNode())
	gr.waitMesh(1)
	time.Sleep(400 * time.Millisecond)
	admin := gr.rooms[0]
	m := sealAs(t, admin, gr.nodes[2], before, gr.rooms[2].seq.Add(1), "before my time")
	if err := admin.check(m); !errors.Is(err, ErrNotPoster) {
		t.Fatalf("a post of epoch %d by a device that joined in epoch %d: %v", before, before+1, err)
	}
	ok := sealAs(t, admin, gr.nodes[2], before+1, gr.rooms[2].seq.Add(1), "my time")
	if err := admin.check(ok); err != nil {
		t.Fatalf("a post of the epoch the device joined in: %v", err)
	}
}

// A SetPolicy that settles while a Promote waits for its device is kept: the Promote's Commit is built from the view of the
// moment it commits.
func TestPromoteKeepsAPolicySetWhileItWaits(t *testing.T) {
	p0 := ident(t)
	gr := newGroupWith(t, 2, Limits{}, func(o *tnodeOpts) { o.person = p0 })
	gr.waitMesh(1)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	admin := gr.rooms[0]
	done := make(chan error, 1)
	go func() { done <- admin.Promote(ctx, gr.nodes[1].key(), Admin) }()
	time.Sleep(5 * time.Millisecond)
	if err := admin.SetPolicy(ctx, Policy{Posters: []ed25519.PublicKey{p0.PublicKey()}}); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	v := admin.view.Load()
	if len(v.pol.persons) != 1 || len(v.roles.Admins) != 2 {
		t.Fatalf("after Promote: %d posters, %d admins at epoch %d", len(v.pol.persons), len(v.roles.Admins), v.epoch)
	}
}

// countPins counts the Commit objects read through a room's settle.Pins.
type countPins struct {
	settle.Pins
	n *atomic.Int64
}

func (c countPins) Get(ctx context.Context, key string, max int) ([]byte, error) {
	if isObject(key) {
		c.n.Add(1)
	}
	return c.Pins.Get(ctx, key, max)
}

// A SettledCommit with no quorum of settler signatures is reported once and its Commit is never fetched.
func TestUnsignedSettledCommitIsNotFetched(t *testing.T) {
	gr := newGroup(t, 2, Limits{})
	gr.waitMesh(1)
	admin, member := gr.rooms[0], gr.rooms[1]
	e := admin.Status().Epoch
	big := make([]byte, 1<<20)
	rand.Read(big)
	h := sha256Of(big)
	forged := &settle.Settled{TBS: settle.TBS{GroupID: admin.gid[:], Epoch: e, Commit: h, Braid: admin.view.Load().roles.Braid}}
	var n atomic.Int64
	member.mu.Lock()
	member.pins = countPins{member.pins, &n}
	member.mu.Unlock()
	if err := admin.appendCtl(append(pieces(h, big), &ctlRecord{Kind: ctlPut, Key: settle.SettledKey(admin.gid[:], e), Value: forged.Encode()})...); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 20*time.Second, "the report of the SettledCommit", func() bool { return len(errEvents(gr.recs[1])) > 0 })
	time.Sleep(6 * time.Second)
	if got := n.Load(); got != 0 {
		t.Fatalf("the member read the Commit of an unsigned SettledCommit %d times", got)
	}
	if evs := errEvents(gr.recs[1]); len(evs) != 1 || !errors.Is(evs[0].Err, ErrCommitRef) {
		t.Fatalf("reports: %v", evs)
	}
}

func errEvents(rec *recorder) (out []Event) {
	for _, ev := range rec.events() {
		if ev.Err != nil {
			out = append(out, ev)
		}
	}
	return out
}

// A second Commit that the settlers settle for an epoch is reported with ErrEquivocation.
func TestSecondSettledCommitOfAnEpochIsReported(t *testing.T) {
	gr := newGroup(t, 2, Limits{})
	gr.waitMesh(1)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	admin := gr.rooms[0]
	e := admin.Status().Epoch
	var other [32]byte
	rand.Read(other[:])
	tbs := settle.TBS{GroupID: admin.gid[:], Epoch: e, Commit: other, Braid: admin.view.Load().roles.Braid}
	sig, err := gr.nodes[0].dev.Sign(settle.SettledContext, tbs.Encode())
	if err != nil {
		t.Fatal(err)
	}
	forged := &settle.Settled{TBS: tbs, Shares: []settle.Share{{Signer: [32]byte(gr.nodes[0].key()), Signature: [64]byte(sig)}}}
	if err := admin.SetPolicy(ctx, Policy{AdminsPost: true}); err != nil {
		t.Fatal(err)
	}
	gr.waitEpoch(e + 1)
	if err := admin.appendCtl(&ctlRecord{Kind: ctlPut, Key: settle.SettledKey(admin.gid[:], e), Value: forged.Encode()}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 20*time.Second, "a report", func() bool { return len(errEvents(gr.recs[1])) > 0 })
	if !hasErr(gr.recs[1], ErrEquivocation) {
		t.Fatalf("reports: %v", errEvents(gr.recs[1]))
	}
}

// An entry of a segment sealed under an epoch after the segment's is reported, and the entries after it are read.
func TestFarEpochEntryIsReportedAndTheSegmentGoesOn(t *testing.T) {
	gr := newGroup(t, 2, Limits{CatchUpWait: 2 * time.Second, PinWait: 500 * time.Millisecond})
	gr.waitMesh(1)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	admin := gr.rooms[0]
	if _, err := admin.Send(ctx, []byte("first"), MessageID{}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 20*time.Second, "first", func() bool { return gr.recs[1].has("first") })
	gr.stop(1)
	bad := &sealed{Epoch: 1 << 40, CT: []byte{1, 2, 3}}
	if err := admin.archive(&stored{Device: [32]byte(gr.nodes[0].key()), Seq: 1, Body: bad.encode()}); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Send(ctx, []byte("after"), MessageID{}); err != nil {
		t.Fatal(err)
	}
	r := gr.restart(1)
	waitFor(t, 30*time.Second, "the message after the far entry", func() bool { return gr.recs[1].has("after") })
	waitFor(t, 10*time.Second, "the report", func() bool { return hasErr(gr.recs[1], ErrWrongEpoch) })
	waitFor(t, 10*time.Second, "the end of the catch-up", func() bool { return !r.Status().CatchingUp })
}
