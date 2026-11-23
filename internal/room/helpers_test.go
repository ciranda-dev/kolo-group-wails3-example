package room

import (
	"context"
	"crypto/ed25519"
	"fmt"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	kolobraid "github.com/ciranda-dev/kolo-braid"
	kolodht "github.com/ciranda-dev/kolo-dht"
	kologossip "github.com/ciranda-dev/kolo-gossip"
	koloid "github.com/ciranda-dev/kolo-id"
	kololog "github.com/ciranda-dev/kolo-log"
	"github.com/ciranda-dev/kolo-pin"
	kolosecure "github.com/ciranda-dev/kolo-secure"
	kolostore "github.com/ciranda-dev/kolo-store"
	koloswarm "github.com/ciranda-dev/kolo-swarm"
	kolotransport "github.com/ciranda-dev/kolo-transport"
	"github.com/prometheus/client_golang/prometheus"

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

	opts    tnodeOpts
	mls     kolostore.Backend // what a restart keeps: the MLS state, the room's state and the logs
	backend kolostore.Backend
	logs    kolostore.Backend // the backend of store, in memory
	store   *kolostore.Store

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
	pins    []pin.Peer
	dir     string // keeps the device's state in Pebble stores under this directory instead of in memory
	limits  Limits
	allow   func(ed25519.PublicKey, netip.AddrPort) bool // replaces the node's own Allow
	noServe bool                                         // the test serves no connection: a device that is no member
	policy  Policy                                       // the policy a created room gets
	serve   bool                                         // Config.ServeTree
	// beforeOpen, when set, replaces the bootstrap of a start: it gets the swarm before the room node opens.
	beforeOpen func(*koloswarm.Swarm)
}

func newTNode(t testing.TB, o tnodeOpts) *tnode {
	t.Helper()
	tn := &tnode{t: t, opts: o, person: o.person, dev: ident(t), mls: kolostore.NewMemory(), backend: kolostore.NewMemory(), logs: kolostore.NewMemory()}
	tn.store = kolostore.New(tn.logs)
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
	tn.start()
	t.Cleanup(tn.close)
	return tn
}

// start runs the swarm, gossip and room node of the device over the state it keeps, which is empty for a new device and
// what the last run left after a restart.
func (tn *tnode) start() {
	t, o := tn.t, tn.opts
	if o.dir != "" {
		open := func(name string) kolostore.Backend {
			b, err := kolostore.OpenPebble(filepath.Join(o.dir, name))
			if err != nil {
				t.Fatal(err)
			}
			return b
		}
		tn.mls, tn.backend, tn.store = open("mls"), open("state"), kolostore.New(open("logs"))
	}
	tn.asked, tn.served, tn.conns = map[[32]byte]bool{}, map[[32]byte]error{}, map[[32]byte]koloswarm.Conn{}
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
	if o.beforeOpen != nil {
		o.beforeOpen(sw)
	} else if o.server != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := sw.Bootstrap(ctx, []netip.AddrPort{addrOf(o.server.Addr())}); err != nil {
			t.Fatal(err)
		}
	}
	if tn.g, err = kologossip.New(tn.dev, &kologossip.Config{Cert: tn.cert, Swarm: sw, HeartbeatInterval: 100 * time.Millisecond}); err != nil {
		t.Fatal(err)
	}
	tn.np.Store(nil)
	tn.n, err = Open(context.Background(), &Config{Device: tn.dev, Cert: tn.cert, Swarm: sw, Gossip: tn.g, MLSStore: backendstore.New(tn.mls, []byte("mls/")),
		Backend: tn.backend, Store: tn.store, Pins: o.pins, BraidLimits: braidLimits(), Limits: o.limits, ServeTree: o.serve})
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
}

// restart stops the device and starts it again over the state it kept, as a process that was killed and run again.
func (tn *tnode) restart() {
	tn.stop()
	tn.start()
}

// stop ends the device and keeps its state.
func (tn *tnode) stop() {
	tn.n.Close()
	tn.cancel()
	tn.g.Close()
	tn.sw.Close()
	tn.wg.Wait()
	if tn.opts.dir != "" {
		tn.store.Close()
		tn.mls.Close()
		tn.backend.Close()
	}
}

