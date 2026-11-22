package room

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	kologossip "github.com/ciranda-dev/kolo-gossip"
	koloid "github.com/ciranda-dev/kolo-id"
	"github.com/ciranda-dev/kolo-pin"
	kolostore "github.com/ciranda-dev/kolo-store"

	"github.com/ciranda-dev/kolo-mls/backendstore"
)

// ghostKeyPackage is a key package of a device that never connects, so an Add of it ends an epoch.
func ghostKeyPackage(t testing.TB, gr *group) []byte {
	t.Helper()
	person, dev := ident(t), ident(t)
	offer, err := person.OfferDeviceCert(dev.PublicKey(), &koloid.IssueOptions{Serial: 1})
	if err != nil {
		t.Fatal(err)
	}
	cert, err := dev.AcceptDeviceCert(offer, person.PublicKey())
	if err != nil {
		t.Fatal(err)
	}
	n, err := Open(context.Background(), &Config{Device: dev, Cert: cert, Gossip: gr.nodes[0].g, MLSStore: backendstore.New(kolostore.NewMemory(), []byte("mls/")),
		Backend: kolostore.NewMemory(), Store: kolostore.New(kolostore.NewMemory())})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { n.Close() })
	kp, err := n.KeyPackage()
	if err != nil {
		t.Fatal(err)
	}
	return kp
}

// advance makes k epochs: in each the first member sends one message and an Add of a ghost device ends the epoch. It
// returns the epoch of each body.
func (gr *group) advance(t testing.TB, ctx context.Context, k int, tag string) map[string]uint64 {
	t.Helper()
	sent := map[string]uint64{}
	for range k {
		e := gr.rooms[0].Status().Epoch
		body := fmt.Sprintf("%s-%d", tag, e)
		if _, err := gr.rooms[0].Send(ctx, []byte(body), MessageID{}); err != nil {
			t.Fatal(err)
		}
		sent[body] = e
		if _, err := gr.rooms[0].Add(ctx, ghostKeyPackage(t, gr)); err != nil {
			t.Fatal(err)
		}
		gr.waitEpoch(e + 1)
	}
	return sent
}

// readHistory reads every page of History, newest page first, and returns the events oldest first.
func readHistory(t testing.TB, ctx context.Context, r *Room, page int) []Event {
	t.Helper()
	var pages [][]Event
	var before MessageID
	for {
		evs, err := r.History(ctx, before, page)
		if err != nil {
			t.Fatal(err)
		}
		if len(evs) == 0 {
			break
		}
		if len(evs) > page {
			t.Fatalf("a page of %d events for %d", len(evs), page)
		}
		pages = append(pages, evs)
		before = evs[0].ID
	}
	var all []Event
	for i := len(pages) - 1; i >= 0; i-- {
		all = append(all, pages[i]...)
	}
	return all
}

// linkEpochs returns the history links in the control log of an admin, by epoch.
func linkEpochs(t testing.TB, r *Room) map[uint64][]byte {
	t.Helper()
	out := map[uint64][]byte{}
	l := r.ctl
	for i := range l.log.Len() {
		b, err := l.log.Get(i)
		if err != nil {
			t.Fatal(err)
		}
		rec, err := decodeCtl(b, r.n.limits.MaxMessageSize)
		if err != nil {
			t.Fatal(err)
		}
		if rec.Kind == ctlLink {
			out[rec.Epoch] = rec.Value
		}
	}
	return out
}

// bodiesOf returns the bodies of the events and fails on an event with an error.
func bodiesOf(t testing.TB, evs []Event) map[string]uint64 {
	t.Helper()
	out := map[string]uint64{}
	for _, ev := range evs {
		if ev.Err != nil {
			t.Fatalf("an event with an error: %+v", ev)
		}
		out[string(ev.Body)] = ev.Epoch
	}
	return out
}

func newHistoryGroup(t testing.TB, n int, p Policy) *group {
	t.Helper()
	gr := newGroupWith(t, n, Limits{KeepSegments: 200, CatchUpWait: 10 * time.Second}, func(o *tnodeOpts) { o.policy = p })
	gr.waitMesh(n - 1)
	return gr
}

