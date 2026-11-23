package room

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	kololog "github.com/ciranda-dev/kolo-log"
	kolosecure "github.com/ciranda-dev/kolo-secure"
	kolostore "github.com/ciranda-dev/kolo-store"
	kolosync "github.com/ciranda-dev/kolo-sync"

	kologossip "github.com/ciranda-dev/kolo-gossip"
	kolomls "github.com/ciranda-dev/kolo-mls"
	"github.com/ciranda-dev/kolo-mls/cred"
	"github.com/ciranda-dev/kolo-mls/settle"
	koloswarm "github.com/ciranda-dev/kolo-swarm"
)

// Node holds the rooms of one device.
type Node struct {
	cfg    Config
	limits Limits
	now    func() time.Time
	sync   *kolosync.Server

	logRoom sync.Map // logKey to the *Room that offers the log

	mu      sync.Mutex
	rooms   map[GroupID]*Room
	conns   map[[32]byte][]koloswarm.Conn
	links   map[*connState]struct{}
	members atomic.Pointer[map[[32]byte]struct{}]
	open    atomic.Bool  // a room has a read link or is one: any device is admitted
	joining atomic.Int32 // joins that are fetching their tree: any device is admitted, since nothing tells a joiner who serves it
	closed  bool
}

// connState is a connection being served and the rooms whose parts of it run.
type connState struct {
	key    [32]byte
	mux    *mux
	ctx    context.Context
	wg     sync.WaitGroup
	synced map[*Room]bool
	braids map[*Room]bool
	done   bool
}

// Open starts a node.
func Open(ctx context.Context, cfg *Config) (*Node, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if cfg == nil || cfg.Device == nil || cfg.Cert == nil || cfg.Gossip == nil || cfg.MLSStore == nil || cfg.Backend == nil || cfg.Store == nil {
		return nil, errInvalid("a node needs a device, its certificate, a gossip, an MLS store, a backend and a store")
	}
	if string(cfg.Cert.Device()) != string(cfg.Device.PublicKey()) {
		return nil, errInvalid("the certificate is for another device")
	}
	lim, err := cfg.Limits.withDefaults()
	if err != nil {
		return nil, err
	}
	n := &Node{cfg: *cfg, limits: lim, now: cfg.Now, rooms: map[GroupID]*Room{}, conns: map[[32]byte][]koloswarm.Conn{}, links: map[*connState]struct{}{}}
	if n.now == nil {
		n.now = time.Now
	}
	n.members.Store(&map[[32]byte]struct{}{})
	n.sync = kolosync.NewServer(func(peer, owner ed25519.PublicKey, id kololog.LogID) kolosync.Access {
		if len(owner) != ed25519.PublicKeySize || len(peer) != ed25519.PublicKeySize {
			return kolosync.NoAccess
		}
		if r, ok := n.logRoom.Load(keyOf([32]byte(owner), id)); ok && r.(*Room).readableBy([32]byte(peer)) {
			return kolosync.Ciphertext
		}
		return kolosync.NoAccess
	})
	if err := n.reopen(ctx); err != nil {
		n.Close()
		return nil, err
	}
	return n, nil
}

