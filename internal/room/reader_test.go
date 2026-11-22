package room

import (
	"context"
	"crypto/ed25519"
	"errors"
	"testing"
	"time"
)

func newLinkGroup(t testing.TB, n int) (*group, ed25519.PublicKey) {
	t.Helper()
	p0 := ident(t)
	gr := newGroupWith(t, n, Limits{KeepSegments: 200, CatchUpWait: 10 * time.Second}, func(o *tnodeOpts) {
		o.person, o.policy = p0, Policy{Posters: []ed25519.PublicKey{p0.PublicKey()}, ReadLink: true}
	})
	gr.waitMesh(n - 1)
	return gr, p0.PublicKey()
}

// A device that holds a read link reads the room live, across epochs, and its history, and cannot post.
func TestReaderJoinsByALinkReadsAndCannotPost(t *testing.T) {
	gr, _ := newLinkGroup(t, 2)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	admin := gr.rooms[0]
	old := gr.advance(t, ctx, 3, "old")
	link, err := gr.rooms[1].Link()
	if err != nil {
		t.Fatal(err)
	}
	nd := newTNode(t, tnodeOpts{server: gr.server, limits: gr.lim})
	rd, err := nd.n.JoinLink(ctx, link)
	if err != nil {
		t.Fatal(err)
	}
	rec := record(t, rd)
	waitFor(t, 60*time.Second, "the reader's mesh", func() bool { return rd.gr.Stats().Mesh >= 1 })
	if _, err := admin.Send(ctx, []byte("live-1"), MessageID{}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 20*time.Second, "the live message", func() bool { return rec.has("live-1") })
	if _, err := rd.Send(ctx, []byte("no"), MessageID{}); !errors.Is(err, ErrNotPoster) {
		t.Fatalf("Send by a reader: %v", err)
	}
	got := bodiesOf(t, readHistory(t, ctx, rd, 10))
	for body, e := range old {
		if got[body] != e {
			t.Errorf("history lacks %s of epoch %d", body, e)
		}
	}
	e := admin.Status().Epoch
	if _, err := admin.Add(ctx, ghostKeyPackage(t, gr)); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 30*time.Second, "the reader at the next epoch", func() bool { return rd.Status().Epoch == e+1 })
	if _, err := admin.Send(ctx, []byte("live-2"), MessageID{}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 20*time.Second, "the live message of the next epoch", func() bool { return rec.has("live-2") })
	for _, m := range admin.Members() {
		if m.Device.Equal(nd.key()) {
			t.Error("a reader is a member of the room")
		}
	}
	if _, err := rd.Link(); !errors.Is(err, ErrNotMember) {
		t.Errorf("Link of a reader: %v", err)
	}
	if err := rd.Remove(ctx, ident(t).PublicKey()); !errors.Is(err, ErrNotAdmin) {
		t.Errorf("Remove by a reader: %v", err)
	}
	if err := rd.SetPolicy(ctx, Policy{}); !errors.Is(err, ErrNotAdmin) {
		t.Errorf("SetPolicy by a reader: %v", err)
	}
}

// A reader that leaves stops its room and forgets the logs it held.
func TestReaderLeavesAndForgetsItsLogs(t *testing.T) {
	gr, _ := newLinkGroup(t, 2)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	link, err := gr.rooms[0].Link()
	if err != nil {
		t.Fatal(err)
	}
	nd := newTNode(t, tnodeOpts{server: gr.server, limits: gr.lim})
	rd, err := nd.n.JoinLink(ctx, link)
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, 30*time.Second, "the reader's logs", func() bool { return rd.Status().Logs > 0 })
	if err := rd.Leave(ctx); err != nil {
		t.Fatal(err)
	}
	nextRemoved(t, rd, 5*time.Second)
	if n := rd.Status().Logs; n != 0 {
		t.Errorf("the reader still holds %d logs", n)
	}
}

// Turning the read link off and on again makes a new link: the old reader reads nothing of the later epochs.
func TestNewLinkStopsTheOldReader(t *testing.T) {
	gr, p0 := newLinkGroup(t, 2)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
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
	rec := record(t, rd)
	waitFor(t, 60*time.Second, "the reader's mesh", func() bool { return rd.gr.Stats().Mesh >= 1 })
	posters := []ed25519.PublicKey{p0}
	e := admin.Status().Epoch
	if err := admin.SetPolicy(ctx, Policy{Posters: posters}); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Link(); !errors.Is(err, ErrNoLink) {
		t.Fatalf("Link of a room that turned it off: %v", err)
	}
	if err := admin.SetPolicy(ctx, Policy{Posters: posters, ReadLink: true}); err != nil {
		t.Fatal(err)
	}
	gr.waitEpoch(e + 2)
	fresh, err := admin.Link()
	if err != nil {
		t.Fatal(err)
	}
	if string(fresh) == string(link) {
		t.Fatal("the link did not change")
	}
	if _, err := admin.Send(ctx, []byte("secret of the new link"), MessageID{}); err != nil {
		t.Fatal(err)
	}
	nd2 := newTNode(t, tnodeOpts{server: gr.server, limits: gr.lim})
	rd2, err := nd2.n.JoinLink(ctx, fresh)
	if err != nil {
		t.Fatal(err)
	}
	rec2 := record(t, rd2)
	waitFor(t, 60*time.Second, "the new reader's mesh", func() bool { return rd2.gr.Stats().Mesh >= 1 })
	if _, err := admin.Send(ctx, []byte("live"), MessageID{}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 20*time.Second, "the new reader", func() bool { return rec2.has("live") })
	time.Sleep(time.Second)
	if rec.has("live") || rd.Status().Epoch > e+1 {
		t.Errorf("the old reader reads on: epoch %d", rd.Status().Epoch)
	}
}

// A device that is not a member cannot join with a link that is cut, forged or for a room without a read link.
func TestJoinLinkRefusesABadLink(t *testing.T) {
	gr, _ := newLinkGroup(t, 2)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	link, err := gr.rooms[0].Link()
	if err != nil {
		t.Fatal(err)
	}
	nd := newTNode(t, tnodeOpts{server: gr.server})
	forge := func(f func(*readLink)) []byte {
		l, err := decodeReadLink(link, 1<<20)
		if err != nil {
			t.Fatal(err)
		}
		f(l)
		return l.encode()
	}
	for name, b := range map[string][]byte{
		"nothing":             nil,
		"a cut link":          link[:len(link)/2],
		"a flipped state":     forge(func(l *readLink) { l.State[len(l.State)-1] ^= 1 }),
		"another read secret": forge(func(l *readLink) { l.ReadSecret[0] ^= 1 }),
		"another room":        forge(func(l *readLink) { l.GID[0] ^= 1 }),
		"another epoch":       forge(func(l *readLink) { l.Epoch++ }),
	} {
		if _, err := nd.n.JoinLink(ctx, b); err == nil {
			t.Errorf("%s: joined", name)
		}
	}
	if _, err := nd.n.JoinLink(ctx, link); err != nil {
		t.Fatal(err)
	}
	if _, err := nd.n.JoinLink(ctx, link); err == nil {
		t.Error("a room joined twice")
	}
}
