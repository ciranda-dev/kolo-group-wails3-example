package room

import (
	"context"
	"crypto/ed25519"
	"errors"
	"testing"
	"time"

	koloid "github.com/ciranda-dev/kolo-id"
	kololog "github.com/ciranda-dev/kolo-log"
	kolosecure "github.com/ciranda-dev/kolo-secure"
	kolosync "github.com/ciranda-dev/kolo-sync"

	"github.com/ciranda-dev/kolo-mls/settle"
)

// entryOf is the segment entry of a message that signer sealed, as an admin would store it.
func entryOf(r *Room, key *[keySize]byte, epoch uint64, signer *koloid.Identity, seq uint64, body string) []byte {
	m := forged(r, key, epoch, nil, signer, seq, &content{Time: time.Now().UnixMilli(), Body: []byte(body)})
	return (&stored{Device: [32]byte(signer.PublicKey()), Seq: seq, Body: m.Body}).encode()
}

func catchUpErrors(rec *recorder) []Event {
	var out []Event
	for _, ev := range rec.events() {
		if ev.Err != nil && errors.Is(ev.Err, ErrCatchUp) {
			out = append(out, ev)
		}
	}
	return out
}

// An admin that stores what no member could read or verify cannot make a member skip it or show it: every such entry is
// reported with ErrCatchUp and the cause, and the good entry beside them shows.
func TestHostileSegmentEntriesAreReportedNotSkipped(t *testing.T) {
	gr := newGroup(t, 3, Limits{})
	gr.waitMesh(2)
	admin, reader := gr.rooms[0], gr.rooms[1]
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if _, err := admin.Send(ctx, []byte("opens the segment"), MessageID{}); err != nil {
		t.Fatal(err)
	}
	v := admin.view.Load()
	other := [keySize]byte{7}
	author, outsider := gr.nodes[2].dev, ident(t)
	badSig := func() []byte {
		st, _ := decodeStored(entryOf(admin, &v.key, v.epoch, author, 1001, "bad signature"))
		st.sealed.Sig[0] ^= 1
		st.Body = st.sealed.encode()
		return st.encode()
	}()
	entries := []struct {
		name  string
		entry []byte
		cause error
	}{
		{"a bad signature", badSig, ErrSignature},
		{"another key", entryOf(admin, &other, v.epoch, author, 1002, "other key"), ErrOpen},
		{"an author that is no member", entryOf(admin, &v.key, v.epoch, outsider, 1003, "outsider"), nil},
		{"bytes that are no entry", []byte("junk"), ErrMalformed},
		{"a good message", entryOf(admin, &v.key, v.epoch, author, 1004, "good"), nil},
	}
	for _, e := range entries {
		if _, err := admin.seg.log.Append(e.entry); err != nil {
			t.Fatal(err)
		}
	}
	rec := gr.recs[1]
	waitFor(t, 30*time.Second, "the good message", func() bool { return rec.has("good") })
	waitFor(t, 30*time.Second, "the reports", func() bool { return len(catchUpErrors(rec)) == 4 })
	time.Sleep(200 * time.Millisecond)
	errs := catchUpErrors(rec)
	if len(errs) != 4 {
		t.Fatalf("%d reports, want 4", len(errs))
	}
	for _, e := range entries[:4] {
		if e.cause == nil {
			continue
		}
		found := false
		for _, ev := range errs {
			found = found || errors.Is(ev.Err, e.cause)
		}
		if !found {
			t.Errorf("no report with %v for %s", e.cause, e.name)
		}
	}
	for _, bad := range []string{"bad signature", "other key", "outsider"} {
		if rec.has(bad) {
			t.Errorf("%q shows", bad)
		}
	}
	for _, l := range reader.snapshot() {
		if !l.ctl && !l.tree && l.cursor != l.view.Len() {
			t.Fatalf("a segment is read to %d of %d: an entry was left unread", l.cursor, l.view.Len())
		}
	}
}

// A segment that its admin declares longer than the entries anybody holds is reported once CatchUpWait has passed.
func TestSegmentWithAGapIsReported(t *testing.T) {
	gr := newGroup(t, 3, Limits{CatchUpWait: 300 * time.Millisecond})
	gr.waitMesh(2)
	admin := gr.rooms[0]
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if _, err := admin.Send(ctx, []byte("one"), MessageID{}); err != nil {
		t.Fatal(err)
	}
	admin.segMu.Lock()
	err := admin.appendCtl(&ctlRecord{Kind: ctlClose, ID: admin.seg.id, Size: admin.seg.log.Len() + 5})
	admin.segMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, 30*time.Second, "the report of the gap", func() bool { return len(catchUpErrors(gr.recs[1])) > 0 })
	if err := catchUpErrors(gr.recs[1])[0].Err; !errors.Is(err, ErrCatchUp) {
		t.Fatal(err)
	}
}

