package room

import (
	"context"
	"crypto/ed25519"
	"fmt"
	"net"
	"net/netip"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	kolobraid "github.com/ciranda-dev/kolo-braid"
	kolodht "github.com/ciranda-dev/kolo-dht"
	kologossip "github.com/ciranda-dev/kolo-gossip"
	koloid "github.com/ciranda-dev/kolo-id"
	kolosecure "github.com/ciranda-dev/kolo-secure"
	kolostore "github.com/ciranda-dev/kolo-store"
	koloswarm "github.com/ciranda-dev/kolo-swarm"
	kolotransport "github.com/ciranda-dev/kolo-transport"

	"github.com/ciranda-dev/kolo-mls/backendstore"
)

// waitFor polls cond until it holds, failing the test after d.
func waitFor(t testing.TB, d time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out after %v waiting for %s", d, what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func ident(t testing.TB) *koloid.Identity {
	t.Helper()
	id, err := koloid.Generate()
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// newServer starts a kolo-dht server on loopback with per-IP limits raised: every test peer is 127.0.0.1.
func newServer(t testing.TB) *kolodht.Node {
	t.Helper()
	n, err := kolodht.New(context.Background(), "127.0.0.1:0", ident(t), &kolodht.Config{
		Server:               true,
		RequestTimeout:       300 * time.Millisecond,
		MaxRequestsPerIP:     5000,
		MaxRequestsPerPrefix: 10000,
		MaxRequests:          20000,
		MaxRecordsPerIP:      2048,
		MaxRecordsPerTopic:   2048, // a sixteenth of it may come from one /24, and every test device is on 127.0.0.0/24
		MaxRelays:            256,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { n.Close() })
	return n
}

func addrOf(a net.Addr) netip.AddrPort {
	ap := a.(*net.UDPAddr).AddrPort()
	return netip.AddrPortFrom(ap.Addr().Unmap(), ap.Port())
}

// braidLimits are the smallest pacing values kolo-braid allows, so tests settle in milliseconds.
func braidLimits() *kolobraid.Limits {
	return &kolobraid.Limits{MaxTail: 64, PulseGap: 10 * time.Millisecond, PivotWait: 100 * time.Millisecond, ConfirmWait: time.Second, AnnounceGap: 10 * time.Millisecond}
}

// tnode is a device on loopback: a swarm, a gossip and a room node on one device key.
type tnode struct {
	t      testing.TB
	person *koloid.Identity
	dev    *koloid.Identity
	cert   *koloid.DeviceCert
	sw     *koloswarm.Swarm
	g      *kologossip.Gossip
	n      *Node
	np     atomic.Pointer[Node]
	cancel context.CancelFunc
	wg     sync.WaitGroup

	askedMu sync.Mutex
	asked   map[[32]byte]bool
	served  map[[32]byte]error // the last ServeConn result per remote key
	conns   map[[32]byte]koloswarm.Conn
}

func (tn *tnode) conn(key ed25519.PublicKey) koloswarm.Conn {
	tn.askedMu.Lock()
	defer tn.askedMu.Unlock()
	return tn.conns[[32]byte(key)]
}

func (tn *tnode) servedErr(key ed25519.PublicKey) (error, bool) {
	tn.askedMu.Lock()
	defer tn.askedMu.Unlock()
	err, ok := tn.served[[32]byte(key)]
	return err, ok
}

// wasAsked reports whether this device's swarm asked its admission rule about key.
func (tn *tnode) wasAsked(key ed25519.PublicKey) bool {
	tn.askedMu.Lock()
	defer tn.askedMu.Unlock()
	return tn.asked[[32]byte(key)]
}

type tnodeOpts struct {
	server  *kolodht.Node
	person  *koloid.Identity
	limits  Limits
	allow   func(ed25519.PublicKey, netip.AddrPort) bool // replaces the node's own Allow
	noServe bool                                         // the test serves no connection: a device that is no member
}

func newTNode(t testing.TB, o tnodeOpts) *tnode {
	t.Helper()
	tn := &tnode{t: t, person: o.person, dev: ident(t), asked: map[[32]byte]bool{}, served: map[[32]byte]error{}, conns: map[[32]byte]koloswarm.Conn{}}
	if tn.person == nil {
		tn.person = ident(t)
	}
	offer, err := tn.person.OfferDeviceCert(tn.dev.PublicKey(), &koloid.IssueOptions{Serial: 1})
	if err != nil {
		t.Fatal(err)
	}
	if tn.cert, err = tn.dev.AcceptDeviceCert(offer, tn.person.PublicKey()); err != nil {
		t.Fatal(err)
	}
	sw, err := koloswarm.New(context.Background(), "127.0.0.1:0", tn.dev, &koloswarm.Config{
		DHT: &kolodht.Config{
			RequestTimeout: 300 * time.Millisecond,
			PunchTimeout:   time.Second,
			Secure:         &kolosecure.Config{Transport: &kolotransport.Config{MaxConnsPerIP: 256}},
		},
		MaxConnecting:      4,
		MaxHandshakes:      128,
		MaxHandshakesPerIP: 128,
		HelloTimeout:       2 * time.Second,
		LookupInterval:     time.Second,
		RetryMin:           100 * time.Millisecond,
		RetryMax:           time.Second,
		Allow: func(k ed25519.PublicKey, a netip.AddrPort) bool {
			tn.askedMu.Lock()
			tn.asked[[32]byte(k)] = true
			tn.askedMu.Unlock()
			if o.allow != nil {
				return o.allow(k, a)
			}
			n := tn.np.Load()
			return n != nil && n.Allow(k, a)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	tn.sw = sw
	if o.server != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := sw.Bootstrap(ctx, []netip.AddrPort{addrOf(o.server.Addr())}); err != nil {
			t.Fatal(err)
		}
	}
	if tn.g, err = kologossip.New(tn.dev, &kologossip.Config{Cert: tn.cert, Swarm: sw, HeartbeatInterval: 100 * time.Millisecond}); err != nil {
		t.Fatal(err)
	}
	mls := kolostore.NewMemory()
	tn.n, err = Open(context.Background(), &Config{Device: tn.dev, Cert: tn.cert, Swarm: sw, Gossip: tn.g, MLSStore: backendstore.New(mls, []byte("mls/")),
		Backend: kolostore.NewMemory(), BraidLimits: braidLimits(), Limits: o.limits})
	if err != nil {
		t.Fatal(err)
	}
	tn.np.Store(tn.n)
	ctx, cancel := context.WithCancel(context.Background())
	tn.cancel = cancel
	tn.wg.Go(func() {
		for {
			ev, err := sw.Next(ctx)
			if err != nil {
				return
			}
			if ev.Kind != koloswarm.Connected {
				continue
			}
			tn.askedMu.Lock()
			tn.conns[[32]byte(ev.Peer.PublicKey())] = ev.Peer.Conn()
			tn.askedMu.Unlock()
			if o.noServe {
				continue
			}
			tn.wg.Go(func() {
				err := tn.n.ServeConn(ctx, ev.Peer.Conn(), ev.Peer.Outbound())
				tn.askedMu.Lock()
				tn.served[[32]byte(ev.Peer.PublicKey())] = err
				tn.askedMu.Unlock()
			})
		}
	})
	t.Cleanup(tn.close)
	return tn
}

func (tn *tnode) close() {
	tn.n.Close()
	tn.cancel()
	tn.g.Close()
	tn.sw.Close()
	tn.wg.Wait()
}

func (tn *tnode) key() ed25519.PublicKey { return tn.dev.PublicKey() }

// recorder collects what a room delivers.
type recorder struct {
	r  *Room
	mu sync.Mutex
	ev []Event
}

func record(t testing.TB, r *Room) *recorder {
	rec := &recorder{r: r}
	go func() {
		for {
			ev, err := r.Next(context.Background())
			if err != nil {
				return
			}
			rec.mu.Lock()
			rec.ev = append(rec.ev, ev)
			rec.mu.Unlock()
		}
	}()
	return rec
}

func (rec *recorder) events() []Event {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	return append([]Event(nil), rec.ev...)
}

// messages returns the message events without an error, in delivery order.
func (rec *recorder) messages() []Event {
	var out []Event
	for _, ev := range rec.events() {
		if ev.Kind == Message && ev.Err == nil {
			out = append(out, ev)
		}
	}
	return out
}

func (rec *recorder) bodies() []string {
	var out []string
	for _, ev := range rec.messages() {
		out = append(out, string(ev.Body))
	}
	return out
}

func (rec *recorder) has(body string) bool {
	for _, ev := range rec.messages() {
		if string(ev.Body) == body {
			return true
		}
	}
	return false
}

func (rec *recorder) epoch() uint64 { return rec.r.Status().Epoch }

// group is a created room and the members that joined it.
type group struct {
	t      testing.TB
	server *kolodht.Node
	nodes  []*tnode
	rooms  []*Room
	recs   []*recorder
}

// newGroup creates a room on a new node and adds n-1 more nodes to it, one Add per node, and waits until every
// member holds the last epoch.
func newGroup(t testing.TB, n int, lim Limits) *group {
	t.Helper()
	return newGroupWith(t, n, lim, nil)
}

// newGroupWith is newGroup with mod applied to the options of the first node.
func newGroupWith(t testing.TB, n int, lim Limits, mod func(*tnodeOpts)) *group {
	t.Helper()
	server := newServer(t)
	gr := &group{t: t, server: server}
	fo := tnodeOpts{server: server, limits: lim}
	if mod != nil {
		mod(&fo)
	}
	first := newTNode(t, fo)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	r, err := first.n.Create(ctx, "test", Policy{})
	if err != nil {
		t.Fatal(err)
	}
	gr.nodes, gr.rooms, gr.recs = []*tnode{first}, []*Room{r}, []*recorder{record(t, r)}
	for i := 1; i < n; i++ {
		nd := newTNode(t, tnodeOpts{server: server, limits: lim})
		gr.join(ctx, nd)
	}
	return gr
}

// join adds nd to the room with an Add by the first node and waits for every member to reach the new epoch.
func (gr *group) join(ctx context.Context, nd *tnode) {
	gr.t.Helper()
	kp, err := nd.n.KeyPackage()
	if err != nil {
		gr.t.Fatal(err)
	}
	w, err := gr.rooms[0].Add(ctx, kp)
	if err != nil {
		gr.t.Fatal(err)
	}
	r, err := nd.n.Join(ctx, w)
	if err != nil {
		gr.t.Fatal(err)
	}
	gr.nodes, gr.rooms, gr.recs = append(gr.nodes, nd), append(gr.rooms, r), append(gr.recs, record(gr.t, r))
	gr.waitEpoch(gr.rooms[0].Status().Epoch)
}

// joinAll adds the nodes to the room with Adds of at most batch key packages each.
func (gr *group) joinAll(ctx context.Context, nds []*tnode, batch int) {
	gr.t.Helper()
	for len(nds) > 0 {
		k := min(batch, len(nds))
		gr.joinBatch(ctx, nds[:k])
		nds = nds[k:]
		// A Commit sent before its receiver's mesh links are grafted can miss it, and nothing repairs that until catch-up is built.
		gr.waitMesh(min(4, len(gr.rooms)-1))
	}
}

func (gr *group) joinBatch(ctx context.Context, nds []*tnode) {
	gr.t.Helper()
	var kps [][]byte
	for _, nd := range nds {
		kp, err := nd.n.KeyPackage()
		if err != nil {
			gr.t.Fatal(err)
		}
		kps = append(kps, kp)
	}
	w, err := gr.rooms[0].Add(ctx, kps...)
	if err != nil {
		gr.t.Fatal(err)
	}
	for _, nd := range nds {
		r, err := nd.n.Join(ctx, w)
		if err != nil {
			gr.t.Fatal(err)
		}
		gr.nodes, gr.rooms, gr.recs = append(gr.nodes, nd), append(gr.rooms, r), append(gr.recs, record(gr.t, r))
	}
	gr.waitEpoch(gr.rooms[0].Status().Epoch)
}

func (gr *group) waitEpoch(e uint64) {
	gr.t.Helper()
	waitFor(gr.t, 60*time.Second, fmt.Sprintf("every member at epoch %d", e), func() bool {
		for _, r := range gr.rooms {
			if !r.removed.Load() && r.Status().Epoch != e {
				return false
			}
		}
		return true
	})
}

// waitMesh waits until every member has min peers in its gossip mesh: a message sent before a mesh link is grafted can
// miss its peer, which gossip repairs only for a short while (kolo-gossip guide, Publishing and reading).
func (gr *group) waitMesh(min int) {
	gr.t.Helper()
	waitFor(gr.t, 60*time.Second, "the gossip mesh", func() bool {
		for _, r := range gr.rooms {
			if !r.removed.Load() && r.gr.Stats().Mesh < min {
				return false
			}
		}
		return true
	})
}