// reopen opens the rooms the backend holds, so a restart finds them as they were.
func (n *Node) reopen(ctx context.Context) error {
	type found struct {
		gid GroupID
		mt  *meta
	}
	var rooms []found
	var derr error
	err := n.cfg.Backend.Scan([]byte("g/"), []byte("g0"), false, 0, func(k, v []byte) bool {
		if len(k) != 2+64+2 || k[len(k)-2] != '/' || k[len(k)-1] != 'm' {
			return true
		}
		gid, err := hex.DecodeString(string(k[2 : 2+64]))
		if err != nil {
			return true
		}
		mt, err := decodeMeta(v)
		if err != nil {
			derr = err
			return false
		}
		rooms = append(rooms, found{GroupID(gid), mt})
		return true
	})
	if err = errors.Join(err, derr); err != nil {
		return err
	}
	for _, f := range rooms {
		m, err := settle.OpenMember(n.cfg.MLSStore, f.gid[:], n.mlsConfig(), settle.MemberOptions{})
		if err != nil {
			return fmt.Errorf("room: reopening %x: %w", f.gid[:4], err)
		}
		r, err := n.newRoom(ctx, f.mt.Name, m, f.mt.Secret, f.mt, nil)
		if err != nil {
			m.Close()
			return err
		}
		if f.mt.Braid {
			b, order, err := openBraid(ctx, n.cfg.Device, n.cfg.Backend, []byte(f.mt.Prefix+"b/"), n.cfg.BraidLimits)
			if err != nil {
				r.Close()
				return err
			}
			if !r.view.Load().roles.Admin(n.cfg.Device.PublicKey()) {
				r.holdBraid(b, order, f.mt.Prefix)
				continue
			}
			if err := r.startAdmin(b, order); err != nil {
				b.Close()
				r.Close()
				return err
			}
		}
	}
	return nil
}

func (n *Node) isMember(key []byte) bool {
	if len(key) != ed25519.PublicKeySize {
		return false
	}
	_, ok := (*n.members.Load())[[32]byte(key)]
	return ok || n.open.Load() || n.joining.Load() > 0
}

// readableBy reports whether a device may read the room's logs: a member, a device removed by the last Commit while its window
// lasts, since it pulls that Commit from the logs, or anyone when the room has a read link.
func (r *Room) readableBy(dev [32]byte) bool {
	v := r.view.Load()
	return r.reader || v.pol.link || v.author(dev) != nil || v.prev != nil && r.n.now().Before(v.prevUntil) && v.prev.author(dev) != nil
}

// Allow is the admission rule for koloswarm.Config.Allow: only devices that are leaves of a room of this node, and
// that gossip has not graylisted. A node that has a room with a read link, or reads one, admits any device.
func (n *Node) Allow(key ed25519.PublicKey, addr netip.AddrPort) bool {
	return n.isMember(key) && n.cfg.Gossip.Allow(key, addr)
}

// ServeConn serves one connection: gossip on its unidirectional streams, kolo-sync on its bidirectional ones and, for an
// admin, the braid on both, each behind a tag byte. A device that is no member of any room is refused.
func (n *Node) ServeConn(ctx context.Context, conn koloswarm.Conn, outbound bool) error {
	key := conn.RemotePublicKey()
	if !n.isMember(key) {
		conn.CloseWithError(0, "room: not a member device")
		return ErrNotMember
	}
	k := [32]byte(key)
	sctx, cancel := context.WithCancel(ctx)
	cs := &connState{key: k, mux: newMux(conn), ctx: sctx, synced: map[*Room]bool{}, braids: map[*Room]bool{}}
	n.mu.Lock()
	n.conns[k] = append(n.conns[k], conn)
	n.links[cs] = struct{}{}
	n.mu.Unlock()
	defer func() {
		n.mu.Lock()
		n.conns[k] = slices.DeleteFunc(n.conns[k], func(c koloswarm.Conn) bool { return c == conn })
		if len(n.conns[k]) == 0 {
			delete(n.conns, k)
		}
		delete(n.links, cs)
		cs.done = true
		n.mu.Unlock()
	}()
	cs.mux.run()
	cs.wg.Go(func() { n.sync.Serve(sctx, cs.mux.sync) })
	n.attachAll()
	err := n.cfg.Gossip.ServeConn(ctx, cs.mux.gossip, outbound)
	cancel()
	cs.mux.stop()
	n.mu.Lock()
	cs.done = true
	n.mu.Unlock()
	cs.wg.Wait()
	return err
}

