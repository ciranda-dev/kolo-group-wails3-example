package room

import (
	"context"
	"errors"
	"testing"
	"time"

	kolostore "github.com/ciranda-dev/kolo-store"

	kolomls "github.com/ciranda-dev/kolo-mls"
	"github.com/ciranda-dev/kolo-mls/settle"
)

func TestOpenRefusesWhatItCannotRun(t *testing.T) {
	tn := newTNode(t, tnodeOpts{})
	ctx := context.Background()
	good := Config{Device: tn.dev, Cert: tn.cert, Gossip: tn.g, MLSStore: tn.n.cfg.MLSStore, Backend: kolostore.NewMemory()}
	for name, mod := range map[string]func(*Config){
		"no device":    func(c *Config) { c.Device = nil },
		"no gossip":    func(c *Config) { c.Gossip = nil },
		"no MLS store": func(c *Config) { c.MLSStore = nil },
		"no backend":   func(c *Config) { c.Backend = nil },
		"another cert": func(c *Config) { c.Device = ident(t) },
		"a bad limit":  func(c *Config) { c.Limits.Rate = -1 },
	} {
		c := good
		mod(&c)
		if _, err := Open(ctx, &c); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := Open(ctx, nil); !errors.Is(err, ErrInvalid) {
		t.Errorf("nil config: %v", err)
	}
	l, err := Limits{}.withDefaults()
	if err != nil || l.MaxMessageSize != 1<<20 || l.Rate != 10 || l.Burst != 10 || l.ParentHold != 5*time.Second || l.PastEpoch != 10*time.Second || l.MaxHeld != 8333 || l.MaxKnown != 200000 || l.EventQueue != 1024 {
		t.Fatalf("defaults %+v %v", l, err)
	}
}

func TestOperationsRefuseWhatTheyCannotDo(t *testing.T) {
	gr := newGroup(t, 3, Limits{})
	gr.waitMesh(2)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	a, b := gr.rooms[0], gr.rooms[1]
	if _, err := a.Add(ctx); !errors.Is(err, ErrInvalid) {
		t.Errorf("Add of nothing: %v", err)
	}
	kp, _ := gr.nodes[1].n.KeyPackage()
	if _, err := b.Add(ctx, kp); !errors.Is(err, ErrNotAdmin) {
		t.Errorf("Add by a member that is no admin: %v", err)
	}
	if err := b.Remove(ctx, gr.nodes[2].person.PublicKey()); !errors.Is(err, ErrNotAdmin) {
		t.Errorf("Remove by a member that is no admin: %v", err)
	}
	if err := a.Remove(ctx, ident(t).PublicKey()); !errors.Is(err, ErrNotMember) {
		t.Errorf("Remove of a stranger: %v", err)
	}
	// A Commit that removes its own committer is refused, and the refusal leaves the room usable.
	if err := a.Remove(ctx, gr.nodes[0].person.PublicKey()); !errors.Is(err, kolomls.ErrInvalidCommit) {
		t.Errorf("Remove of the admin: %v", err)
	}
	if _, err := a.Send(ctx, []byte("still works"), MessageID{}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 20*time.Second, "the message", func() bool { return gr.recs[2].has("still works") })
	if _, err := gr.nodes[0].n.Create(ctx, "x", Policy{History: NoHistory}); !errors.Is(err, ErrNotImplemented) {
		t.Errorf("Create with a policy that is not built: %v", err)
	}
	if _, err := gr.nodes[0].n.Create(ctx, string(make([]byte, maxName+1)), Policy{}); !errors.Is(err, ErrInvalid) {
		t.Errorf("Create with a long name: %v", err)
	}
	for _, err := range []error{a.Promote(ctx, nil, Admin), a.SetPolicy(ctx, Policy{}), a.Leave(ctx)} {
		if !errors.Is(err, ErrNotImplemented) {
			t.Errorf("an unbuilt method: %v", err)
		}
	}
	if _, err := a.Link(); !errors.Is(err, ErrNotImplemented) {
		t.Errorf("Link: %v", err)
	}
	if _, err := a.History(ctx, MessageID{}, 1); !errors.Is(err, ErrNotImplemented) {
		t.Errorf("History: %v", err)
	}
	if _, err := gr.nodes[0].n.JoinLink(ctx, nil); !errors.Is(err, ErrNotImplemented) {
		t.Errorf("JoinLink: %v", err)
	}
}

func TestJoinRefusesABadOrUsedWelcome(t *testing.T) {
	gr := newGroup(t, 2, Limits{})
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	nd := newTNode(t, tnodeOpts{server: gr.server})
	kp, _ := nd.n.KeyPackage()
	w, err := gr.rooms[0].Add(ctx, kp)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := nd.n.Join(ctx, w[:len(w)/2]); !errors.Is(err, ErrMalformed) {
		t.Fatalf("a cut welcome: %v", err)
	}
	if _, err := nd.n.Join(ctx, nil); !errors.Is(err, ErrMalformed) {
		t.Fatalf("no welcome: %v", err)
	}
	other := newTNode(t, tnodeOpts{server: gr.server})
	if _, err := other.n.Join(ctx, w); err == nil {
		t.Fatal("a device joined with a welcome made for another device")
	}
	r, err := nd.n.Join(ctx, w)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := nd.n.Room(r.ID()); err != nil || got != r || len(nd.n.Rooms()) != 1 {
		t.Fatalf("Room: %v", err)
	}
	if _, err := nd.n.Room(GroupID{1}); !errors.Is(err, ErrUnknownRoom) {
		t.Fatalf("an unknown room: %v", err)
	}
	if _, err := nd.n.Join(ctx, w); err == nil {
		t.Fatal("a welcome was used twice")
	}
	if r.Name() != "test" || r.Status().Epoch != gr.rooms[0].Status().Epoch {
		t.Fatalf("name %q epoch %d", r.Name(), r.Status().Epoch)
	}
}

func TestClosedRoomEndsNext(t *testing.T) {
	gr := newGroup(t, 2, Limits{})
	r := gr.rooms[1]
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	r.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := r.Next(ctx); !errors.Is(err, ErrClosed) {
		t.Fatalf("Next after Close: %v", err)
	}
	if _, err := r.Send(ctx, []byte("x"), MessageID{}); !errors.Is(err, ErrClosed) {
		t.Fatalf("Send after Close: %v", err)
	}
	if len(gr.nodes[1].n.Rooms()) != 0 || gr.nodes[1].n.Allow(gr.nodes[0].key(), netip4(gr.nodes[0])) {
		t.Fatal("a closed room still admits its members")
	}
}

func TestPinsBoundWhatTheyReturn(t *testing.T) {
	p := pins{kolostore.NewMemory()}
	ctx := context.Background()
	for _, k := range []string{"a/1", "a/2", "a/3", "b/1"} {
		if err := p.Put(ctx, k, []byte("value")); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := p.Get(ctx, "nope", 0); !errors.Is(err, settle.ErrMissing) {
		t.Fatalf("a missing key: %v", err)
	}
	if _, err := p.Get(ctx, "a/1", 3); !errors.Is(err, settle.ErrPinTooLarge) {
		t.Fatalf("a value past the bound: %v", err)
	}
	if ks, err := p.List(ctx, "a/", 3); err != nil || len(ks) != 3 {
		t.Fatalf("List: %v %v", ks, err)
	}
	if _, err := p.List(ctx, "a/", 2); !errors.Is(err, settle.ErrPinTooLarge) {
		t.Fatalf("List past the bound: %v", err)
	}
}

// The roster reuses the Author of a device whose credential did not change, so gossip finds its chain check cached.
func TestAuthorsAreReusedAcrossEpochs(t *testing.T) {
	gr := newGroup(t, 2, Limits{})
	r := gr.rooms[0]
	before := r.view.Load().author([32]byte(gr.nodes[1].key()))
	nd := newTNode(t, tnodeOpts{server: gr.server})
	gr.join(context.Background(), nd)
	after := r.view.Load().author([32]byte(gr.nodes[1].key()))
	if before == nil || before != after {
		t.Fatal("the Author of an unchanged device was rebuilt")
	}
}