// A device that joins at epoch 40 reads the messages of epochs 1 to 39, from the segments of the admin, through the chain of links
// and nothing else: the live stream shows none of them.
func TestJoinerAtEpoch40ReadsEveryEpoch(t *testing.T) {
	gr := newHistoryGroup(t, 2, Policy{})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	sent := gr.advance(t, ctx, 38, "m")
	last := fmt.Sprintf("m-%d", gr.rooms[0].Status().Epoch)
	if _, err := gr.rooms[0].Send(ctx, []byte(last), MessageID{}); err != nil {
		t.Fatal(err)
	}
	sent[last] = gr.rooms[0].Status().Epoch
	gr.join(ctx, gr.newNode())
	r, rec := gr.rooms[len(gr.rooms)-1], gr.recs[len(gr.recs)-1]
	if r.Status().Epoch != 40 || r.joinEpoch != 40 {
		t.Fatalf("joined at epoch %d", r.joinEpoch)
	}
	start := time.Now()
	all := readHistory(t, ctx, r, 7)
	t.Logf("40 epochs, 39 messages read in %v", time.Since(start).Round(time.Millisecond))
	got := bodiesOf(t, all)
	if len(got) != len(sent) || len(all) != len(sent) {
		t.Fatalf("read %d messages, want %d", len(all), len(sent))
	}
	for body, epoch := range sent {
		if got[body] != epoch {
			t.Errorf("%s: epoch %d, want %d", body, got[body], epoch)
		}
	}
	for i := 1; i < len(all); i++ {
		if all[i].Epoch < all[i-1].Epoch {
			t.Fatalf("epoch %d after epoch %d", all[i].Epoch, all[i-1].Epoch)
		}
	}
	if r.lo != 0 {
		t.Fatalf("the oldest key is epoch %d", r.lo)
	}
	time.Sleep(300 * time.Millisecond)
	for body := range sent {
		if rec.has(body) {
			t.Errorf("%s shows in the live stream", body)
		}
	}
	// Asking again from the start gives the same history.
	if again := readHistory(t, ctx, r, 100); len(again) != len(all) {
		t.Fatalf("a second read gives %d messages, want %d", len(again), len(all))
	}
}

// Under SinceEpoch the admin writes no link for an epoch up to P, so a joiner holds no key before P and reads from P on.
func TestSinceEpochReadsOnlyFromThePoint(t *testing.T) {
	const since = 5
	gr := newHistoryGroup(t, 2, Policy{History: SinceEpoch, Since: since})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	sent := gr.advance(t, ctx, 10, "s")
	gr.join(ctx, gr.newNode())
	r := gr.rooms[len(gr.rooms)-1]
	if p := r.view.Load().hist; p != (histPolicy{Rule: SinceEpoch, Since: since}) {
		t.Fatalf("the joiner reads the policy %+v", p)
	}
	got := bodiesOf(t, readHistory(t, ctx, r, 4))
	for body, epoch := range sent {
		_, read := got[body]
		if read != (epoch >= since) {
			t.Errorf("%s of epoch %d: read %v", body, epoch, read)
		}
	}
	if len(got) != 6 {
		t.Fatalf("read %d messages, want 6 (epochs 5 to 10)", len(got))
	}
	if r.lo != since {
		t.Fatalf("the oldest key is epoch %d, want %d", r.lo, since)
	}
	for e := range linkEpochs(t, gr.rooms[0]) {
		if e <= since {
			t.Errorf("a link for epoch %d was written", e)
		}
	}
	if err := r.deriveBack(ctx, since-1); !errors.Is(err, ErrHistory) {
		t.Fatalf("the key before the point: %v", err)
	}
}

// Under NoHistory no link is written and a joiner reads nothing before its epoch.
func TestNoHistoryWritesNoLinkAndReadsNothing(t *testing.T) {
	gr := newHistoryGroup(t, 2, Policy{History: NoHistory})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	gr.advance(t, ctx, 4, "n")
	gr.join(ctx, gr.newNode())
	r := gr.rooms[len(gr.rooms)-1]
	if links := linkEpochs(t, gr.rooms[0]); len(links) != 0 {
		t.Fatalf("%d links written", len(links))
	}
	if evs, err := r.History(ctx, MessageID{}, 10); err != nil || len(evs) != 0 {
		t.Fatalf("History: %d events, %v", len(evs), err)
	}
	if r.lo != r.joinEpoch {
		t.Fatalf("the oldest key is epoch %d", r.lo)
	}
}

