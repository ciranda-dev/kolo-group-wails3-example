package room

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"net/netip"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	kologossip "github.com/ciranda-dev/kolo-gossip"
	"github.com/ciranda-dev/kolo-mls/settle"
	kolostore "github.com/ciranda-dev/kolo-store"
	koloswarm "github.com/ciranda-dev/kolo-swarm"
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

// A history link that opens to a wrong key does not end History: every key the links give is tried, and the disagreement
// is reported.
func TestWrongHistoryLinkDoesNotHideTheHonestOne(t *testing.T) {
	gr := newGroup(t, 2, Limits{CatchUpWait: 5 * time.Second})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	sent := gr.advance(t, ctx, 3, "old")
	late := gr.newNode()
	gr.join(ctx, late)
	r := gr.rooms[2]
	je := r.joinEpoch
	waitFor(t, 30*time.Second, "the link of the join epoch", func() bool {
		r.hmu.Lock()
		defer r.hmu.Unlock()
		return len(r.links[je]) > 0
	})
	r.kmu.RLock()
	key := r.keys[je]
	r.kmu.RUnlock()
	var fake [keySize]byte
	rand.Read(fake[:])
	bad, err := sealLink(r.gid[:], je, &key, &fake)
	if err != nil {
		t.Fatal(err)
	}
	var owner [32]byte
	rand.Read(owner[:])
	r.hmu.Lock()
	r.links[je] = append([]linkRec{{owner: owner, link: [linkSize]byte(bad)}}, r.links[je]...)
	r.hmu.Unlock()
	evs, err := r.History(ctx, MessageID{}, 100)
	if err != nil {
		t.Fatal(err)
	}
	shown, reported := 0, false
	for _, ev := range evs {
		switch {
		case errors.Is(ev.Err, ErrLink):
			reported = true
		case ev.Err != nil:
			t.Errorf("history entry: %v", ev.Err)
		case strings.HasPrefix(string(ev.Body), "old"):
			shown++
		}
	}
	if shown != len(sent) || !reported {
		t.Fatalf("%d of %d old messages shown, the wrong link reported: %v", shown, len(sent), reported)
	}
	r = gr.restart(2)
	if got := len(r.keysOf(je - 1)); got != 2 {
		t.Fatalf("after a restart the room holds %d keys of epoch %d", got, je-1)
	}
	if evs, err = r.History(ctx, MessageID{}, 100); err != nil {
		t.Fatal(err)
	}
	shown = 0
	for _, ev := range evs {
		if ev.Err == nil && strings.HasPrefix(string(ev.Body), "old") {
			shown++
		}
	}
	if shown != len(sent) {
		t.Fatalf("after a restart %d of %d old messages shown", shown, len(sent))
	}
}

// The tree log of an Add is read by its joiner and by the members while its epoch lasts, and never by a reader of the read
// link or by a member once the room has moved on.
func TestTreeLogIsNotServedToReadersOrAfterItsEpoch(t *testing.T) {
	server := newServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	lim := Limits{PinWait: 200 * time.Millisecond}
	x := newTNode(t, tnodeOpts{server: server, limits: lim})
	y := newTNode(t, tnodeOpts{server: server, limits: lim})
	y2 := newTNode(t, tnodeOpts{server: server, limits: lim})
	admin, err := x.n.Create(ctx, "public", Policy{AdminsPost: true, ReadLink: true})
	if err != nil {
		t.Fatal(err)
	}
	w, err := admin.Add(ctx, kpOf(t, y), kpOf(t, y2))
	if err != nil {
		t.Fatal(err)
	}
	for _, nd := range []*tnode{y, y2} {
		if _, err := nd.n.Join(ctx, w); err != nil {
			t.Fatal(err)
		}
	}
	link, err := admin.Link()
	if err != nil {
		t.Fatal(err)
	}
	s := newTNode(t, tnodeOpts{server: server, limits: lim})
	if _, err := s.n.JoinLink(ctx, link); err != nil {
		t.Fatal(err)
	}
	z := newTNode(t, tnodeOpts{server: server, limits: lim})
	wz, inv := addFor(t, ctx, admin, z)
	waitFor(t, 30*time.Second, "the reader and the member connected to the admin", func() bool {
		return x.n.syncConn(s.key()) != nil && x.n.syncConn(y.key()) != nil && x.n.syncConn(y2.key()) != nil
	})
	fetch := func(nd *tnode, d time.Duration) error {
		fctx, fcancel := context.WithTimeout(ctx, d)
		defer fcancel()
		_, err := nd.n.fetchTree(fctx, inv)
		return err
	}
	if err := fetch(s, 3*time.Second); err == nil {
		t.Fatal("a reader of the read link pulled the tree log")
	}
	if err := fetch(y, 20*time.Second); err != nil {
		t.Fatalf("a member in the epoch of the tree: %v", err)
	}
	e := admin.Status().Epoch
	if err := admin.SetPolicy(ctx, Policy{AdminsPost: true, ReadLink: true, History: SinceEpoch, Since: e}); err != nil {
		t.Fatal(err)
	}
	if err := fetch(y2, 3*time.Second); err == nil {
		t.Fatal("a member pulled the tree log of an epoch the room has left")
	}
	if _, err := z.n.Join(ctx, wz); err != nil {
		t.Fatalf("the joiner: %v", err)
	}
}

