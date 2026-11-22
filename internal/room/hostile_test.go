package room

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	kologossip "github.com/ciranda-dev/kolo-gossip"
	kolosync "github.com/ciranda-dev/kolo-sync"

	"github.com/ciranda-dev/kolo-mls/settle"
)

// hostile is a group of three whose members a test attacks through the validator, as a peer's message would arrive.
type hostile struct {
	gr      *group
	r       *Room // the room under attack: the second member's
	a, b    *tnode
	key     [keySize]byte
	epoch   uint64
	person  []byte
	deviceA []byte
	deviceB []byte
}

func newHostile(t *testing.T, lim Limits) *hostile {
	gr := newGroup(t, 3, lim)
	h := &hostile{gr: gr, r: gr.rooms[1], a: gr.nodes[0], b: gr.nodes[1]}
	v := h.r.view.Load()
	h.key, h.epoch = v.key, v.epoch
	h.person, h.deviceA, h.deviceB = h.a.person.PublicKey(), h.a.key(), h.b.key()
	return h
}

func (h *hostile) msg(epoch uint64, key *[keySize]byte, seq uint64, c *content) *kologossip.Message {
	return forged(h.r, key, epoch, h.person, h.a.dev, seq, c)
}

func TestHostileMessagesAreRefusedWithStableErrors(t *testing.T) {
	h := newHostile(t, Limits{})
	r, now := h.r, time.Now().UnixMilli()
	good := func() *content { return &content{Time: now, Body: []byte("ok")} }
	other := [keySize]byte{9}
	if err := r.check(h.msg(h.epoch, &h.key, 1, good())); err != nil {
		t.Fatalf("a good message: %v", err)
	}
	for _, c := range []struct {
		name string
		m    *kologossip.Message
		want error
		v    kologossip.Verdict
	}{
		{"wrong epoch, ahead", h.msg(h.epoch+1, &h.key, 2, good()), ErrWrongEpoch, kologossip.Ignore},
		{"wrong epoch, behind", h.msg(h.epoch-2, &h.key, 3, good()), ErrWrongEpoch, kologossip.Ignore},
		{"a key of another epoch", h.msg(h.epoch, &other, 4, good()), ErrOpen, kologossip.Reject},
		{"too many parents", h.msg(h.epoch, &h.key, 5, &content{Time: now, Parents: make([]MessageID, r.n.limits.MaxParents+1)}), ErrTooManyParents, kologossip.Reject},
		{"a time far from the envelope's", h.msg(h.epoch, &h.key, 6, &content{Time: now + int64(2*r.n.limits.MaxSkew/time.Millisecond)}), ErrBadTime, kologossip.Reject},
		{"an empty body", &kologossip.Message{Device: h.deviceA, Person: h.person, Time: time.Now()}, ErrMalformed, kologossip.Reject},
		{"an unknown kind", &kologossip.Message{Device: h.deviceA, Person: h.person, Time: time.Now(), Body: []byte{9, 9, 9}}, ErrMalformed, kologossip.Reject},
		{"a truncated body", &kologossip.Message{Device: h.deviceA, Person: h.person, Time: time.Now(), Body: h.msg(h.epoch, &h.key, 7, good()).Body[:20]}, ErrMalformed, kologossip.Reject},
	} {
		if err := r.check(c.m); !errors.Is(err, c.want) {
			t.Errorf("%s: %v, want %v", c.name, err, c.want)
		}
		if got := r.validate(context.Background(), c.m); got != c.v {
			t.Errorf("%s: verdict %v, want %v", c.name, got, c.v)
		}
	}
}

// A sealed body that a member copies into its own envelope, or into another sequence number, does not open.
func TestReplayedAndForgedSealedBodiesDoNotOpen(t *testing.T) {
	h := newHostile(t, Limits{})
	orig := h.msg(h.epoch, &h.key, 10, &content{Time: time.Now().UnixMilli(), Body: []byte("from a")})
	if err := h.r.check(orig); err != nil {
		t.Fatal(err)
	}
	asB := *orig
	asB.Device, asB.Person = h.deviceB, h.b.person.PublicKey()
	if err := h.r.check(&asB); !errors.Is(err, ErrSignature) {
		t.Fatalf("a member's sealed body under another member's envelope: %v", err)
	}
	again := *orig
	again.Seq = 11
	if err := h.r.check(&again); !errors.Is(err, ErrSignature) {
		t.Fatalf("a sealed body under another sequence number: %v", err)
	}
}

// The same message that reaches a member twice shows once.
func TestRepeatedMessageShowsOnce(t *testing.T) {
	h := newHostile(t, Limits{})
	rec := h.gr.recs[1]
	m := h.msg(h.epoch, &h.key, 20, &content{Time: time.Now().UnixMilli(), Body: []byte("twice")})
	h.r.receive(m)
	h.r.receive(m)
	waitFor(t, 5*time.Second, "the message", func() bool { return rec.has("twice") })
	time.Sleep(100 * time.Millisecond)
	n := 0
	for _, b := range rec.bodies() {
		if b == "twice" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("shown %d times", n)
	}
}