func (tn *tnode) close() {
	tn.stop()
	tn.store.Close()
}

func (tn *tnode) key() ed25519.PublicKey { return tn.dev.PublicKey() }

// recorder collects what a room delivers. A light recorder keeps the events with an error and counts the messages.
type recorder struct {
	r     *Room
	light bool
	count atomic.Int64
	mu    sync.Mutex
	ev    []Event
}

func record(t testing.TB, r *Room) *recorder { return recordWith(r, false) }

func recordWith(r *Room, light bool) *recorder {
	rec := &recorder{r: r, light: light}
	go func() {
		for {
			ev, err := r.Next(context.Background())
			if err != nil {
				return
			}
			if light {
				if ev.Err == nil && ev.Kind == Message {
					rec.count.Add(1)
					continue
				}
				if ev.Err == nil {
					continue
				}
			}
			rec.mu.Lock()
			rec.ev = append(rec.ev, ev)
			rec.mu.Unlock()
		}
	}()
	return rec
}

// bodies set returns the distinct bodies of the messages shown without an error.
func (rec *recorder) bodySet() map[string]struct{} {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	out := map[string]struct{}{}
	for _, ev := range rec.ev {
		if ev.Kind == Message && ev.Err == nil {
			out[string(ev.Body)] = struct{}{}
		}
	}
	return out
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
	away   map[int]bool // members that are stopped; the waits skip them
	light  bool         // the recorders of members that join count messages and keep nothing else
	keep   map[int]bool // members whose recorders keep every event even then
	lim    Limits
	pins   []pin.Peer
	serve  bool // the members that join serve trees; the first device is the admin
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
	return newGroupPins(t, n, lim, nil, mod)
}

// newGroupPins is newGroupWith for devices that use the given pins.
func newGroupPins(t testing.TB, n int, lim Limits, pins []pin.Peer, mod func(*tnodeOpts)) *group {
	t.Helper()
	return newGroupServe(t, n, lim, pins, false, mod)
}

// newGroupServe is newGroupPins for members that serve trees when serve is set.
func newGroupServe(t testing.TB, n int, lim Limits, pins []pin.Peer, serve bool, mod func(*tnodeOpts)) *group {
	t.Helper()
	server := newServer(t)
	gr := &group{t: t, server: server, lim: lim, pins: pins, serve: serve}
	fo := tnodeOpts{server: server, limits: lim, pins: pins}
	if mod != nil {
		mod(&fo)
	}
	first := newTNode(t, fo)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	r, err := first.n.Create(ctx, "test", fo.policy)
	if err != nil {
		t.Fatal(err)
	}
	gr.nodes, gr.rooms, gr.recs = []*tnode{first}, []*Room{r}, []*recorder{record(t, r)}
	for i := 1; i < n; i++ {
		gr.join(ctx, gr.newNode())
	}
	return gr
}

// newNode makes a device that can join the group: it uses the group's server, limits and pins.
func (gr *group) newNode() *tnode {
	return newTNode(gr.t, tnodeOpts{server: gr.server, limits: gr.lim, pins: gr.pins, serve: gr.serve})
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
	gr.nodes, gr.rooms, gr.recs = append(gr.nodes, nd), append(gr.rooms, r), append(gr.recs, recordWith(r, gr.light))
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
	// Each join pulls its tree from the devices a lookup finds, so they run together.
	joined := make([]*Room, len(nds))
	errs := make([]error, len(nds))
	var wg sync.WaitGroup
	for i, nd := range nds {
		wg.Go(func() { joined[i], errs[i] = nd.n.Join(ctx, w) })
	}
	wg.Wait()
	for i, nd := range nds {
		if errs[i] != nil {
			gr.t.Fatal(errs[i])
		}
		gr.nodes, gr.rooms, gr.recs = append(gr.nodes, nd), append(gr.rooms, joined[i]), append(gr.recs, recordWith(joined[i], gr.light))
	}
	gr.waitEpoch(gr.rooms[0].Status().Epoch)
}