// What followPin reads of a segment does not race with the sync loop closing it (run with -race).
func TestPinPullReadsSegmentStateUnderItsLock(t *testing.T) {
	gr := newGroup(t, 2, Limits{})
	gr.waitMesh(1)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if _, err := gr.rooms[0].Send(ctx, []byte("x"), MessageID{}); err != nil {
		t.Fatal(err)
	}
	m := gr.rooms[1]
	var seg, ctl *roomLog
	waitFor(t, 20*time.Second, "a replica of a segment", func() bool {
		for _, l := range m.snapshot() {
			if !l.own && !l.ctl && !l.tree {
				seg = l
			}
			if l.ctl && !l.own {
				ctl = l
			}
		}
		return seg != nil && ctl != nil
	})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for range 1000 {
			m.missing(seg)
		}
	}()
	m.applyCtl(ctl, 0, &ctlRecord{Kind: ctlClose, ID: seg.id, Size: 1})
	<-done
}

// failDeletes is a backend whose Delete fails once armed.
type failDeletes struct {
	kolostore.Backend
	armed atomic.Bool
}

func (f *failDeletes) Delete(k []byte) error {
	if f.armed.Load() {
		return errors.New("delete refused")
	}
	return f.Backend.Delete(k)
}

// An admin whose event queue is full does not hold the room's lock while it reports that a used Welcome could not be
// released, so its Commits go on.
func TestContactReportDoesNotHoldTheRoomLock(t *testing.T) {
	server := newServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	lim := Limits{EventQueue: 1}
	x := newTNode(t, tnodeOpts{server: server, limits: lim})
	y := newTNode(t, tnodeOpts{server: server, limits: lim})
	r, err := x.n.Create(ctx, "test", Policy{})
	if err != nil {
		t.Fatal(err)
	}
	gid := r.ID()
	w, err := r.Add(ctx, kpOf(t, y))
	if err != nil {
		t.Fatal(err)
	}
	ry, err := y.n.Join(ctx, w)
	if err != nil {
		t.Fatal(err)
	}
	record(t, ry)
	fd := &failDeletes{Backend: x.backend}
	x.backend = fd
	x.restart()
	if r, err = x.n.Room(gid); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 30*time.Second, "the gossip mesh", func() bool { return r.gr.Stats().Mesh > 0 && ry.gr.Stats().Mesh > 0 })
	for len(r.events) < cap(r.events) {
		r.events <- Event{}
	}
	fd.armed.Store(true)
	if _, err := ry.Send(ctx, []byte("hello"), MessageID{}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 20*time.Second, "the Welcome taken as used", func() bool { return unused(r) == 0 })
	time.Sleep(200 * time.Millisecond)
	waitFor(t, 5*time.Second, "the room lock", func() bool {
		if !r.mu.TryLock() {
			return false
		}
		r.mu.Unlock()
		return true
	})
}

