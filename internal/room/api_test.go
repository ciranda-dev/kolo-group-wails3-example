package room

import (
	"context"
	"errors"
	"testing"
	"time"
)

func listed(n *Node, id GroupID) bool {
	for _, r := range n.Rooms() {
		if r.ID() == id {
			return true
		}
	}
	return false
}

// A room that this device was removed from, or left, is no longer listed by its node.
func TestNodeHidesARoomItLeftOrWasRemovedFrom(t *testing.T) {
	gr := newGroup(t, 3, Limits{})
	gr.waitMesh(2)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	id := gr.rooms[0].ID()
	if !listed(gr.nodes[2].n, id) {
		t.Fatal("a member does not list its room")
	}
	if err := gr.rooms[0].Remove(ctx, gr.nodes[2].person.PublicKey()); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 30*time.Second, "the removed device to drop the room", func() bool {
		_, err := gr.nodes[2].n.Room(id)
		return errors.Is(err, ErrRemoved) && !listed(gr.nodes[2].n, id)
	})
	lctx, stop := context.WithTimeout(ctx, 20*time.Second)
	defer stop()
	if err := gr.rooms[1].Leave(lctx); err != nil {
		t.Fatal(err)
	}
	if _, err := gr.nodes[1].n.Room(id); !errors.Is(err, ErrRemoved) {
		t.Errorf("Room after Leave: %v", err)
	}
	if listed(gr.nodes[1].n, id) {
		t.Error("Rooms lists a room the device left")
	}
	if !listed(gr.nodes[0].n, id) {
		t.Error("the admin no longer lists its room")
	}
}

// Members reads a page at a time, with the number of devices of the epoch.
func TestMembersPage(t *testing.T) {
	gr := newGroup(t, 3, Limits{})
	r := gr.rooms[0]
	all := r.Members()
	for _, c := range []struct{ offset, n, want int }{{0, 2, 2}, {2, 5, 1}, {0, 3, 3}, {3, 1, 0}, {9, 1, 0}, {-1, 1, 1}, {1, -1, 0}} {
		page, total := r.MembersPage(c.offset, c.n)
		if total != len(all) || len(page) != c.want {
			t.Fatalf("MembersPage(%d, %d): %d of %d, want %d of %d", c.offset, c.n, len(page), total, c.want, len(all))
		}
		for i, m := range page {
			if !m.Device.Equal(all[max(c.offset, 0)+i].Device) {
				t.Fatalf("MembersPage(%d, %d): device %d is not the one of Members", c.offset, c.n, i)
			}
		}
	}
}

// A room joined with a read link says so, and Status counts the devices of the room connected, not the readers.
func TestReaderAndPeersAreReported(t *testing.T) {
	gr, _ := newLinkGroup(t, 2)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	admin := gr.rooms[0]
	link, err := admin.Link()
	if err != nil {
		t.Fatal(err)
	}
	nd := newTNode(t, tnodeOpts{server: gr.server, limits: gr.lim})
	rd, err := nd.n.JoinLink(ctx, link)
	if err != nil {
		t.Fatal(err)
	}
	if !rd.Reader() || admin.Reader() || gr.rooms[1].Reader() {
		t.Fatalf("Reader: reader %v, admin %v, member %v", rd.Reader(), admin.Reader(), gr.rooms[1].Reader())
	}
	waitFor(t, 30*time.Second, "the reader connected to the admin", func() bool { return nd.conn(gr.nodes[0].key()) != nil })
	waitFor(t, 30*time.Second, "the peers", func() bool { return admin.Status().Peers == 1 && rd.Status().Peers >= 1 })
	gr.stop(1)
	waitFor(t, 30*time.Second, "the member gone", func() bool { return admin.Status().Peers == 0 })
}

// SinceCommit starts the rule at the epoch of the Commit that sets it.
func TestSetPolicySinceCommitIsTheEpochOfItsCommit(t *testing.T) {
	gr := newGroup(t, 2, Limits{})
	gr.waitMesh(1)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	admin := gr.rooms[0]
	e := admin.Status().Epoch
	for _, bad := range []Policy{{SinceCommit: true}, {History: SinceEpoch, Since: 3, SinceCommit: true}} {
		if err := admin.SetPolicy(ctx, bad); !errors.Is(err, ErrInvalid) {
			t.Fatalf("SetPolicy(%+v): %v", bad, err)
		}
	}
	if err := admin.SetPolicy(ctx, Policy{History: SinceEpoch, SinceCommit: true}); err != nil {
		t.Fatal(err)
	}
	if p, got := admin.Policy(), admin.Status().Epoch; p.History != SinceEpoch || p.Since != got || got != e+1 {
		t.Fatalf("policy %v since %d at epoch %d, was %d", p.History, p.Since, got, e)
	}
}

// Promote sends its offer again until the device answers, so a device that was offline when it started is promoted.
func TestPromoteReachesADeviceThatWasOffline(t *testing.T) {
	gr := newGroup(t, 3, Limits{RepublishAfter: time.Second})
	gr.waitMesh(2)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	gr.stop(1)
	done := make(chan error, 1)
	go func() { done <- gr.rooms[0].Promote(ctx, gr.nodes[1].key(), Admin) }()
	time.Sleep(3 * time.Second)
	r := gr.restart(1)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(45 * time.Second):
		t.Fatal("Promote did not finish after the device came back")
	}
	waitFor(t, 30*time.Second, "the new admin", r.isAdmin)
}