func (gr *group) waitEpoch(e uint64) {
	gr.t.Helper()
	waitFor(gr.t, 60*time.Second, fmt.Sprintf("every member at epoch %d", e), func() bool {
		for i, r := range gr.rooms {
			if !gr.away[i] && !r.removed.Load() && r.Status().Epoch != e {
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
		for i, r := range gr.rooms {
			if !gr.away[i] && !r.removed.Load() && r.gr.Stats().Mesh < min {
				return false
			}
		}
		return true
	})
}

// restart stops member i and starts it again over its stored state, and returns its reopened room.
func (gr *group) restart(i int) *Room {
	gr.t.Helper()
	gr.nodes[i].restart()
	delete(gr.away, i)
	rooms := gr.nodes[i].n.Rooms()
	if len(rooms) != 1 {
		gr.t.Fatalf("member %d reopened %d rooms", i, len(rooms))
	}
	gr.rooms[i], gr.recs[i] = rooms[0], recordWith(rooms[0], gr.light && !gr.keep[i])
	return rooms[0]
}

// stop ends member i and keeps its state.
func (gr *group) stop(i int) {
	if gr.away == nil {
		gr.away = map[int]bool{}
	}
	gr.away[i] = true
	gr.nodes[i].stop()
}

// pinServer is a pinning peer on 127.0.0.1 with its data in a temporary directory.
type pinServer struct {
	peer pin.Peer
	stop context.CancelFunc
	done chan error
	reg  *prometheus.Registry
}

// metric returns the sum of the counters named name whose op label is op ("" for any).
func (ps *pinServer) metric(name, op string) (sum float64) {
	mfs, _ := ps.reg.Gather()
	for _, mf := range mfs {
		if mf.GetName() != name {
			continue
		}
		for _, m := range mf.GetMetric() {
			ok := op == ""
			for _, l := range m.GetLabel() {
				ok = ok || l.GetName() == "op" && l.GetValue() == op
			}
			if ok {
				sum += m.GetCounter().GetValue()
			}
		}
	}
	return sum
}

// served is the bytes the pin sent to peers pulling its logs.
func (ps *pinServer) served() uint64 { return uint64(ps.metric("kolo_pin_bytes_served_total", "")) }

func newPin(t testing.TB) *pinServer {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "pin")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	// pin.Serve loads the identity of its directory, so the listener must use that one.
	id, err := pin.LoadIdentity(dir)
	if err != nil {
		t.Fatal(err)
	}
	ln, err := kolosecure.Listen(context.Background(), "127.0.0.1:0", id, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	ps := &pinServer{peer: pin.Peer{ID: id.PublicKey(), Addr: ln.Addr().String()}, stop: cancel, done: make(chan error, 1), reg: prometheus.NewRegistry()}
	go func() { ps.done <- pin.Serve(ctx, ln, pin.Options{Dir: dir, Metrics: ps.reg}) }()
	t.Cleanup(func() { ps.down() })
	return ps
}

// down stops the pinning peer.
func (ps *pinServer) down() {
	ps.stop()
	select {
	case <-ps.done:
	case <-time.After(10 * time.Second):
	}
}

// pinsHold waits until every pin holds at least entries entries of the log of owner.
func pinsHold(t testing.TB, peers []pin.Peer, owner *koloid.Identity, logID kololog.LogID, entries uint64) {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for {
		last := ""
		for i, p := range peers {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			info, err := pin.Status(ctx, owner, p, logID)
			cancel()
			if err != nil || info.Entries < entries {
				last = fmt.Sprintf("pin %d holds %d of %d entries of log %x: %v", i, info.Entries, entries, logID[:3], err)
				break
			}
		}
		if last == "" {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal(last)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// waitPeers waits until every running member is connected to at least n gossip peers.
func (gr *group) waitPeers(n int) {
	gr.t.Helper()
	waitFor(gr.t, 60*time.Second, "the gossip peers", func() bool {
		for i, r := range gr.rooms {
			if !gr.away[i] && !r.removed.Load() && r.gr.Stats().Peers < n {
				return false
			}
		}
		return true
	})
}