// A device over its rate is refused before its signatures are checked.
func TestRateIsCheckedBeforeTheSignatures(t *testing.T) {
	gr := newGroup(t, 2, Limits{Rate: 1, Burst: 2})
	gr.waitMesh(1)
	admin, member := gr.rooms[0], gr.rooms[1]
	e := admin.Status().Epoch
	exhaust := func(r *Room, mk func() *kologossip.Message) {
		t.Helper()
		for i := 0; ; i++ {
			err := r.check(mk())
			if errors.Is(err, ErrRate) {
				return
			}
			if err != nil || i > 10 {
				t.Fatalf("message %d: %v", i, err)
			}
		}
	}
	exhaust(admin, func() *kologossip.Message { return sealAs(t, admin, gr.nodes[1], e, member.seq.Add(1), "x") })
	bad := sealAs(t, admin, gr.nodes[1], e, member.seq.Add(1), "bad signature")
	bad.Body[len(bad.Body)-1] ^= 1
	if err := admin.check(bad); !errors.Is(err, ErrRate) {
		t.Fatalf("a message with a bad signature over the rate: %v", err)
	}
	offer := func() *kologossip.Message {
		return &kologossip.Message{Person: gr.nodes[0].person.PublicKey(), Device: gr.nodes[0].key(), Seq: admin.seq.Add(1), Time: time.Now(),
			Body: (&adminOffer{Kind: kindPromote, Device: [32]byte(gr.nodes[1].key())}).encode()}
	}
	exhaust(member, offer)
	forged := &settle.Settled{TBS: settle.TBS{GroupID: admin.gid[:], Epoch: e}}
	ref := offer()
	ref.Body = (&commitRef{Settled: forged.Encode()}).encode()
	if err := member.check(ref); !errors.Is(err, ErrRate) {
		t.Fatalf("an unsigned announcement over the rate: %v", err)
	}
}

// Requests to leave sent again and again while the removal settles start one removal.
func TestRepeatedLeaveRequestsStartOneRemoval(t *testing.T) {
	gr := newGroup(t, 2, Limits{})
	gr.waitMesh(1)
	admin := gr.rooms[0]
	m := &kologossip.Message{Person: gr.nodes[1].person.PublicKey(), Device: gr.nodes[1].key(), Body: []byte{kindLeave}}
	before := runtime.NumGoroutine()
	for range 200 {
		admin.handleLeave(m)
	}
	if n := runtime.NumGoroutine() - before; n > 20 {
		t.Fatalf("200 requests started %d goroutines", n)
	}
	gr.waitEpoch(admin.Status().Epoch)
	waitFor(t, 30*time.Second, "the removal", func() bool { return len(admin.Members()) == 1 })
}

// A segment whose entry keys failed once still reports that it lacks entries.
func TestGapIsReportedAfterAKeyFailure(t *testing.T) {
	gr := newGroup(t, 2, Limits{CatchUpWait: 100 * time.Millisecond})
	gr.waitMesh(1)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if _, err := gr.rooms[0].Send(ctx, []byte("x"), MessageID{}); err != nil {
		t.Fatal(err)
	}
	m := gr.rooms[1]
	var seg *roomLog
	waitFor(t, 20*time.Second, "a replica of a segment", func() bool {
		for _, l := range m.snapshot() {
			if !l.own && !l.ctl && !l.tree {
				seg = l
			}
		}
		return seg != nil
	})
	m.reportOnce(seg, errors.New("a key that failed"))
	m.logMu.Lock()
	seg.closed, seg.size = true, 1000
	m.logMu.Unlock()
	waitFor(t, 10*time.Second, "the report of the gap", func() bool {
		for _, ev := range errEvents(gr.recs[1]) {
			if strings.Contains(ev.Err.Error(), "of 1000 entries") {
				return true
			}
		}
		return false
	})
}

// Each secret of a room comes from its own label: the room secret is not the MLS export under the topic's label.
func TestRoomSecretAndTopicUseTheirOwnLabels(t *testing.T) {
	gr := newGroup(t, 1, Limits{})
	r := gr.rooms[0]
	r.mu.Lock()
	b, err := r.m.Export("kolo-group/topic/v0", r.m.GroupID(), keySize)
	r.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if [keySize]byte(b) == r.secret {
		t.Fatal("the room secret is the export under the topic's label")
	}
}

// copyBackend returns a copy of what b holds.
func copyBackend(t *testing.T, b kolostore.Backend) kolostore.Backend {
	t.Helper()
	c := kolostore.NewMemory()
	var err error
	if serr := b.Scan(nil, nil, false, 0, func(k, v []byte) bool {
		err = c.Put(k, v)
		return err == nil
	}); serr != nil || err != nil {
		t.Fatal(serr, err)
	}
	return c
}

// ownRecord reports whether this admin's control log holds a record of kind for epoch.
func ownRecord(t *testing.T, r *Room, kind byte, epoch uint64) bool {
	t.Helper()
	r.logMu.Lock()
	l := r.ctl
	r.logMu.Unlock()
	for i := uint64(0); i < l.log.Len(); i++ {
		b, err := l.log.Get(i)
		if err != nil {
			t.Fatal(err)
		}
		if rec, err := decodeCtl(b, r.n.limits.MaxMessageSize); err == nil && rec.Kind == kind && rec.Epoch == epoch {
			return true
		}
	}
	return false
}

