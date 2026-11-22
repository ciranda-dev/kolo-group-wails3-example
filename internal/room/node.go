package room

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"net/netip"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	kolobraid "github.com/ciranda-dev/kolo-braid"
	kololog "github.com/ciranda-dev/kolo-log"
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

	mu      sync.Mutex
	rooms   map[GroupID]*Room
	conns   map[[32]byte][]koloswarm.Conn
	members atomic.Pointer[map[[32]byte]struct{}]
	closed  bool
}

// Open starts a node.
func Open(ctx context.Context, cfg *Config) (*Node, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if cfg == nil || cfg.Device == nil || cfg.Cert == nil || cfg.Gossip == nil || cfg.MLSStore == nil || cfg.Backend == nil {
		return nil, errInvalid("a node needs a device, its certificate, a gossip, an MLS store and a backend")
	}
	if string(cfg.Cert.Device()) != string(cfg.Device.PublicKey()) {
		return nil, errInvalid("the certificate is for another device")
	}
	lim, err := cfg.Limits.withDefaults()
	if err != nil {
		return nil, err
	}
	n := &Node{cfg: *cfg, limits: lim, now: cfg.Now, rooms: map[GroupID]*Room{}, conns: map[[32]byte][]koloswarm.Conn{}}
	if n.now == nil {
		n.now = time.Now
	}
	n.members.Store(&map[[32]byte]struct{}{})
	n.sync = kolosync.NewServer(func(peer, _ ed25519.PublicKey, _ kololog.LogID) kolosync.Access {
		if n.isMember(peer) {
			return kolosync.Ciphertext
		}
		return kolosync.NoAccess
	})
	return n, nil
}

func (n *Node) isMember(key []byte) bool {
	if len(key) != ed25519.PublicKeySize {
		return false
	}
	_, ok := (*n.members.Load())[[32]byte(key)]
	return ok
}

// Allow is the admission rule for koloswarm.Config.Allow: only devices that are leaves of a room of this node, and
// that gossip has not graylisted.
func (n *Node) Allow(key ed25519.PublicKey, addr netip.AddrPort) bool {
	return n.isMember(key) && n.cfg.Gossip.Allow(key, addr)
}

// ServeConn serves one connection: gossip on its unidirectional streams and kolo-sync on its bidirectional ones.
// A device that is no member of any room is refused.
func (n *Node) ServeConn(ctx context.Context, conn koloswarm.Conn, outbound bool) error {
	key := conn.RemotePublicKey()
	if !n.isMember(key) {
		conn.CloseWithError(0, "room: not a member device")
		return ErrNotMember
	}
	k := [32]byte(key)
	n.mu.Lock()
	n.conns[k] = append(n.conns[k], conn)
	n.mu.Unlock()
	defer func() {
		n.mu.Lock()
		n.conns[k] = slices.DeleteFunc(n.conns[k], func(c koloswarm.Conn) bool { return c == conn })
		if len(n.conns[k]) == 0 {
			delete(n.conns, k)
		}
		n.mu.Unlock()
	}()
	sctx, cancel := context.WithCancel(ctx)
	var wg sync.WaitGroup
	wg.Go(func() { n.sync.Serve(sctx, conn) })
	err := n.cfg.Gossip.ServeConn(ctx, conn, outbound)
	cancel()
	wg.Wait()
	return err
}

// refreshMembers recomputes the admitted devices from the rooms and closes the connections of those that left.
func (n *Node) refreshMembers() {
	n.mu.Lock()
	defer n.mu.Unlock()
	set := map[[32]byte]struct{}{}
	for _, r := range n.rooms {
		if r.removed.Load() {
			continue
		}
		if v := r.view.Load(); v != nil {
			for d := range v.authors {
				set[d] = struct{}{}
			}
		}
	}
	n.members.Store(&set)
	for k, cs := range n.conns {
		if _, ok := set[k]; !ok {
			for _, c := range cs {
				c.CloseWithError(0, "room: no longer a member device")
			}
		}
	}
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
	return kolomls.NewKeyPackage(n.cfg.MLSStore, kolomls.KeyPackageOptions{Credential: c, SignatureKey: sig, Capabilities: settle.Capabilities()})
}

// Create makes a room with this device as its first member, admin and settler.
func (n *Node) Create(ctx context.Context, name string, p Policy) (*Room, error) {
	if p.History != AllHistory || p.Since != 0 || len(p.Posters) != 0 || p.ReadLink {
		return nil, ErrNotImplemented
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
	exts, err := settle.GroupExtensions(roles)
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
	r, err := n.newRoom(ctx, name, m, [keySize]byte(secret))
	if err != nil {
		m.Close()
		return fail(err)
	}
	if err := r.startAdmin(b, order, kolobraid.PrefixBackend(n.cfg.Backend, []byte(prefix+"p/"))); err != nil {
		r.Close()
		return fail(err)
	}
	return r, nil
}

// Join enters a room from the welcome that an admin's Add returned for a key package of this node.
func (n *Node) Join(ctx context.Context, welcome []byte) (*Room, error) {
	inv, err := decodeInvite(welcome, n.limits.MaxTreeBytes)
	if err != nil {
		return nil, err
	}
	tree := func(_ []byte, _ uint64, max int) ([]byte, error) {
		if max > 0 && len(inv.Tree) > max {
			return nil, settle.ErrPinTooLarge
		}
		return inv.Tree, nil
	}
	m, err := settle.JoinMember(n.cfg.MLSStore, inv.Welcome, inv.Settled, tree, kolomls.JoinOptions{Config: n.mlsConfig(), MaxTreeBytes: n.limits.MaxTreeBytes}, settle.MemberOptions{})
	if err != nil {
		return nil, err
	}
	r, err := n.newRoom(ctx, inv.Name, m, inv.Secret)
	if err != nil {
		m.Close()
		return nil, err
	}
	return r, nil
}

// JoinLink enters a room that has a read link; the device only reads.
func (n *Node) JoinLink(ctx context.Context, link []byte) (*Room, error) {
	return nil, ErrNotImplemented
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
