package room

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"net/netip"
	"testing"
	"time"

	kologossip "github.com/ciranda-dev/kolo-gossip"
	koloid "github.com/ciranda-dev/kolo-id"
	koloswarm "github.com/ciranda-dev/kolo-swarm"
)

// authenticators returns the epoch authenticator of every member.
// authenticatorOf returns the epoch authenticator of r's member, which the room's goroutines use under r.mu.
func authenticatorOf(r *Room) []byte {
	r.mu.Lock()
	defer r.mu.Unlock()
	a, _ := r.m.EpochAuthenticator()
	return a
}

func (gr *group) authenticators() [][]byte {
	var out [][]byte
	for _, r := range gr.rooms {
		r.mu.Lock()
		a, err := r.m.EpochAuthenticator()
		r.mu.Unlock()
		if err != nil {
			gr.t.Fatal(err)
		}
		out = append(out, a)
	}
	return out
}

// checkDelivered fails unless every member holds exactly the messages want, each opened and from its sender.
func (gr *group) checkDelivered(want map[string]*tnode) {
	gr.t.Helper()
	for i, rec := range gr.recs {
		deadline := time.Now().Add(30 * time.Second)
		for len(rec.messages()) < len(want) && time.Now().Before(deadline) {
			time.Sleep(10 * time.Millisecond)
		}
		if len(rec.messages()) < len(want) {
			var missing []string
			for body := range want {
				if !rec.has(body) {
					missing = append(missing, body)
				}
			}
			var tot kologossip.RouterStats
			for _, n := range gr.nodes {
				st := n.g.Stats().Router
				tot.Rejected, tot.Ignored, tot.UnknownAuthor, tot.Throttled, tot.OutOfWindow = tot.Rejected+st.Rejected, tot.Ignored+st.Ignored, tot.UnknownAuthor+st.UnknownAuthor, tot.Throttled+st.Throttled, tot.OutOfWindow+st.OutOfWindow
				tot.Resolving, tot.CertBroken, tot.SeenFull = tot.Resolving+st.Resolving, tot.CertBroken+st.CertBroken, tot.SeenFull+st.SeenFull
			}
			gr.t.Fatalf("member %d holds %d of %d messages, missing %v; in all nodes of gossip: rejected %d ignored %d unknown author %d throttled %d out of window %d resolving %d cert broken %d seen full %d", i, len(rec.messages()), len(want), missing,
				tot.Rejected, tot.Ignored, tot.UnknownAuthor, tot.Throttled, tot.OutOfWindow, tot.Resolving, tot.CertBroken, tot.SeenFull)
		}
		got := map[string]Event{}
		for _, ev := range rec.messages() {
			got[string(ev.Body)] = ev
		}
		for body, from := range want {
			ev, ok := got[body]
			if !ok || !bytes.Equal(ev.Device, from.key()) || !bytes.Equal(ev.Person, from.person.PublicKey()) {
				gr.t.Fatalf("member %d: message %q missing or from the wrong device", i, body)
			}
		}
		for _, ev := range rec.events() {
			if ev.Err != nil {
				gr.t.Fatalf("member %d: event error %v", i, ev.Err)
			}
		}
	}
}