// attachAll starts the parts of every connection that a room needs and has not got: the follower on the connections of its
// members, and the braid on those of its members when it has one.
func (n *Node) attachAll() {
	n.mu.Lock()
	defer n.mu.Unlock()
	for cs := range n.links {
		if cs.done {
			continue
		}
		for _, r := range n.rooms {
			if r.removed.Load() || !r.reader && r.view.Load().author(cs.key) == nil {
				continue
			}
			if !cs.synced[r] {
				cs.synced[r] = true
				cs.wg.Go(func() { r.fol.Follow(cs.ctx, cs.mux.sync) })
			}
			if b := r.bp.Load(); b != nil && !cs.braids[r] {
				cs.braids[r] = true
				cs.wg.Go(func() { b.ServeConn(cs.ctx, cs.mux.braid) })
			}
		}
	}
}

func (n *Node) syncConn(key ed25519.PublicKey) kolosync.Conn {
	n.mu.Lock()
	defer n.mu.Unlock()
	for cs := range n.links {
		if cs.key == [32]byte(key) && !cs.done {
			return cs.mux.sync
		}
	}
	return nil
}

// refreshMembers recomputes the admitted devices from the rooms, closes the connections of those that left and starts the
// parts of the rooms on the connections of those that joined.
func (n *Node) refreshMembers() {
	n.mu.Lock()
	set := map[[32]byte]struct{}{}
	open := false
	for _, r := range n.rooms {
		if r.removed.Load() {
			continue
		}
		if v := r.view.Load(); v != nil {
			open = open || r.reader || v.pol.link
			for d := range v.authors {
				set[d] = struct{}{}
			}
		}
	}
	n.members.Store(&set)
	n.open.Store(open)
	for k, cs := range n.conns {
		if _, ok := set[k]; !ok && !open {
			for _, c := range cs {
				n.closeLater(k, c)
			}
		}
	}
	n.mu.Unlock()
	n.attachAll()
}

// closeLater closes the connection of a device that is no member any more after PastEpoch: the Commit that removed it is on
// its way over the connection, and a device that never receives it never learns that it was removed. The device is refused
// at once for any new connection.
func (n *Node) closeLater(k [32]byte, c koloswarm.Conn) {
	time.AfterFunc(n.limits.PastEpoch, func() {
		if !n.isMember(k[:]) {
			c.CloseWithError(0, "room: no longer a member device")
		}
	})
}

func (n *Node) mlsConfig() kolomls.Config {
	return settle.NewConfig(&cred.Authenticator{Now: n.now}, &settle.Policy{}, n.now)
}

// KeyPackage returns a key package that an admin can add to a room; each is used once.
func (n *Node) KeyPackage() ([]byte, error) {
	_, sig, err := ed25519.GenerateKey(nil)
	if err != nil {
		return nil, err
	}
	c, err := cred.New(n.cfg.Device, n.cfg.Cert, sig.Public().(ed25519.PublicKey))
	if err != nil {
		return nil, err
	}
	caps := settle.Capabilities()
	caps.Extensions = append(caps.Extensions, policyExtension)
	return kolomls.NewKeyPackage(n.cfg.MLSStore, kolomls.KeyPackageOptions{Credential: c, SignatureKey: sig, Capabilities: caps})
}