// A member removed in epoch 20 holds no key of epoch 20 or later, reads none of the messages after it, and cannot open the
// links, which are sealed under the keys it lacks.
func TestMemberRemovedAtEpoch20ReadsNothingAfter(t *testing.T) {
	gr := newHistoryGroup(t, 3, Policy{})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	removed, rec := gr.rooms[2], gr.recs[2]
	gr.advance(t, ctx, 17, "b")
	if e := gr.rooms[0].Status().Epoch; e != 19 {
		t.Fatalf("epoch %d before the removal", e)
	}
	if err := gr.rooms[0].Remove(ctx, gr.nodes[2].person.PublicKey()); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 60*time.Second, "the others at epoch 20", func() bool { return gr.rooms[0].Status().Epoch == 20 && gr.rooms[1].Status().Epoch == 20 })
	gr.away = map[int]bool{2: true}
	after := gr.advance(t, ctx, 5, "a")
	time.Sleep(300 * time.Millisecond)
	removed.kmu.RLock()
	var newest uint64
	for e := range removed.keys {
		newest = max(newest, e)
	}
	key := removed.keys[19]
	removed.kmu.RUnlock()
	if newest != 19 {
		t.Fatalf("the removed device holds the key of epoch %d", newest)
	}
	for body := range after {
		if rec.has(body) {
			t.Errorf("%s shows at the removed device", body)
		}
	}
	for _, ev := range rec.messages() {
		if ev.Epoch >= 20 {
			t.Errorf("a message of epoch %d shows at the removed device", ev.Epoch)
		}
	}
	links := linkEpochs(t, gr.rooms[0])
	for e := uint64(20); e <= 25; e++ {
		if links[e] == nil {
			t.Fatalf("no link of epoch %d", e)
		}
		if _, err := openLink(removed.gid[:], e, &key, links[e]); !errors.Is(err, ErrOpen) {
			t.Errorf("the removed device opened the link of epoch %d: %v", e, err)
		}
	}
}

// A link written after a segment of its epoch or a later one, or for an epoch the policy leaves out, is refused and reported.
func TestLateAndOutOfPolicyLinksAreRefused(t *testing.T) {
	gr := newHistoryGroup(t, 2, Policy{History: SinceEpoch, Since: 1})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	gr.advance(t, ctx, 8, "l")
	gr.join(ctx, gr.newNode())
	r, rec := gr.rooms[len(gr.rooms)-1], gr.recs[len(gr.recs)-1]
	if r.joinEpoch != 10 {
		t.Fatalf("joined at epoch %d", r.joinEpoch)
	}
	owner := [32]byte{9}
	good := make([]byte, linkSize)
	reports := func() int {
		n := 0
		for _, ev := range rec.events() {
			if ev.Err != nil && errors.Is(ev.Err, ErrLink) {
				n++
			}
		}
		return n
	}
	kept := func(e uint64) bool {
		r.hmu.Lock()
		defer r.hmu.Unlock()
		return slices.ContainsFunc(r.links[e], func(x linkRec) bool { return x.owner == owner })
	}
	for _, c := range []struct {
		name    string
		l       *roomLog
		epoch   uint64
		kept    bool
		reports int
	}{
		{"a timely link", &roomLog{owner: owner, ctl: true}, 5, true, 0},
		{"after a segment of an older epoch", &roomLog{owner: owner, ctl: true, opened: true, openedTo: 4}, 6, true, 0},
		{"after a segment of its epoch", &roomLog{owner: owner, ctl: true, opened: true, openedTo: 7}, 7, false, 1},
		{"after a segment of a later epoch", &roomLog{owner: owner, ctl: true, opened: true, openedTo: 9}, 8, false, 2},
		{"for an epoch the policy leaves out", &roomLog{owner: owner, ctl: true}, 1, false, 3},
		{"for an epoch past the join", &roomLog{owner: owner, ctl: true}, 11, false, 3},
	} {
		r.addLink(c.l, &ctlRecord{Kind: ctlLink, Epoch: c.epoch, Value: good})
		waitFor(t, 5*time.Second, c.name, func() bool { return reports() == c.reports })
		if kept(c.epoch) != c.kept {
			t.Errorf("%s: kept %v", c.name, kept(c.epoch))
		}
	}
}

