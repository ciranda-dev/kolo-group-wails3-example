package room

import (
	"context"
	"crypto/ed25519"
	"encoding/binary"
	"errors"
	"testing"
	"time"

	kologossip "github.com/ciranda-dev/kolo-gossip"
)

// sealAs returns text sealed in epoch e of r as the device of tn would send it, in the form gossip delivers it.
func sealAs(t testing.TB, r *Room, tn *tnode, e, seq uint64, text string) *kologossip.Message {
	t.Helper()
	r.kmu.RLock()
	key, ok := r.keys[e]
	r.kmu.RUnlock()
	if !ok {
		t.Fatalf("no key of epoch %d", e)
	}
	now := time.Now()
	c := &content{Time: now.UnixMilli(), Body: []byte(text)}
	nonce, ct, err := seal(&key, aad(r.gid[:], e, tn.key(), seq), c.encode())
	if err != nil {
		t.Fatal(err)
	}
	s := &sealed{Epoch: e, Nonce: nonce, CT: ct}
	if err := signSealed(tn.dev, r.gid[:], s, seq); err != nil {
		t.Fatal(err)
	}
	return &kologossip.Message{Person: tn.person.PublicKey(), Device: tn.key(), Seq: seq, Time: now, Body: s.encode()}
}

// rawPublish publishes a sealed message on the gossip topic of r, past the checks that Send makes.
func rawPublish(t testing.TB, r *Room, m *kologossip.Message) {
	t.Helper()
	r.sendMu.Lock()
	defer r.sendMu.Unlock()
	r.cur.Store(m.Seq)
	if _, err := r.gr.Publish(context.Background(), m.Body); err != nil {
		t.Fatal(err)
	}
}

func hasErr(rec *recorder, want error) bool {
	for _, ev := range rec.events() {
		if ev.Err != nil && errors.Is(ev.Err, want) {
			return true
		}
	}
	return false
}

// A device that is no poster cannot send, its message is refused by every validator and by an admin's segment writer, and
// the posters' messages reach it.
func TestNonPosterIsRefusedAtValidatorsAndAdmins(t *testing.T) {
	p0 := ident(t)
	gr := newGroupWith(t, 3, Limits{}, func(o *tnodeOpts) {
		o.person, o.policy = p0, Policy{Posters: []ed25519.PublicKey{p0.PublicKey()}}
	})
	gr.waitMesh(2)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	admin, reader, other := gr.rooms[0], gr.rooms[1], gr.rooms[2]
	if _, err := reader.Send(ctx, []byte("no"), MessageID{}); !errors.Is(err, ErrNotPoster) {
		t.Fatalf("Send by a device that is no poster: %v", err)
	}
	if _, err := admin.Send(ctx, []byte("hello"), MessageID{}); err != nil {
		t.Fatal(err)
	}
	for i := range gr.rooms {
		waitFor(t, 20*time.Second, "the poster's message", func() bool { return gr.recs[i].has("hello") })
	}
	e := admin.Status().Epoch
	forged := sealAs(t, admin, gr.nodes[1], e, reader.seq.Add(1), "forged")
	for i, r := range []*Room{admin, other} {
		if err := r.check(forged); !errors.Is(err, ErrNotPoster) {
			t.Errorf("validator %d: %v", i, err)
		}
		if v := r.validate(ctx, forged); v != kologossip.Reject {
			t.Errorf("validator %d: verdict %v", i, v)
		}
	}
	before := segLen(admin)
	admin.receive(forged)
	waitFor(t, 5*time.Second, "the refusal", func() bool { return hasErr(gr.recs[0], ErrNotPoster) })
	if got := segLen(admin); got != before || gr.recs[0].has("forged") {
		t.Fatalf("the admin stored or showed a message of a device that is no poster: %d entries of %d", got, before)
	}
	rawPublish(t, reader, sealAs(t, admin, gr.nodes[1], e, reader.seq.Add(1), "forged on the wire"))
	if _, err := admin.Send(ctx, []byte("after"), MessageID{}); err != nil {
		t.Fatal(err)
	}
	for i := range gr.rooms {
		waitFor(t, 20*time.Second, "the poster's second message", func() bool { return gr.recs[i].has("after") })
		if gr.recs[i].has("forged on the wire") || gr.recs[i].has("forged") {
			t.Errorf("member %d shows a message of a device that is no poster", i)
		}
	}
	if got := segLen(admin); got != before+1 {
		t.Errorf("the admin's segment holds %d entries, want %d", got, before+1)
	}
}