// A SettledCommit that one admin stores for an epoch is not the only one: another admin's hostile value for the next epoch is
// reported once and does not stop the room from applying the real one the first admin settles.
func TestHostileCommitInTheControlLogIsReportedAndBypassed(t *testing.T) {
	gr := newGroup(t, 3, Limits{})
	gr.waitMesh(2)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if err := gr.rooms[0].Promote(ctx, gr.nodes[1].key(), Admin); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 30*time.Second, "the second admin", func() bool { return gr.rooms[1].isAdmin() && gr.rooms[1].Status().Epoch == gr.rooms[0].Status().Epoch })
	next := gr.rooms[0].Status().Epoch
	if err := gr.rooms[1].appendCtl(&ctlRecord{Kind: ctlPut, Key: settledKey(gr.rooms[1], next), Value: []byte("not a SettledCommit")}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 30*time.Second, "the report", func() bool { return len(catchUpErrors(gr.recs[2])) > 0 })
	nd := gr.newNode()
	kp, _ := nd.n.KeyPackage()
	if _, err := gr.rooms[0].Add(ctx, kp); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 30*time.Second, "the members at the new epoch", func() bool { return gr.rooms[2].Status().Epoch == next+1 && gr.rooms[1].Status().Epoch == next+1 })
	if n := len(catchUpErrors(gr.recs[2])); n != 1 {
		t.Fatalf("%d reports, want one", n)
	}
}

// An admin that serves a second history of a segment it signed is detected by a member that holds the first, which keeps
// what it verified and reports the fork.
func TestRewrittenSegmentHistoryIsRefused(t *testing.T) {
	gr := newGroup(t, 3, Limits{})
	reader := gr.rooms[1]
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	adminDev := gr.nodes[0].dev
	opt := kololog.WithDerivedKeys(reader.logSec, 0)
	base, err := kololog.New(adminDev, kololog.NewMemory(), opt)
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range []string{"a", "b"} {
		if _, err := base.Append([]byte(b)); err != nil {
			t.Fatal(err)
		}
	}
	header := base.Header().Encode()
	head, err := base.Head()
	if err != nil {
		t.Fatal(err)
	}
	var es []kololog.Entry
	for i := range base.Len() {
		e, err := base.Entry(i)
		if err != nil {
			t.Fatal(err)
		}
		es = append(es, e)
	}
	base.Close()
	// A history is served as a replica: only one writable log of an owner and id lives in a process.
	grow := func(tags ...string) *kololog.Replica {
		mem := kololog.NewMemory()
		if err := mem.Commit(kololog.Change{Entries: es}); err != nil {
			t.Fatal(err)
		}
		l, err := kololog.Restore(adminDev, header, head.Encode(), mem, opt)
		if err != nil {
			t.Fatal(err)
		}
		for _, b := range tags {
			if _, err := l.Append([]byte(b)); err != nil {
				t.Fatal(err)
			}
		}
		h, err := l.Head()
		if err != nil {
			t.Fatal(err)
		}
		var all []kololog.Entry
		for i := range l.Len() {
			e, err := l.Entry(i)
			if err != nil {
				t.Fatal(err)
			}
			all = append(all, e)
		}
		l.Close()
		out := kololog.NewMemory()
		if err := out.Commit(kololog.Change{Entries: all}); err != nil {
			t.Fatal(err)
		}
		rep, err := kololog.RestoreReplica(adminDev.PublicKey(), l.ID(), header, kololog.Heads{Head: &h}, out)
		if err != nil {
			t.Fatal(err)
		}
		return rep
	}
	serve := func(l *kololog.Replica) *kolosecure.Conn {
		srv := kolosync.NewServer(func(_, _ ed25519.PublicKey, _ kololog.LogID) kolosync.Access { return kolosync.Ciphertext })
		if err := srv.Offer(l); err != nil {
			t.Fatal(err)
		}
		ln, err := kolosecure.Listen(ctx, "127.0.0.1:0", adminDev, nil)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { ln.Close() })
		go func() {
			if c, err := ln.Accept(ctx); err == nil {
				srv.Serve(ctx, c)
			}
		}()
		c, err := kolosecure.Dial(ctx, ln.Addr().String(), gr.nodes[1].dev, adminDev.PublicKey(), nil)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { c.Close() })
		return c
	}
	rep, err := reader.replicate([32]byte(adminDev.PublicKey()), header, false, reader.view.Load().epoch)
	if err != nil {
		t.Fatal(err)
	}
	if err := kolosync.PullCiphertext(ctx, serve(grow("c", "d")), rep.rep); err != nil || rep.rep.Len() != 4 {
		t.Fatalf("the first history: %v, %d entries", err, rep.rep.Len())
	}
	go reader.fol.Follow(ctx, serve(grow("x", "y", "z")))
	waitFor(t, 30*time.Second, "the report of the fork", func() bool {
		for _, ev := range catchUpErrors(gr.recs[1]) {
			if errors.Is(ev.Err, kolosync.ErrDiverged) {
				return true
			}
		}
		return false
	})
	if rep.rep.Len() != 4 {
		t.Fatalf("the reader holds %d entries after the fork, want its verified 4", rep.rep.Len())
	}
}

func settledKey(r *Room, epoch uint64) string { return settle.SettledKey(r.gid[:], epoch) }