// A late link that an admin really writes reaches a member as a report and not as a key.
func TestAdminLinkWrittenAfterItsSegmentIsReported(t *testing.T) {
	gr := newHistoryGroup(t, 3, Policy{})
	admin, rec := gr.rooms[0], gr.recs[2]
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if _, err := admin.Send(ctx, []byte("opens the segment"), MessageID{}); err != nil {
		t.Fatal(err)
	}
	e := admin.Status().Epoch
	if err := admin.appendCtl(&ctlRecord{Kind: ctlLink, Epoch: e, Value: make([]byte, linkSize)}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 30*time.Second, "the report", func() bool {
		for _, ev := range rec.events() {
			if ev.Err != nil && errors.Is(ev.Err, ErrLink) && ev.Epoch == e {
				return true
			}
		}
		return false
	})
}

// A link that does not open is refused and reported; another admin's good link for the same epoch still opens.
func TestLinkThatDoesNotOpenIsRefused(t *testing.T) {
	gr := newHistoryGroup(t, 2, Policy{})
	r := gr.rooms[1]
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	lo := r.lo
	honest := linkEpochs(t, gr.rooms[0])[lo]
	if honest == nil {
		t.Fatalf("no link of epoch %d", lo)
	}
	junk := make([]byte, linkSize)
	r.hmu.Lock()
	r.links = map[uint64][]linkRec{lo: {{owner: [32]byte{1}, link: [linkSize]byte(junk)}}}
	r.hmu.Unlock()
	err := r.deriveBack(ctx, lo-1)
	if !errors.Is(err, ErrHistory) || !errors.Is(err, ErrOpen) {
		t.Fatalf("a junk link: %v", err)
	}
	r.kmu.RLock()
	_, held := r.keys[lo-1]
	r.kmu.RUnlock()
	if held || r.lo != lo {
		t.Fatal("a key came out of a junk link")
	}
	r.hmu.Lock()
	r.links[lo] = append(r.links[lo], linkRec{owner: [32]byte{2}, link: [linkSize]byte(honest)})
	r.hmu.Unlock()
	if err := r.deriveBack(ctx, lo-1); err != nil {
		t.Fatalf("a good link beside a junk one: %v", err)
	}
	if r.lo != lo-1 {
		t.Fatalf("the oldest key is epoch %d", r.lo)
	}
}

// A missing link ends the chain with ErrHistory after CatchUpWait and History reports it.
func TestMissingLinkIsReported(t *testing.T) {
	gr := newGroupWith(t, 2, Limits{CatchUpWait: 300 * time.Millisecond}, nil)
	gr.waitMesh(1)
	gr.join(context.Background(), gr.newNode())
	r := gr.rooms[len(gr.rooms)-1]
	waitFor(t, 30*time.Second, "the control logs", func() bool {
		r.hmu.Lock()
		defer r.hmu.Unlock()
		return len(r.links[r.joinEpoch]) > 0 && r.controlRead()
	})
	r.hmu.Lock()
	r.links = map[uint64][]linkRec{}
	r.hmu.Unlock()
	if _, err := r.History(context.Background(), MessageID{}, 3); !errors.Is(err, ErrHistory) {
		t.Fatalf("History with no link: %v", err)
	}
}

// What an admin stores that no reader can open is reported in History with its cause and never left out, and the good
// message beside it is read.
func TestUndecryptableEntriesOfAPastSegmentAreReported(t *testing.T) {
	gr := newHistoryGroup(t, 2, Policy{})
	admin := gr.rooms[0]
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	sent := gr.advance(t, ctx, 3, "u")
	var seg *roomLog
	for _, l := range admin.snapshot() {
		if !l.ctl && l.epoch == 2 {
			seg = l
		}
	}
	if seg == nil {
		t.Fatal("no segment of epoch 2")
	}
	admin.kmu.RLock()
	key := admin.keys[2]
	admin.kmu.RUnlock()
	other, author := [keySize]byte{7}, gr.nodes[1].dev
	badSig, _ := decodeStored(entryOf(admin, &key, 2, author, 2001, "bad signature"))
	badSig.sealed.Sig[0] ^= 1
	badSig.Body = badSig.sealed.encode()
	for _, e := range [][]byte{badSig.encode(), entryOf(admin, &other, 2, author, 2002, "other key"), []byte("junk"), entryOf(admin, &key, 2, author, 2003, "good")} {
		if _, err := seg.log.Append(e); err != nil {
			t.Fatal(err)
		}
	}
	gr.join(ctx, gr.newNode())
	all := readHistory(t, ctx, gr.rooms[len(gr.rooms)-1], 50)
	causes := map[error]bool{}
	reads := 0
	for _, ev := range all {
		if ev.Err == nil {
			reads++
			continue
		}
		if !errors.Is(ev.Err, ErrCatchUp) {
			t.Errorf("an error without ErrCatchUp: %v", ev.Err)
		}
		for _, c := range []error{ErrSignature, ErrOpen, ErrMalformed} {
			causes[c] = causes[c] || errors.Is(ev.Err, c)
		}
	}
	if len(causes) != 3 || !causes[ErrSignature] || !causes[ErrOpen] || !causes[ErrMalformed] {
		t.Fatalf("causes reported: %v", causes)
	}
	if reads != len(sent)+1 {
		t.Fatalf("%d messages read, want %d", reads, len(sent)+1)
	}
}