// A member that is no poster reads everything its history rule allows, the live messages and the ones before its join.
func TestReadOnlyMemberReadsLiveAndHistory(t *testing.T) {
	p0 := ident(t)
	gr := newGroupWith(t, 2, Limits{KeepSegments: 200, CatchUpWait: 10 * time.Second}, func(o *tnodeOpts) {
		o.person, o.policy = p0, Policy{Posters: []ed25519.PublicKey{p0.PublicKey()}}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	sent := gr.advance(t, ctx, 3, "old")
	late := gr.newNode()
	gr.join(ctx, late)
	r, rec := gr.rooms[2], gr.recs[2]
	gr.waitMesh(2)
	if _, err := gr.rooms[0].Send(ctx, []byte("live"), MessageID{}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 20*time.Second, "the live message", func() bool { return rec.has("live") })
	got := bodiesOf(t, readHistory(t, ctx, r, 10))
	for body, epoch := range sent {
		if got[body] != epoch {
			t.Errorf("history lacks %s of epoch %d", body, epoch)
		}
	}
	if _, err := r.Send(ctx, []byte("no"), MessageID{}); !errors.Is(err, ErrNotPoster) {
		t.Fatalf("Send by a read-only member: %v", err)
	}
	if p := r.Policy(); len(p.Posters) != 1 || p.ReadLink {
		t.Fatalf("policy %+v", p)
	}
}

// settle accepts a change of the policy extension in a Commit, the new policy holds from the next epoch, and a message
// sealed in the epoch before is still judged by the policy of that epoch.
func TestSetPolicyHoldsFromTheNextEpochAndNotBefore(t *testing.T) {
	gr := newGroup(t, 3, Limits{})
	gr.waitMesh(2)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	admin, member := gr.rooms[0], gr.rooms[1]
	p0 := gr.nodes[0].person.PublicKey()
	if _, err := member.Send(ctx, []byte("before"), MessageID{}); err != nil {
		t.Fatal(err)
	}
	e := admin.Status().Epoch
	if err := admin.SetPolicy(ctx, Policy{Posters: []ed25519.PublicKey{p0}}); err != nil {
		t.Fatal(err)
	}
	gr.waitEpoch(e + 1)
	for i, r := range gr.rooms {
		if p := r.Policy(); len(p.Posters) != 1 || !p.Posters[0].Equal(p0) {
			t.Errorf("member %d holds policy %+v", i, p)
		}
	}
	if _, err := member.Send(ctx, []byte("after"), MessageID{}); !errors.Is(err, ErrNotPoster) {
		t.Fatalf("Send under the new policy: %v", err)
	}
	if err := admin.check(sealAs(t, admin, gr.nodes[1], e+1, member.seq.Add(1), "new epoch")); !errors.Is(err, ErrNotPoster) {
		t.Errorf("a message of the new epoch: %v", err)
	}
	old := sealAs(t, admin, gr.nodes[1], e, member.seq.Add(1), "late")
	for i, r := range gr.rooms {
		if err := r.check(old); err != nil {
			t.Errorf("a message of the epoch before at member %d: %v", i, err)
		}
	}
	rawPublish(t, member, old)
	for _, i := range []int{0, 2} {
		waitFor(t, 20*time.Second, "the message of the epoch before", func() bool { return gr.recs[i].has("late") })
	}
	if err := admin.SetPolicy(ctx, Policy{}); err != nil {
		t.Fatal(err)
	}
	gr.waitEpoch(e + 2)
	if _, err := member.Send(ctx, []byte("again"), MessageID{}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 20*time.Second, "the message under the policy that lets everyone post", func() bool { return gr.recs[0].has("again") })
}

// The policy of each epoch is kept across a restart, so an old message is judged by its own epoch's policy.
func TestPolicyOfEachEpochSurvivesARestart(t *testing.T) {
	gr := newGroup(t, 2, Limits{})
	gr.waitMesh(1)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	p0 := gr.nodes[0].person.PublicKey()
	e := gr.rooms[0].Status().Epoch
	if err := gr.rooms[0].SetPolicy(ctx, Policy{Posters: []ed25519.PublicKey{p0}}); err != nil {
		t.Fatal(err)
	}
	gr.waitEpoch(e + 1)
	old := sealAs(t, gr.rooms[0], gr.nodes[1], e, gr.rooms[1].seq.Add(1), "late")
	fresh := sealAs(t, gr.rooms[0], gr.nodes[1], e+1, gr.rooms[1].seq.Add(1), "new")
	for _, i := range []int{0, 1} {
		r := gr.restart(i)
		if err := r.check(old); err != nil {
			t.Errorf("member %d, a message of the epoch before: %v", i, err)
		}
		if err := r.check(fresh); !errors.Is(err, ErrNotPoster) {
			t.Errorf("member %d, a message of the new epoch: %v", i, err)
		}
	}
}

// A policy whose Commit is far past one gossip message still reaches the members, by reference.
func TestSetPolicyWithACommitPastOneGossipMessage(t *testing.T) {
	gr := newGroupWith(t, 2, Limits{MaxMessageSize: 64 << 10}, nil)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	var posters []ed25519.PublicKey
	for i := range 3000 {
		k := make(ed25519.PublicKey, 32)
		binary.BigEndian.PutUint32(k, uint32(i))
		posters = append(posters, k)
	}
	e := gr.rooms[0].Status().Epoch
	if err := gr.rooms[0].SetPolicy(ctx, Policy{Posters: posters}); err != nil {
		t.Fatalf("SetPolicy: %v", err)
	}
	gr.waitEpoch(e + 1)
	if got := len(gr.rooms[1].view.Load().pol.persons); got != 3000 {
		t.Fatalf("the member holds %d posters", got)
	}
}

// A history rule set by a Commit holds from the next epoch: epochs that had no links stay closed, and a joiner reads back
// only to the cut.
func TestHistoryRuleChangedByACommitIsNotRetroactive(t *testing.T) {
	gr := newHistoryGroup(t, 2, Policy{})
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	admin := gr.rooms[0]
	first := gr.advance(t, ctx, 2, "a")
	if err := admin.SetPolicy(ctx, Policy{History: NoHistory}); err != nil {
		t.Fatal(err)
	}
	gr.waitEpoch(admin.Status().Epoch)
	second := gr.advance(t, ctx, 2, "b")
	cut := admin.Status().Epoch
	if err := admin.SetPolicy(ctx, Policy{}); err != nil {
		t.Fatal(err)
	}
	if want := (histPolicy{Rule: AllHistory, Since: cut}); admin.view.Load().pol.hist != want {
		t.Fatalf("the rule is %+v, want %+v", admin.view.Load().pol.hist, want)
	}
	gr.waitEpoch(cut + 1)
	third := gr.advance(t, ctx, 2, "c")
	gr.join(ctx, gr.newNode())
	r, rec := gr.rooms[2], gr.recs[2]
	got := bodiesOf(t, readHistory(t, ctx, r, 10))
	for body, e := range third {
		if got[body] != e {
			t.Errorf("history lacks %s of epoch %d", body, e)
		}
	}
	for _, closed := range []map[string]uint64{first, second} {
		for body, e := range closed {
			if _, ok := got[body]; ok {
				t.Errorf("history holds %s of epoch %d, before the cut at %d", body, e, cut)
			}
		}
	}
	if hasErr(rec, ErrLink) {
		t.Error("a joiner reported a link that the rule of its epoch allowed")
	}
}