func TestThreeNodesDeliverEveryMessage(t *testing.T) {
	gr := newGroup(t, 3, Limits{})
	gr.waitMesh(2)
	for i, r := range gr.rooms {
		if got := len(r.Members()); got != 3 {
			t.Fatalf("member %d lists %d members", i, got)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	want := map[string]*tnode{}
	for i, r := range gr.rooms {
		for j := range 3 {
			body := fmt.Sprintf("m%d-%d", i, j)
			if _, err := r.Send(ctx, []byte(body), MessageID{}); err != nil {
				t.Fatal(err)
			}
			want[body] = gr.nodes[i]
		}
	}
	gr.checkDelivered(want)
	auths := gr.authenticators()
	for i, a := range auths {
		if !bytes.Equal(a, auths[0]) {
			t.Fatalf("member %d has another epoch authenticator", i)
		}
	}
}

// forged is a gossip message as a hostile member could publish it: a body sealed under key for epoch, with the
// device and sequence the envelope would carry.
func forged(r *Room, key *[keySize]byte, epoch uint64, person ed25519.PublicKey, signer *koloid.Identity, seq uint64, c *content) *kologossip.Message {
	device := signer.PublicKey()
	nonce, ct, err := seal(key, aad(r.gid[:], epoch, device, seq), c.encode())
	if err != nil {
		panic(err)
	}
	s := &sealed{Epoch: epoch, Nonce: nonce, CT: ct}
	if err := signSealed(signer, r.gid[:], s, seq); err != nil {
		panic(err)
	}
	body := s.encode()
	return &kologossip.Message{ID: MessageID{byte(seq), byte(seq >> 8), 1}, Person: person, Device: device, Seq: seq, Time: time.Now(), Body: body}
}

func hello(r *Room) *content {
	return &content{Time: time.Now().UnixMilli(), Body: []byte("hello")}
}

func TestNonMemberRefusedAtAdmission(t *testing.T) {
	gr := newGroup(t, 3, Limits{})
	gr.waitMesh(2)
	outsider := newTNode(t, tnodeOpts{allow: allowAll})
	for i, n := range gr.nodes {
		if n.n.Allow(outsider.key(), netip4(outsider)) {
			t.Fatalf("member %d admits a device that is no member", i)
		}
		if !n.n.Allow(gr.nodes[(i+1)%3].key(), netip4(gr.nodes[(i+1)%3])) {
			t.Fatalf("member %d refuses a member", i)
		}
	}
	// A dial to a member ends without a gossip peer.
	topic := topicOf(gr.rooms[0].secret)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	before := gr.rooms[0].gr.Stats().Peers
	if err := outsider.sw.Join(ctx, topic, &koloswarm.JoinOptions{Lookup: true}); err != nil {
		t.Fatal(err)
	}
	outsider.sw.Connect(ctx, topic, gr.nodes[0].key(), addrOf(gr.nodes[0].sw.Addr()), netip4(gr.nodes[0]).Addr())
	waitFor(t, 5*time.Second, "the member's swarm to ask its admission rule about the outsider", func() bool { return gr.nodes[0].wasAsked(outsider.key()) })
	time.Sleep(300 * time.Millisecond)
	if got := gr.rooms[0].gr.Stats().Peers; got != before {
		t.Fatalf("the outsider became a gossip peer: %d peers, %d before", got, before)
	}
	// The roster refuses its messages whatever the connection.
	if _, err := (authors{gr.rooms[0]}).Lookup(topic, outsider.person.PublicKey(), outsider.key()); !errors.Is(err, kologossip.ErrUnknownAuthor) {
		t.Fatalf("Lookup of a non-member: %v", err)
	}
}

func TestServeConnRefusesNonMember(t *testing.T) {
	server := newServer(t)
	member := newTNode(t, tnodeOpts{server: server, allow: allowAll})
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	r, err := member.n.Create(ctx, "x", Policy{})
	if err != nil {
		t.Fatal(err)
	}
	outsider := newTNode(t, tnodeOpts{allow: allowAll, noServe: true})
	topic := topicOf(r.secret)
	cctx, ccancel := context.WithTimeout(ctx, 5*time.Second)
	defer ccancel()
	if err := outsider.sw.Join(cctx, topic, &koloswarm.JoinOptions{Lookup: true}); err != nil {
		t.Fatal(err)
	}
	// The member may close the connection before the outsider's own hello ends, which the swarm reports as a protocol error.
	err = outsider.sw.Connect(cctx, topic, member.key(), addrOf(member.sw.Addr()), netip.MustParseAddr("127.0.0.1"))
	if err != nil && !errors.Is(err, koloswarm.ErrProtocol) {
		t.Fatalf("the swarm of a device that admits everyone refused the dial: %v", err)
	}
	waitFor(t, 5*time.Second, "ServeConn to refuse the outsider", func() bool {
		err, ok := member.servedErr(outsider.key())
		return ok && errors.Is(err, ErrNotMember)
	})
	if r.gr.Stats().Peers != 0 {
		t.Fatal("the outsider became a gossip peer")
	}
}

func TestRemovedDeviceRefusedAfterSettledCommit(t *testing.T) {
	gr := newGroup(t, 3, Limits{PastEpoch: 300 * time.Millisecond})
	gr.waitMesh(2)
	a, b, c := gr.rooms[0], gr.rooms[1], gr.rooms[2]
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	e := a.Status().Epoch
	c.kmu.RLock()
	oldKey := c.keys[e]
	c.kmu.RUnlock()
	cPerson, cDev, cID := gr.nodes[2].person.PublicKey(), gr.nodes[2].key(), gr.nodes[2].dev
	if err := a.Remove(ctx, cPerson); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 30*time.Second, "b at the new epoch", func() bool { return b.Status().Epoch == e+1 })
	// Messages sealed in the old epoch still pass while its window lasts.
	if err := b.check(forged(b, &oldKey, e, cPerson, cID, 1, hello(b))); err != nil {
		t.Fatalf("a message of the previous epoch inside its window: %v", err)
	}
	waitFor(t, 30*time.Second, "c told it was removed", func() bool {
		for _, ev := range gr.recs[2].events() {
			if ev.Kind == Removed {
				return true
			}
		}
		return false
	})
	if _, err := c.Send(ctx, []byte("x"), MessageID{}); !errors.Is(err, ErrRemoved) {
		t.Fatalf("Send after removal: %v", err)
	}
	if _, err := c.Next(ctx); !errors.Is(err, ErrRemoved) {
		t.Fatalf("Next after removal: %v", err)
	}
	time.Sleep(400 * time.Millisecond)
	if err := b.check(forged(b, &oldKey, e, cPerson, cID, 2, hello(b))); !errors.Is(err, ErrWrongEpoch) {
		t.Fatalf("a message of the removed device after the window: %v", err)
	}
	if _, err := (authors{b}).Lookup(topicOf(b.secret), cPerson, cDev); !errors.Is(err, kologossip.ErrUnknownAuthor) {
		t.Fatalf("the removed device is still an author: %v", err)
	}
	for i, n := range gr.nodes[:2] {
		if n.n.Allow(cDev, netip.AddrPort{}) {
			t.Fatalf("member %d still admits the removed device", i)
		}
		if len(gr.rooms[i].Members()) != 2 {
			t.Fatalf("member %d lists %d members", i, len(gr.rooms[i].Members()))
		}
	}
	// Later messages are sealed under a key the removed device lacks.
	if _, err := b.Send(ctx, []byte("after the removal"), MessageID{}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 30*time.Second, "a to read b's message", func() bool { return gr.recs[0].has("after the removal") })
	v := b.view.Load()
	nonce, ct, _ := seal(&v.key, aad(b.gid[:], v.epoch, gr.nodes[1].key(), 7), hello(b).encode())
	if _, err := open(&oldKey, nonce, aad(b.gid[:], v.epoch, gr.nodes[1].key(), 7), ct); !errors.Is(err, ErrOpen) {
		t.Fatalf("the removed device opened a later message: %v", err)
	}
	time.Sleep(300 * time.Millisecond)
	if gr.recs[2].has("after the removal") {
		t.Fatal("the removed device received a message sent after its removal")
	}
	// A removed device that restarts has no room to reopen.
	gr.stop(2)
	gr.nodes[2].restart()
	if rooms := gr.nodes[2].n.Rooms(); len(rooms) != 0 {
		t.Fatalf("a removed device reopened %d rooms", len(rooms))
	}
}

// netip4 is the loopback address a test device listens on.
func netip4(n *tnode) netip.AddrPort { return addrOf(n.sw.Addr()) }

func allowAll(ed25519.PublicKey, netip.AddrPort) bool { return true }
