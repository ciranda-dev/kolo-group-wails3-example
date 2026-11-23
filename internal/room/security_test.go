package room

import (
	"context"
	"crypto/ed25519"
	"errors"
	"testing"
	"time"
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