// A joiner that restarts keeps the keys it derived and reads the history again.
func TestHistorySurvivesARestart(t *testing.T) {
	gr := newHistoryGroup(t, 2, Policy{})
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()
	sent := gr.advance(t, ctx, 6, "r")
	nd := newTNode(t, tnodeOpts{server: gr.server, limits: gr.lim, dir: t.TempDir()})
	gr.join(ctx, nd)
	i := len(gr.rooms) - 1
	if got := bodiesOf(t, readHistory(t, ctx, gr.rooms[i], 100)); len(got) != len(sent) {
		t.Fatalf("read %d messages, want %d", len(got), len(sent))
	}
	r := gr.restart(i)
	if r.lo != 0 || r.joinEpoch != gr.rooms[0].Status().Epoch {
		t.Fatalf("after the restart: the oldest key is epoch %d, joined at %d", r.lo, r.joinEpoch)
	}
	// The peers still hold the connection of the stopped device until it times out, so the new one can take a while.
	waitFor(t, 3*time.Minute, "the restarted device to reach a peer", func() bool { return r.gr.Stats().Peers >= 1 })
	if got := bodiesOf(t, readHistory(t, ctx, r, 100)); len(got) != len(sent) {
		t.Fatalf("after the restart: read %d messages, want %d", len(got), len(sent))
	}
}

// Pages come newest first, each oldest first inside; a before that is not the last page's oldest message is refused.
func TestHistoryPagesAndRefusals(t *testing.T) {
	gr := newHistoryGroup(t, 2, Policy{})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	sent := gr.advance(t, ctx, 8, "p")
	gr.join(ctx, gr.newNode())
	r := gr.rooms[len(gr.rooms)-1]
	if _, err := r.History(ctx, MessageID{}, 0); !errors.Is(err, ErrInvalid) {
		t.Errorf("n = 0: %v", err)
	}
	first, err := r.History(ctx, MessageID{}, 3)
	if err != nil || len(first) != 3 {
		t.Fatalf("the first page: %d events, %v", len(first), err)
	}
	if first[2].Epoch != 8 || first[0].Epoch != 6 {
		t.Fatalf("the first page holds epochs %d to %d", first[0].Epoch, first[2].Epoch)
	}
	if _, err := r.History(ctx, MessageID{9}, 3); !errors.Is(err, ErrInvalid) {
		t.Errorf("a before that is none: %v", err)
	}
	second, err := r.History(ctx, first[0].ID, 3)
	if err != nil || len(second) != 3 || second[2].Epoch != 5 {
		t.Fatalf("the second page: %+v %v", second, err)
	}
	third, err := r.History(ctx, second[0].ID, 10)
	if err != nil || len(third) != len(sent)-6 {
		t.Fatalf("the third page: %d events, %v", len(third), err)
	}
	if end, err := r.History(ctx, third[0].ID, 10); err != nil || len(end) != 0 {
		t.Fatalf("past the oldest: %d events, %v", len(end), err)
	}
	r.removed.Store(true)
	if _, err := r.History(ctx, MessageID{}, 3); !errors.Is(err, ErrRemoved) {
		t.Fatalf("History of a removed device: %v", err)
	}
}