func TestOversizedBodiesAreRefused(t *testing.T) {
	h := newHostile(t, Limits{MaxMessageSize: 4096})
	ctx := context.Background()
	if _, err := h.r.Send(ctx, make([]byte, 4096), MessageID{}); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("Send of a body past the limit: %v", err)
	}
	// The sealed message adds its header, parents and padding to the body, and gossip refuses an envelope past the limit.
	if _, err := h.r.Send(ctx, make([]byte, 4096-100), MessageID{}); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("Send of a body whose envelope is past the limit: %v", err)
	}
	if _, err := h.r.Send(ctx, make([]byte, 3000), MessageID{}); err != nil {
		t.Fatalf("Send of a body under the limit: %v", err)
	}
	m := h.msg(h.epoch, &h.key, 30, &content{Time: time.Now().UnixMilli(), Body: make([]byte, 5000)})
	if err := h.r.check(m); !errors.Is(err, ErrMalformed) {
		t.Fatalf("a message past the limit reaching the validator: %v", err)
	}
}

// A device over its rate is refused with ErrRate and Reject; its tokens come back with time.
func TestFloodIsBoundedByTheRate(t *testing.T) {
	h := newHostile(t, Limits{Rate: 5, Burst: 5})
	good := &content{Time: time.Now().UnixMilli(), Body: []byte("x")}
	accepted, refused := 0, 0
	for i := range 50 {
		// Device b has sent nothing yet, so its bucket is full.
		switch err := h.r.check(forged(h.r, &h.key, h.epoch, h.b.person.PublicKey(), h.b.dev, uint64(100+i), good)); {
		case err == nil:
			accepted++
		case errors.Is(err, ErrRate):
			refused++
		default:
			t.Fatal(err)
		}
	}
	if accepted != 5 || refused != 45 {
		t.Fatalf("%d accepted, %d refused of 50 at once under a burst of 5", accepted, refused)
	}
	if got := h.r.validate(context.Background(), forged(h.r, &h.key, h.epoch, h.b.person.PublicKey(), h.b.dev, 999, good)); got != kologossip.Reject {
		t.Fatalf("verdict %v for a message over the rate", got)
	}
	l := newLimiter(5, 5)
	dev := [32]byte{1}
	for range 5 {
		l.allow(dev, t0)
	}
	if l.allow(dev, t0) || l.allow(dev, t0.Add(100*time.Millisecond)) || !l.allow(dev, t0.Add(200*time.Millisecond)) {
		t.Fatal("tokens do not come back at the rate")
	}
	if l.allow(dev, t0.Add(200*time.Millisecond)) {
		t.Fatal("a token was spent twice")
	}
	l.allow([32]byte{2}, t0)
	if len(l.buckets) != 2 {
		t.Fatalf("%d buckets", len(l.buckets))
	}
}

// Commits come only from admin devices, for the current epoch of this room.
func TestCommitMessagesAreChecked(t *testing.T) {
	h := newHostile(t, Limits{})
	v := h.r.view.Load()
	settled := func(gid []byte, epoch uint64) []byte {
		return (&settle.Settled{TBS: settle.TBS{GroupID: gid, Epoch: epoch}}).Encode()
	}
	body := func(s []byte) []byte { return (&commitMsg{Settled: s, Commit: []byte{1}}).encode() }
	m := func(dev []byte, s []byte) *kologossip.Message {
		return &kologossip.Message{Device: dev, Person: h.person, Time: time.Now(), Body: body(s)}
	}
	gid := h.r.gid[:]
	if err := h.r.check(m(h.deviceA, settled(gid, v.epoch))); err != nil {
		t.Fatalf("a Commit of the admin for this epoch: %v", err)
	}
	if err := h.r.check(m(h.deviceB, settled(gid, v.epoch))); !errors.Is(err, ErrNotAdmin) {
		t.Fatalf("a Commit of a device that is no admin: %v", err)
	}
	if err := h.r.check(m(h.deviceA, settled(gid, v.epoch-1))); !errors.Is(err, ErrWrongEpoch) {
		t.Fatalf("a Commit of an old epoch: %v", err)
	}
	if err := h.r.check(m(h.deviceA, settled(make([]byte, 32), v.epoch))); !errors.Is(err, ErrWrongEpoch) {
		t.Fatalf("a Commit of another room: %v", err)
	}
	if err := h.r.check(m(h.deviceA, []byte("not a settled commit"))); !errors.Is(err, ErrMalformed) {
		t.Fatalf("a Commit that does not decode: %v", err)
	}
}