// An admin that stopped after it applied a Commit and before it wrote the history link and the readers' state of the new
// epoch writes them when it opens the room again.
func TestReopenWritesTheLinkAndStateACrashLost(t *testing.T) {
	server := newServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	x := newTNode(t, tnodeOpts{server: server})
	r, err := x.n.Create(ctx, "test", Policy{AdminsPost: true, ReadLink: true})
	if err != nil {
		t.Fatal(err)
	}
	gid := r.ID()
	time.Sleep(200 * time.Millisecond)
	logs, state := copyBackend(t, x.logs), copyBackend(t, x.backend)
	e := r.Status().Epoch
	if err := r.SetPolicy(ctx, Policy{AdminsPost: true, ReadLink: true, History: SinceEpoch, Since: e}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	x.stop()
	x.store.Close()
	// The logs and the room's records as they were before the Commit, with the MLS state after it.
	x.logs, x.store, x.backend = logs, kolostore.New(logs), state
	x.start()
	if r, err = x.n.Room(gid); err != nil {
		t.Fatal(err)
	}
	if got := r.Status().Epoch; got != e+1 {
		t.Fatalf("reopened at epoch %d, want %d", got, e+1)
	}
	if !ownRecord(t, r, ctlLink, e+1) || !ownRecord(t, r, ctlRead, e+1) {
		t.Fatalf("link of epoch %d: %v, readers' state: %v", e+1, ownRecord(t, r, ctlLink, e+1), ownRecord(t, r, ctlRead, e+1))
	}
}

// A device that restarts while no server of the discovery network answers opens its rooms once one does.
func TestRestartWaitsForTheDiscoveryNetwork(t *testing.T) {
	gr := newGroup(t, 2, Limits{PinWait: 200 * time.Millisecond})
	gr.waitMesh(1)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	nd := gr.nodes[1]
	nd.stop()
	nd.opts.beforeOpen = func(sw *koloswarm.Swarm) {
		go func() {
			time.Sleep(time.Second)
			sw.Bootstrap(ctx, []netip.AddrPort{addrOf(gr.server.Addr())})
		}()
	}
	nd.start()
	if n := len(nd.n.Rooms()); n != 1 {
		t.Fatalf("the device reopened %d rooms", n)
	}
}

// ctlEntries counts the entries of the control logs that r holds, its own when own is set.
func ctlEntries(r *Room, own bool) (n uint64) {
	for _, l := range r.snapshot() {
		if l.ctl && l.own == own {
			n += l.view.Len()
		}
	}
	return n
}

// A joiner follows the control logs of the current epoch only, and History reads the epochs before through the logs that
// those name, one epoch back at a time.
func TestJoinerPullsOnlyTheCurrentControlLogs(t *testing.T) {
	gr := newGroup(t, 2, Limits{CatchUpWait: 5 * time.Second})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	sent := gr.advance(t, ctx, 6, "old")
	gr.join(ctx, gr.newNode())
	r := gr.rooms[2]
	time.Sleep(time.Second)
	held, all := ctlEntries(r, false), ctlEntries(gr.rooms[0], true)
	t.Logf("the joiner holds %d of the %d entries of the admin's control logs", held, all)
	if held*3 > all {
		t.Fatalf("the joiner holds %d of the %d entries of the admin's control logs", held, all)
	}
	got := bodiesOf(t, readHistory(t, ctx, r, 10))
	for body, e := range sent {
		if got[body] != e {
			t.Errorf("history lacks %s of epoch %d", body, e)
		}
	}
}

// A member forgets the control logs of the epochs it has left, past KeepSegments, and keeps following the room.
func TestMemberForgetsTheControlLogsOfPastEpochs(t *testing.T) {
	gr := newGroup(t, 2, Limits{KeepSegments: 1})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	gr.advance(t, ctx, 6, "e")
	m := gr.rooms[1]
	waitFor(t, 20*time.Second, "the member to forget the old control logs", func() bool {
		n := 0
		for _, l := range m.snapshot() {
			if l.ctl {
				n++
			}
		}
		return n <= 3
	})
	if _, err := gr.rooms[0].Send(ctx, []byte("still here"), MessageID{}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 20*time.Second, "a message after", func() bool { return gr.recs[1].has("still here") })
}