// With the admin away and no other member holding the old segments, a joiner reads the history from the pins.
func TestHistoryComesFromThePinsWhenTheAdminIsAway(t *testing.T) {
	ps := []*pinServer{newPin(t), newPin(t)}
	peers := []pin.Peer{ps[0].peer, ps[1].peer}
	lim := Limits{KeepSegments: 100, PinGap: 50 * time.Millisecond, PinWait: 300 * time.Millisecond, CatchUpWait: 30 * time.Second}
	gr := newGroupPins(t, 2, lim, peers, nil)
	gr.waitMesh(1)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	sent := gr.advance(t, ctx, 5, "x")
	admin := gr.rooms[0]
	waitFor(t, 30*time.Second, "the admin to have pushed its logs", func() bool { return admin.Status().PinError == nil && !admin.dirtyAny() })
	for _, l := range admin.snapshot() {
		pinsHold(t, peers, gr.nodes[0].dev, l.id, l.log.Len())
	}
	gr.join(ctx, gr.newNode())
	r := gr.rooms[len(gr.rooms)-1]
	waitFor(t, 30*time.Second, "the control logs", func() bool {
		r.hmu.Lock()
		defer r.hmu.Unlock()
		return len(r.links[r.joinEpoch]) > 0 && r.controlRead()
	})
	gr.stop(0)
	if got := bodiesOf(t, readHistory(t, ctx, r, 100)); len(got) != len(sent) {
		t.Fatalf("read %d messages, want %d", len(got), len(sent))
	}
}

// A message of an epoch before the join that an admin stored in a later segment belongs to History, even once History
// holds the key of its epoch: it never shows in the live stream.
func TestOldEpochMessagesNeverShowInTheLiveStream(t *testing.T) {
	gr := newHistoryGroup(t, 2, Policy{})
	admin := gr.rooms[0]
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	gr.advance(t, ctx, 3, "o")
	gr.join(ctx, gr.newNode())
	r, rec := gr.rooms[len(gr.rooms)-1], gr.recs[len(gr.recs)-1]
	readHistory(t, ctx, r, 100)
	if _, err := admin.Send(ctx, []byte("live"), MessageID{}); err != nil {
		t.Fatal(err)
	}
	admin.kmu.RLock()
	key := admin.keys[3]
	admin.kmu.RUnlock()
	if _, err := admin.seg.log.Append(entryOf(admin, &key, 3, gr.nodes[1].dev, 3001, "old")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 30*time.Second, "the live message", func() bool { return rec.has("live") })
	time.Sleep(500 * time.Millisecond)
	if rec.has("old") {
		t.Fatal("a message of an epoch before the join shows in the live stream")
	}
	if len(catchUpErrors(rec)) != 0 {
		t.Fatalf("reports: %v", catchUpErrors(rec))
	}
}

// A message of a device that left before the join cannot be attributed: the joiner holds no certificate for it, so History
// reports it and does not show it.
func TestMessageOfADeviceThatLeftBeforeTheJoinIsReported(t *testing.T) {
	gr := newHistoryGroup(t, 3, Policy{})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if _, err := gr.rooms[2].Send(ctx, []byte("from the one who left"), MessageID{}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 30*time.Second, "the message in the admin's segment", func() bool { return segLen(gr.rooms[0]) == 1 })
	if err := gr.rooms[0].Remove(ctx, gr.nodes[2].person.PublicKey()); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 30*time.Second, "the other member at the new epoch", func() bool { return gr.rooms[1].Status().Epoch == 3 })
	gr.away = map[int]bool{2: true}
	if _, err := gr.rooms[0].Send(ctx, []byte("from the admin"), MessageID{}); err != nil {
		t.Fatal(err)
	}
	gr.join(ctx, gr.newNode())
	all := readHistory(t, ctx, gr.rooms[len(gr.rooms)-1], 10)
	var reports, reads int
	for _, ev := range all {
		switch {
		case ev.Err == nil && string(ev.Body) == "from the admin":
			reads++
		case ev.Err != nil && errors.Is(ev.Err, ErrCatchUp) && errors.Is(ev.Err, kologossip.ErrUnknownAuthor):
			reports++
		default:
			t.Errorf("unexpected event: %+v", ev)
		}
	}
	if reports != 1 || reads != 1 {
		t.Fatalf("%d reports and %d reads, want one of each", reports, reads)
	}
}
