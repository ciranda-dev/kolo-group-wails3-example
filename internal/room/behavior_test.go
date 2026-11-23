package room

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/ciranda-dev/kolo-pin"

	"github.com/ciranda-dev/kolo-mls/settle"
)

// ctlRecords returns the records of the control log of room r that this device holds for admin owner.
func ctlRecords(t *testing.T, r *Room, own bool) []*ctlRecord {
	t.Helper()
	var out []*ctlRecord
	for _, l := range r.snapshot() {
		if !l.ctl || l.own != own {
			continue
		}
		for i := uint64(0); i < l.view.Len(); i++ {
			b, err := l.view.Get(i)
			if err != nil {
				t.Fatal(err)
			}
			rec, err := decodeCtl(b, 1<<20)
			if err != nil {
				t.Fatal(err)
			}
			out = append(out, rec)
		}
	}
	return out
}

func segments(r *Room) (out []*roomLog) {
	for _, l := range r.snapshot() {
		if !l.ctl && !l.tree {
			out = append(out, l)
		}
	}
	slices.SortFunc(out, func(a, b *roomLog) int { return int(a.epoch) - int(b.epoch) })
	return out
}

// A segment closes at its entry bound and when the epoch ends, and its closing record in the control log says how many
// entries it holds, so a reader knows whether it has all of them.
func TestSegmentsRollAtTheBoundAndAtTheEpoch(t *testing.T) {
	gr := newGroup(t, 3, Limits{SegmentEntries: 5})
	gr.waitMesh(2)
	admin := gr.rooms[0]
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	first := admin.Status().Epoch
	for i := range 12 {
		if _, err := admin.Send(ctx, []byte(fmt.Sprintf("m%d", i)), MessageID{}); err != nil {
			t.Fatal(err)
		}
	}
	nd := gr.newNode()
	kp, _ := nd.n.KeyPackage()
	if _, err := admin.Add(ctx, kp); err != nil {
		t.Fatal(err)
	}
	// The segment of the epoch that ended is closed when the epoch ends, not when the next message comes.
	waitFor(t, 30*time.Second, "the segment to close with its epoch", func() bool {
		n := 0
		for _, rec := range ctlRecords(t, admin, true) {
			if rec.Kind == ctlClose {
				n++
			}
		}
		return n == 3
	})
	if _, err := admin.Send(ctx, []byte("after"), MessageID{}); err != nil {
		t.Fatal(err)
	}
	lens := map[uint64][]uint64{}
	for _, l := range segments(admin) {
		lens[l.epoch] = append(lens[l.epoch], l.log.Len())
	}
	for _, ls := range lens {
		slices.SortFunc(ls, func(a, b uint64) int { return int(b) - int(a) })
	}
	if len(lens) != 2 || !slices.Equal(lens[first], []uint64{5, 5, 2}) || !slices.Equal(lens[first+1], []uint64{1}) {
		t.Fatalf("segments of these entries in each epoch: %v", lens)
	}
	var opens, closes []uint64
	for _, rec := range ctlRecords(t, admin, true) {
		switch rec.Kind {
		case ctlOpen:
			opens = append(opens, rec.Epoch)
		case ctlClose:
			closes = append(closes, rec.Size)
		}
	}
	if !slices.Equal(opens, []uint64{first, first, first, first + 1}) || !slices.Equal(closes, []uint64{5, 5, 2}) {
		t.Fatalf("opened in epochs %v, closed with sizes %v", opens, closes)
	}
	// A member reads all of them from the admin and knows each closed segment is complete.
	waitFor(t, 30*time.Second, "a member to read the segments", func() bool {
		segs := segments(gr.rooms[1])
		if len(segs) != 4 {
			return false
		}
		for _, l := range segs[:3] {
			gr.rooms[1].logMu.Lock()
			ok := l.complete() && l.cursor == l.size
			gr.rooms[1].logMu.Unlock()
			if !ok {
				return false
			}
		}
		return true
	})
}

// A device keeps the closed segments it has read only up to KeepSegments, newest first, and deletes the others from its store.
func TestKeepSegmentsForgetsTheOldestReadOnes(t *testing.T) {
	gr := newGroup(t, 2, Limits{SegmentEntries: 2, KeepSegments: 2})
	gr.waitMesh(1)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	for i := range 12 {
		if _, err := gr.rooms[0].Send(ctx, []byte(fmt.Sprintf("m%d", i)), MessageID{}); err != nil {
			t.Fatal(err)
		}
	}
	member := gr.rooms[1]
	waitFor(t, 30*time.Second, "the member to read and trim", func() bool {
		return gr.recs[1].has("m11") && len(segments(member)) <= 3
	})
	// The control log, at most KeepSegments closed segments, the one being written and the tree log of the epoch.
	if n := member.Status().Logs; n > 5 {
		t.Fatalf("%d logs held", n)
	}
	// An admin forgets its own closed segments the same way, and keeps them in its store; it also keeps the tree log of the
	// member it added.
	waitFor(t, 30*time.Second, "the admin to trim", func() bool { return gr.rooms[0].Status().Logs <= 5 })
	if !bodiesIn2(gr.recs[1], "m", 12) {
		t.Fatal("a message was lost to the trim")
	}
}