// Create makes a room with this device as its first member, admin and settler. The policy p is recorded in the room's
// GroupContext.
func (n *Node) Create(ctx context.Context, name string, p Policy) (*Room, error) {
	hp, err := p.build(nil)
	if err != nil {
		return nil, err
	}
	if len(name) > maxName {
		return nil, errInvalid("a name of %d bytes", len(name))
	}
	var pre [8]byte
	if _, err := rand.Read(pre[:]); err != nil {
		return nil, err
	}
	prefix := "room/" + hex.EncodeToString(pre[:]) + "/"
	b, order, err := newBraid(ctx, n.cfg.Device, n.cfg.Backend, []byte(prefix+"b/"), n.cfg.BraidLimits)
	if err != nil {
		return nil, err
	}
	fail := func(err error) (*Room, error) {
		b.Close()
		return nil, err
	}
	me := n.cfg.Device.PublicKey()
	roles, err := settle.NewRoles(order.ID(), settle.Majority, []ed25519.PublicKey{me}, []ed25519.PublicKey{me}, settle.Limits{})
	if err != nil {
		return fail(err)
	}
	exts, err := groupExtensions(roles, hp)
	if err != nil {
		return fail(err)
	}
	kp, err := n.KeyPackage()
	if err != nil {
		return fail(err)
	}
	m, err := settle.CreateMember(n.cfg.MLSStore, kolomls.CreateOptions{Config: n.mlsConfig(), KeyPackage: kp, Extensions: exts}, settle.MemberOptions{})
	if err != nil {
		return fail(err)
	}
	secret, err := m.Export(topicLabel, m.GroupID(), keySize)
	if err != nil {
		m.Close()
		return fail(err)
	}
	r, err := n.newRoom(ctx, name, m, [keySize]byte(secret), &meta{Name: name, Secret: [keySize]byte(secret), Prefix: prefix, JoinEpoch: m.Epoch(), Braid: true}, nil)
	if err != nil {
		m.Close()
		return fail(err)
	}
	if err := r.startAdmin(b, order); err != nil {
		r.Close()
		r.kv.dropAll()
		return fail(err)
	}
	if err := r.writeRead(r.view.Load()); err != nil {
		r.Close()
		r.kv.dropAll()
		return fail(err)
	}
	return r, nil
}

// Join enters a room from the welcome that an admin's Add returned for a key package of this node. It pulls the sealed tree
// from the devices of the room that are online, and from the pins when none gives it; the tree must hash to what the welcome
// says and open with the Welcome's keys.
func (n *Node) Join(ctx context.Context, welcome []byte) (*Room, error) {
	inv, err := decodeInvite(welcome, n.limits.MaxTreeBytes)
	if err != nil {
		return nil, err
	}
	sealed, err := n.fetchTree(ctx, inv)
	if err != nil {
		return nil, err
	}
	tree := func(_ []byte, _ uint64, max int) ([]byte, error) {
		if max > 0 && len(sealed) > max {
			return nil, settle.ErrPinTooLarge
		}
		return sealed, nil
	}
	m, err := settle.JoinMember(n.cfg.MLSStore, inv.Welcome, inv.Settled, tree, kolomls.JoinOptions{Config: n.mlsConfig(), MaxTreeBytes: n.limits.MaxTreeBytes}, settle.MemberOptions{})
	if err != nil {
		return nil, err
	}
	r, err := n.newRoom(ctx, inv.Name, m, inv.Secret, nil, inv.Logs)
	if err != nil {
		m.Close()
		return nil, err
	}
	return r, nil
}

// Room returns the room with the given id.
func (n *Node) Room(id GroupID) (*Room, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if r := n.rooms[id]; r != nil {
		return r, nil
	}
	return nil, ErrUnknownRoom
}

// Rooms returns every room of the node.
func (n *Node) Rooms() []*Room {
	n.mu.Lock()
	defer n.mu.Unlock()
	out := make([]*Room, 0, len(n.rooms))
	for _, r := range n.rooms {
		out = append(out, r)
	}
	return out
}

// Close stops every room of the node.
func (n *Node) Close() error {
	n.mu.Lock()
	n.closed = true
	rooms := make([]*Room, 0, len(n.rooms))
	for _, r := range n.rooms {
		rooms = append(rooms, r)
	}
	n.mu.Unlock()
	for _, r := range rooms {
		r.Close()
	}
	return nil
}

func topicOf(secret [keySize]byte) kologossip.Topic {
	h := sha256.New()
	h.Write([]byte(topicLabel))
	h.Write(secret[:])
	return kologossip.Topic(h.Sum(nil))
}

// syncConns returns the kolo-sync part of every connection being served.
func (n *Node) syncConns() []kolosync.Conn {
	n.mu.Lock()
	defer n.mu.Unlock()
	var out []kolosync.Conn
	for cs := range n.links {
		if !cs.done {
			out = append(out, cs.mux.sync)
		}
	}
	return out
}