// Messages sent at once from every member reach every member, each device's in its own order, and a reply shows
// after the message it answers.
func TestConcurrentSendersKeepTheirOrderAndRepliesFollowParents(t *testing.T) {
	gr := newGroup(t, 3, Limits{})
	gr.waitMesh(2)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	first, err := gr.rooms[0].Send(ctx, []byte("question"), MessageID{})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, 30*time.Second, "the question at b", func() bool { return gr.recs[1].has("question") })
	if _, err := gr.rooms[1].Send(ctx, []byte("answer"), first); err != nil {
		t.Fatal(err)
	}
	const per = 8
	done := make(chan error, 3)
	for i, r := range gr.rooms {
		go func() {
			for j := range per {
				if _, err := r.Send(ctx, []byte(fmt.Sprintf("d%d-%d", i, j)), MessageID{}); err != nil {
					done <- err
					return
				}
			}
			done <- nil
		}()
	}
	for range 3 {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	for i, rec := range gr.recs {
		waitFor(t, 60*time.Second, "every message", func() bool { return len(rec.messages()) == 2+3*per })
		pos := map[string]int{}
		for k, b := range rec.bodies() {
			pos[b] = k
		}
		if pos["answer"] < pos["question"] {
			t.Fatalf("member %d shows the answer before the question", i)
		}
		for d := range 3 {
			for j := 1; j < per; j++ {
				if pos[fmt.Sprintf("d%d-%d", d, j)] < pos[fmt.Sprintf("d%d-%d", d, j-1)] {
					t.Fatalf("member %d shows device %d out of its order", i, d)
				}
			}
		}
	}
	// Every member shows the same causal order of what it holds.
	var want []MessageID
	for i, rec := range gr.recs {
		got := ids(Display(rec.messages()))
		if i == 0 {
			want = got
		} else if fmt.Sprint(got) != fmt.Sprint(want) {
			t.Fatalf("member %d displays another order", i)
		}
	}
}

// Gossip and kolo-sync share one connection: sync on its bidirectional streams, gossip on the unidirectional ones.
func TestSyncAndGossipShareOneConnection(t *testing.T) {
	gr := newGroup(t, 3, Limits{})
	gr.waitMesh(2)
	a, b := gr.nodes[0], gr.nodes[1]
	ctl := gr.rooms[0].ctl
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	waitFor(t, 10*time.Second, "a connection from b to a", func() bool { return b.n.syncConn(a.key()) != nil })
	if _, err := kolosync.FetchHeader(ctx, b.n.syncConn(a.key()), a.key(), ctl.id); err != nil {
		t.Fatalf("a member fetching a log over the shared connection: %v", err)
	}
	if _, err := gr.rooms[0].Send(ctx, []byte("still gossiping"), MessageID{}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 10*time.Second, "the message", func() bool { return gr.recs[1].has("still gossiping") })
}

// TestEnvelopeOverhead measures what gossip's envelope adds to a body, so envelopeReserve is a bound and no guess.
func TestEnvelopeOverhead(t *testing.T) {
	const limit = 4096
	gr := newGroup(t, 2, Limits{MaxMessageSize: limit})
	ctx := context.Background()
	body := func(n int) []byte { return make([]byte, n) }
	pub := func(n int) error {
		_, err := gr.rooms[0].publish(ctx, func(uint64) ([]byte, error) { return body(n), nil })
		return err
	}
	lo := 0
	for n := limit; n > 0; n-- {
		if err := pub(n); err == nil {
			lo = n
			break
		} else if !errors.Is(err, ErrTooLarge) {
			t.Fatal(err)
		}
	}
	overhead := limit - lo
	t.Logf("gossip's envelope adds %d bytes to a body", overhead)
	if overhead <= 0 || overhead > envelopeReserve {
		t.Fatalf("overhead %d against a reserve of %d", overhead, envelopeReserve)
	}
}

// FuzzCheck feeds the validator bodies a hostile peer could send: it must return an error or nil and never panic.
func FuzzCheck(f *testing.F) {
	gr := newGroup(f, 2, Limits{Rate: 1e6, Burst: 1 << 20})
	r, a := gr.rooms[1], gr.nodes[0]
	v := r.view.Load()
	good := forged(r, &v.key, v.epoch, a.person.PublicKey(), a.dev, 1, &content{Time: time.Now().UnixMilli(), Body: []byte("x")}).Body
	f.Add(good)
	f.Add((&commitMsg{Settled: (&settle.Settled{TBS: settle.TBS{GroupID: r.gid[:], Epoch: v.epoch}}).Encode(), Commit: []byte{1}}).encode())
	f.Add([]byte{kindCommit, 0xff, 0xff})
	f.Add([]byte{kindLeave})
	f.Add([]byte{kindLeave, 1})
	f.Fuzz(func(t *testing.T, body []byte) {
		r.check(&kologossip.Message{Person: a.person.PublicKey(), Device: a.key(), Seq: 1, Time: time.Now(), Body: body})
	})
}
