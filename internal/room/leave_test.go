package room

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

// nextRemoved waits for the Removed event of r and then for ErrRemoved.
func nextRemoved(t testing.TB, r *Room, d time.Duration) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()
	for {
		ev, err := r.Next(ctx)
		if errors.Is(err, ErrRemoved) {
			return
		}
		if err != nil {
			t.Fatalf("Next: %v", err)
		}
		_ = ev
	}
}

func hasPerson(r *Room, person []byte) bool {
	for _, m := range r.Members() {
		if string(m.Person) == string(person) {
			return true
		}
	}
	return false
}

// A device that an admin removes learns it: the Commit that removes it reaches it, as it did not when the others closed
// their connections to it first.
func TestRemovedDeviceLearnsItWasRemoved(t *testing.T) {
	for i := range 3 {
		gr := newHistoryGroup(t, 3, Policy{})
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		gr.advance(t, ctx, 8, "e")
		if err := gr.rooms[0].Remove(ctx, gr.nodes[2].person.PublicKey()); err != nil {
			t.Fatal(err)
		}
		cancel()
		nextRemoved(t, gr.rooms[2], 20*time.Second)
		t.Logf("run %d: the removed device learned it", i)
	}
}

// A member that leaves stops its room at once, and the admins remove its person.
func TestLeaveRemovesThePerson(t *testing.T) {
	gr := newGroup(t, 3, Limits{})
	gr.waitMesh(2)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	leaver, person := gr.rooms[2], gr.nodes[2].person.PublicKey()
	e := gr.rooms[0].Status().Epoch
	epoch := e
	lctx, stop := context.WithTimeout(ctx, 20*time.Second)
	defer stop()
	if err := leaver.Leave(lctx); err != nil {
		t.Fatal(err)
	}
	if e := gr.rooms[0].Status().Epoch; e != epoch+1 {
		t.Fatalf("Leave returned before the Commit: epoch %d", e)
	}
	nextRemoved(t, leaver, 10*time.Second)
	defer func() {
		if t.Failed() {
			for _, ev := range gr.recs[0].events() {
				t.Logf("admin event: %+v", ev)
			}
		}
	}()
	if _, err := leaver.Send(ctx, []byte("x"), MessageID{}); !errors.Is(err, ErrRemoved) {
		t.Errorf("Send after Leave: %v", err)
	}
	if err := leaver.Leave(ctx); !errors.Is(err, ErrRemoved) {
		t.Errorf("Leave twice: %v", err)
	}
	for _, i := range []int{0, 1} {
		waitFor(t, 30*time.Second, fmt.Sprintf("the person removed at member %d, epochs %d %d (was %d)", i, gr.rooms[0].Status().Epoch, gr.rooms[1].Status().Epoch, e), func() bool { return gr.rooms[i].Status().Epoch == e+1 && !hasPerson(gr.rooms[i], person) })
	}
	time.Sleep(time.Second)
	if got := gr.rooms[0].Status().Epoch; got != e+1 {
		t.Errorf("epoch %d: the request made more than one Commit", got)
	}
	if _, err := gr.rooms[0].Send(ctx, []byte("after"), MessageID{}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 20*time.Second, "the others", func() bool { return gr.recs[1].has("after") })
	gr.nodes[2].restart()
	if n := len(gr.nodes[2].n.Rooms()); n != 0 {
		t.Errorf("a device that left reopened %d rooms", n)
	}
}

// With no admin online the request goes unanswered: Leave returns when its context ends, and the room has stopped.
func TestLeaveWithNoAdminStopsTheRoomAndSaysSo(t *testing.T) {
	gr := newGroup(t, 2, Limits{})
	gr.waitMesh(1)
	gr.stop(0)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := gr.rooms[1].Leave(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Leave: %v", err)
	}
	nextRemoved(t, gr.rooms[1], 5*time.Second)
}

// With two admins one of them answers the request, so the room makes one Commit.
func TestLeaveWithTwoAdminsMakesOneCommit(t *testing.T) {
	gr := newGroup(t, 3, Limits{})
	gr.waitMesh(2)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	if err := gr.rooms[0].Promote(ctx, gr.nodes[1].key(), Admin); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 30*time.Second, "the second admin", func() bool { return gr.rooms[1].isAdmin() })
	gr.waitEpoch(gr.rooms[0].Status().Epoch)
	e, person := gr.rooms[0].Status().Epoch, gr.nodes[2].person.PublicKey()
	lctx, stop := context.WithTimeout(ctx, 20*time.Second)
	defer stop()
	if err := gr.rooms[2].Leave(lctx); err != nil {
		t.Fatal(err)
	}
	for _, i := range []int{0, 1} {
		waitFor(t, 30*time.Second, "the person removed", func() bool { return gr.rooms[i].Status().Epoch == e+1 && !hasPerson(gr.rooms[i], person) })
	}
	time.Sleep(2 * time.Second)
	if a, b := gr.rooms[0].Status().Epoch, gr.rooms[1].Status().Epoch; a != e+1 || b != e+1 {
		t.Errorf("epochs %d and %d, want %d", a, b, e+1)
	}
}