// fetchTree pulls the sealed tree that an invite names into a replica of its log, from the connections of this node, those
// that a lookup of the room's topic brings, and then from the pins, and returns it once it hashes to what the invite says.
func (n *Node) fetchTree(ctx context.Context, inv *invite) ([]byte, error) {
	hd, err := kololog.DecodeLogHeader(inv.TreeHeader)
	if err != nil || len(hd.LogKey) != ed25519.PublicKeySize || inv.TreeEntries == 0 {
		return nil, fmt.Errorf("%w: %w: the log header", ErrTree, ErrMalformed)
	}
	sec, err := logSecret(&inv.Secret)
	if err != nil {
		return nil, err
	}
	rep, err := n.cfg.Store.OpenReplica(hd.LogKey, hd.LogID, inv.TreeHeader)
	if err != nil {
		return nil, err
	}
	l := &roomLog{rep: rep, view: rep}
	if l.deriver, err = kololog.NewKeyDeriver(sec, hd.LogKey, rep.Header()); err != nil {
		rep.Close()
		return nil, err
	}
	defer func() {
		l.close()
		n.cfg.Store.Delete(hd.LogID)
	}()
	n.joining.Add(1)
	defer n.joining.Add(-1)
	topic := topicOf(inv.Secret)
	lookup := func() {
		if sw := n.cfg.Swarm; sw != nil {
			sw.Leave(topic)
			sw.Join(ctx, topic, &koloswarm.JoinOptions{Lookup: true})
		}
	}
	lookup()
	if sw := n.cfg.Swarm; sw != nil {
		defer sw.Leave(topic)
	}
	start, looked := time.Now(), time.Now()
	for rep.Len() < inv.TreeEntries {
		cs := n.syncConns()
		if len(cs) == 0 && time.Since(looked) >= time.Second {
			lookup()
			looked = time.Now()
		}
		for _, c := range cs {
			n.pullTree(ctx, c, rep, inv.TreeEntries)
		}
		if rep.Len() < inv.TreeEntries && time.Since(start) >= n.limits.PinWait {
			n.pullTreeFromPins(ctx, rep, inv.TreeEntries)
		}
		if rep.Len() >= inv.TreeEntries {
			break
		}
		select {
		case <-time.After(50 * time.Millisecond):
		case <-ctx.Done():
			return nil, fmt.Errorf("%w: %d of %d pieces: %w", ErrTree, rep.Len(), inv.TreeEntries, ctx.Err())
		}
	}
	if err := l.deriveKeys(); err != nil {
		return nil, err
	}
	sealed, err := readPieces(rep, 0, inv.TreeHash, n.limits.MaxTreeBytes+sealedTreeOverhead)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrTree, err)
	}
	return sealed, nil
}

// sealedTreeOverhead is the device key and the tag that settle.SealTree adds to a tree.
const sealedTreeOverhead = 48

// pullTree pulls the log into rep over conn and stops once rep holds entries, so a log that grows past the invite is not read.
func (n *Node) pullTree(ctx context.Context, conn kolosync.Conn, rep *kolostore.Replica, entries uint64) {
	pctx, cancel := context.WithTimeout(ctx, 2*n.limits.PinWait)
	defer cancel()
	go func() {
		for pctx.Err() == nil && rep.Len() < entries {
			time.Sleep(10 * time.Millisecond)
		}
		cancel()
	}()
	kolosync.PullCiphertext(pctx, conn, rep)
}

func (n *Node) pullTreeFromPins(ctx context.Context, rep *kolostore.Replica, entries uint64) {
	for _, p := range n.cfg.Pins {
		if rep.Len() >= entries {
			return
		}
		dctx, cancel := context.WithTimeout(ctx, 2*n.limits.PinWait)
		if conn, err := kolosecure.Dial(dctx, p.Addr, n.cfg.Device, p.ID, nil); err == nil {
			n.pullTree(dctx, conn, rep, entries)
			conn.Close()
		}
		cancel()
	}
}