func bodiesIn2(rec *recorder, prefix string, n int) bool {
	for i := range n {
		if !rec.has(fmt.Sprintf("%s%d", prefix, i)) {
			return false
		}
	}
	return true
}

// A room follows at most MaxLogs logs; a log past the bound is reported and not opened.
func TestMaxLogsBoundsTheLogsAMemberFollows(t *testing.T) {
	gr := newGroup(t, 2, Limits{SegmentEntries: 1, MaxLogs: 3, KeepSegments: 100})
	gr.waitMesh(1)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	for i := range 6 {
		if _, err := gr.rooms[0].Send(ctx, []byte(fmt.Sprintf("m%d", i)), MessageID{}); err != nil {
			t.Fatal(err)
		}
	}
	waitFor(t, 30*time.Second, "the report", func() bool {
		for _, ev := range catchUpErrors(gr.recs[1]) {
			if errors.Is(ev.Err, ErrTooManyLogs) {
				return true
			}
		}
		return false
	})
	if n := gr.rooms[1].Status().Logs; n > 3 {
		t.Fatalf("%d logs followed, the bound is 3", n)
	}
}

// A message sent while no admin is online waits as a receipt, survives a restart of its author, is published again, and
// is stored once an admin returns, once and only once; Send refuses past MaxReceipts.
func TestMessageSentWhileTheAdminsAreAwayIsStoredLater(t *testing.T) {
	gr := newGroup(t, 3, Limits{RepublishAfter: 300 * time.Millisecond, MaxReceipts: 5, Rate: 100, Burst: 100})
	gr.waitMesh(2)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	gr.stop(0)
	var ids []MessageID
	for i := range 5 {
		id, err := gr.rooms[1].Send(ctx, []byte(fmt.Sprintf("late-%d", i)), MessageID{})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	if _, err := gr.rooms[1].Send(ctx, []byte("one too many"), MessageID{}); !errors.Is(err, ErrTooManyReceipts) {
		t.Fatalf("Send past MaxReceipts: %v", err)
	}
	if got := gr.rooms[1].Status().Receipts; got != 5 {
		t.Fatalf("%d receipts, want 5", got)
	}
	gr.restart(1)
	if got := gr.rooms[1].Status().Receipts; got != 5 {
		t.Fatalf("%d receipts after a restart, want 5", got)
	}
	gr.restart(0)
	gr.waitPeers(1)
	waitFor(t, 60*time.Second, "every message to be stored", func() bool { return gr.rooms[1].Status().Receipts == 0 })
	for _, id := range ids {
		if !hasStored(gr.recs[1], id) {
			t.Errorf("no Stored event for %x", id[:4])
		}
	}
	// Republishing never stores a message twice: each of the five is in the segment once.
	if got := len(storedIDs(gr.rooms[0])); got != 5 {
		t.Fatalf("the admin holds %d distinct messages", got)
	}
	if got := segLen(gr.rooms[0]); got != 5 {
		t.Fatalf("the admin's segment holds %d entries, want 5", got)
	}
	// The members that were online read the messages once too.
	waitFor(t, 30*time.Second, "the other member to read them", func() bool { return bodiesIn(gr.recs[2], "late", 5) })
	if n := countBodies(gr.recs[2], "late-0"); n != 1 {
		t.Fatalf("a message shows %d times", n)
	}
}

func countBodies(rec *recorder, body string) int {
	n := 0
	for _, ev := range rec.messages() {
		if string(ev.Body) == body {
			n++
		}
	}
	return n
}

// The adapter keeps Commits and SettledCommits in the control log for every member to read, keeps trees and marks on this
// device, lists in pages in order and bounds what it returns.
func TestPinsAdapterOverTheControlLog(t *testing.T) {
	gr := newGroup(t, 2, Limits{})
	r := gr.rooms[0]
	ctx := context.Background()
	p := roomPins{r}
	gid := r.gid[:]
	commit := []byte("a commit")
	h, err := settle.PutObject(ctx, p, commit)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := settle.GetObject(ctx, p, h, 0); err != nil || string(got) != "a commit" {
		t.Fatalf("GetObject: %q %v", got, err)
	}
	if _, err := p.Get(ctx, settle.ObjectKey(h), 3); !errors.Is(err, settle.ErrPinTooLarge) {
		t.Fatalf("a value past the bound: %v", err)
	}
	if _, err := p.Get(ctx, settle.ObjectKey([32]byte{1}), 0); !errors.Is(err, settle.ErrMissing) {
		t.Fatalf("a missing key: %v", err)
	}
	n := len(ctlRecords(t, r, true))
	if _, err := settle.PutObject(ctx, p, commit); err != nil || len(ctlRecords(t, r, true)) != n {
		t.Fatalf("the same Commit written twice adds a record: %v", err)
	}
	if err := p.Put(ctx, settle.TreeKey(gid, 9), []byte("tree")); err != nil {
		t.Fatal(err)
	}
	if len(ctlRecords(t, r, true)) != n {
		t.Fatal("a tree went to the control log")
	}
	if got, err := p.Get(ctx, settle.TreeKey(gid, 9), 0); err != nil || string(got) != "tree" {
		t.Fatalf("a tree: %q %v", got, err)
	}
	if err := p.Delete(ctx, settle.TreeKey(gid, 9)); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Get(ctx, settle.TreeKey(gid, 9), 0); !errors.Is(err, settle.ErrMissing) {
		t.Fatalf("a deleted tree: %v", err)
	}
	if err := p.Delete(ctx, settle.TreeKey(gid, 9)); err != nil {
		t.Fatalf("a key that is not there: %v", err)
	}
	for i := range 5 {
		if err := p.Put(ctx, settle.SettledKey(gid, uint64(100+i)), []byte{byte(i)}); err != nil {
			t.Fatal(err)
		}
	}
	prefix := "kolo-mls/settled/"
	var all []string
	for after := ""; ; {
		page, err := p.List(ctx, prefix, after, 2)
		if err != nil {
			t.Fatal(err)
		}
		if len(page) == 0 {
			break
		}
		if len(page) > 2 {
			t.Fatalf("a page of %d keys", len(page))
		}
		all, after = append(all, page...), page[len(page)-1]
	}
	if !slices.IsSorted(all) || len(all) < 5 {
		t.Fatalf("paging gave %v", all)
	}
	if page, _ := p.List(ctx, settle.ArchivePrefix(gid, 2), "", 0); len(page) != 0 {
		t.Fatalf("the archive of an epoch lists %v: messages are not MLS messages", page)
	}
	// A member that is no admin cannot write a Commit.
	if err := (roomPins{gr.rooms[1]}).Put(ctx, settle.ObjectKey(sha256.Sum256([]byte("x"))), []byte("x")); !errors.Is(err, ErrNotAdmin) {
		t.Fatalf("Put by a member that is no admin: %v", err)
	}
}

// The admin pushes each log it writes to the pins, a closed segment last with its final size.
func TestAdminPushesItsLogsToThePins(t *testing.T) {
	ps := []*pinServer{newPin(t), newPin(t)}
	peers := []pin.Peer{ps[0].peer, ps[1].peer}
	gr := newGroupPins(t, 2, Limits{PinGap: 50 * time.Millisecond, SegmentEntries: 3}, peers, nil)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	admin := gr.rooms[0]
	for i := range 7 {
		if _, err := admin.Send(ctx, []byte(fmt.Sprintf("m%d", i)), MessageID{}); err != nil {
			t.Fatal(err)
		}
	}
	waitFor(t, 30*time.Second, "the pushes", func() bool { return !admin.dirtyAny() && admin.Status().PinError == nil })
	for _, l := range admin.snapshot() {
		pinsHold(t, peers, gr.nodes[0].dev, l.id, l.log.Len())
	}
}

// A message that reaches an admin twice, in two envelopes, is stored once: the republished copy of a message has the same
// sealed body and so the same id.
func TestAMessageReachingAnAdminTwiceIsStoredOnce(t *testing.T) {
	gr := newGroup(t, 3, Limits{})
	admin := gr.rooms[0]
	v := admin.view.Load()
	m := forged(admin, &v.key, v.epoch, gr.nodes[2].person.PublicKey(), gr.nodes[2].dev, 5, &content{Time: time.Now().UnixMilli(), Body: []byte("twice")})
	again := *m
	again.ID, again.Time = MessageID{9}, m.Time.Add(time.Second)
	admin.receive(m)
	admin.receive(&again)
	waitFor(t, 10*time.Second, "the message", func() bool { return gr.recs[0].has("twice") })
	if got := segLen(admin); got != 1 {
		t.Fatalf("the segment holds %d entries, want 1", got)
	}
	if n := countBodies(gr.recs[0], "twice"); n != 1 {
		t.Fatalf("the message shows %d times", n)
	}
}
