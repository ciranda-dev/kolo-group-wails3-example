// Package room is the group room of Kolo Group: the narrow API of decision 0024 section 9. A method that is not built
// yet returns ErrNotImplemented.
package room

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	kolobraid "github.com/ciranda-dev/kolo-braid"
	kolodht "github.com/ciranda-dev/kolo-dht"
	kologossip "github.com/ciranda-dev/kolo-gossip"
	koloid "github.com/ciranda-dev/kolo-id"
	kololog "github.com/ciranda-dev/kolo-log"
	kolomls "github.com/ciranda-dev/kolo-mls"
	"github.com/ciranda-dev/kolo-mls/braid"
	"github.com/ciranda-dev/kolo-mls/settle"
	"github.com/ciranda-dev/kolo-pin"
	kolostore "github.com/ciranda-dev/kolo-store"
	koloswarm "github.com/ciranda-dev/kolo-swarm"
	kolosync "github.com/ciranda-dev/kolo-sync"
)

// GroupID names a room.
type GroupID [32]byte

// MessageID names a message, as kolo-gossip does.
type MessageID = kologossip.MessageID

// HistoryRule says how much history a new member reads.
type HistoryRule uint8

// The history rules; AllHistory is the default.
const (
	AllHistory HistoryRule = iota
	SinceEpoch
	NoHistory
)

// Role is a member's role in the room.
type Role uint8

// The roles above a plain member, whose Role is the zero value.
const (
	Admin Role = iota + 1
	Settler
)

// EventKind tells what an Event reports.
type EventKind uint8

// The kinds of Event: a message, a message of ours that a segment holds now, a change of the members, the removal of this
// device, and progress or a fault of a catch-up.
const (
	Message EventKind = iota
	Stored
	MembersChanged
	Removed
	CatchingUp
)

// Config is what Open needs. Store keeps the logs of the room, Backend the room's state and the admins' braid, and Pins
// are the pinning peers that keep the admins' logs while the admins are away.
type Config struct {
	Device   *koloid.Identity
	Cert     *koloid.DeviceCert
	Swarm    *koloswarm.Swarm
	Gossip   *kologossip.Gossip
	Store    *kolostore.Store
	MLSStore kolomls.Store
	// Backend holds the admins' braid and the Commits of the rooms this device creates.
	Backend kolostore.Backend
	// BraidLimits are the local bounds of the admins' braid; nil takes kolo-braid's defaults.
	BraidLimits *kolobraid.Limits
	Pins        []pin.Peer
	// ServeTree makes a member that is no admin keep and serve the tree logs of joiners, as the admins and the pins do; it
	// pulls the tree of every Add (14.5 MB at 30,000 leaves) and sends it to each joiner that asks. Off by default.
	ServeTree bool
	Now       func() time.Time
	Limits    Limits
}

// Policy is the room setting that a Commit changes: who may read the history, who may post and whether the room has a read
// link. With no poster listed and AdminsPost false every member may post. A room with a read link needs a list of posters.
type Policy struct {
	History       HistoryRule
	Since         uint64              // with SinceEpoch
	Posters       []ed25519.PublicKey // persons that may post
	PosterDevices []ed25519.PublicKey // devices that may post
	AdminsPost    bool                // the admin devices may post
	ReadLink      bool
}

// Member is one device of a room and its person.
type Member struct {
	Person ed25519.PublicKey
	Device ed25519.PublicKey
	Role   Role
}

// Status is what a room reports about itself.
type Status struct {
	Epoch        uint64
	Logs         int   // control logs and segments the device holds
	Receipts     int   // messages sent that no segment holds yet
	CatchingUp   bool  // a followed log has entries not read, or a peer offers more than the device holds
	AdminsOnline int   // other admin devices connected now
	PinError     error // the last error of a push to the pins, nil when the last push worked
}

// Event is something that happened in a room. An event with Err set reports a message or a Commit that could not be used.
type Event struct {
	Kind    EventKind
	ID      MessageID
	ReplyTo MessageID
	Parents []MessageID
	Person  ed25519.PublicKey
	Device  ed25519.PublicKey
	Time    time.Time
	Body    []byte
	Epoch   uint64
	Err     error
}

// Room is one group.
type Room struct {
	n       *Node
	gid     GroupID
	name    string
	secret  [keySize]byte
	self    []byte
	ctx     context.Context
	cancel  context.CancelFunc
	gr      *kologossip.Room
	wg      sync.WaitGroup
	gone    chan struct{}
	endGone func()
	once    sync.Once

	view    atomic.Pointer[view]
	changed atomic.Pointer[chan struct{}]
	removed atomic.Bool

	kmu  sync.RWMutex
	keys map[uint64][keySize]byte
	alts map[uint64][][keySize]byte // other keys that history links gave for an epoch, guarded by kmu
	lo   uint64                     // the oldest epoch in keys, guarded by kmu
	pols []polRec                   // the policy of each epoch it changed in, guarded by kmu

	reader  bool          // a room joined with a read link: no MLS member, only the epoch keys of the link's records
	readKey [keySize]byte // seals the state records, with reader

	ever      map[[32]byte]*kologossip.Author // every author the room has had, guarded by kmu
	first     map[[32]byte]uint64             // the first epoch this device saw each author a leaf in, guarded by kmu
	joinEpoch uint64

	rate   *limiter
	out    *limiter // paces what this device publishes, below the rate its peers accept
	ord    *orderer
	seen   *idSet
	events chan Event
	outMu  sync.Mutex

	sendMu sync.Mutex
	seq    atomic.Uint64
	cur    atomic.Uint64

	opMu       sync.Mutex
	mu         sync.Mutex // guards m, admin and b
	m          *settle.Member
	admin      *braid.Admin
	border     *braid.BraidOrder
	promo      *promotion // the braid this device joined before the roles named it, guarded by mu
	b          *kolobraid.Braid
	bp         atomic.Pointer[kolobraid.Braid] // b, for readers that must not take mu
	pins       settle.Pins
	adminKick  chan struct{}
	stepErr    error                  // guarded by mu
	badCommits map[[32]byte]struct{}  // the Commits and SettledCommits of this epoch reported as unusable, guarded by mu
	settledIn  map[uint64]*seenCommit // the Commit a quorum settled for each recent epoch, guarded by mu

	kv       roomKV
	logSec   *kololog.EntrySecret
	fol      *kolosync.Follower
	syncKick chan struct{}
	idx      objIndex

	logMu sync.Mutex // guards logs, ctl, meta and the fields of every roomLog past its header
	logs  map[logKey]*roomLog
	dirs  map[[32]byte][]byte // headers of control logs named before their owners were admins
	ctl   *roomLog
	meta  meta

	hmu      sync.Mutex           // guards links, past, pastBy and prevs
	links    map[uint64][]linkRec // the history links of each epoch before the join, one for each admin
	past     map[uint64][]*pastSeg
	pastBy   map[logKey]*pastSeg
	prevs    []prevLog         // the control logs that came before the ones followed
	early    map[logKey]uint64 // the final sizes of segments closed before a log that opened them was read
	needPast atomic.Uint64     // one past the epoch History is loading, 0 before it loads one

	histMu   sync.Mutex // serializes History and guards what follows
	hbound   uint64     // the epochs from it up are loaded
	hbuf     []Event    // loaded and not returned yet, oldest first
	hlast    MessageID  // the oldest message of the last page
	hseen    *idSet
	hreports []Event // reports of the links for the next page

	wmu      sync.Mutex
	welcomed map[[32]byte]uint64 // the devices this admin added that have not made contact, with the epoch of their Welcome

	ctlMu sync.Mutex // orders the appends to the control log
	segMu sync.Mutex // guards seg and orders the appends to it
	seg   *roomLog

	promoMu   sync.Mutex
	promoting map[[32]byte]chan *adminRequest // devices this admin offered the role to

	leaveMu sync.Mutex
	leaving map[[32]byte]struct{} // persons whose removal this admin is making

	rmu  sync.Mutex
	rcpt map[MessageID]*receipt

	hint     atomic.Pointer[hint]
	progress atomic.Int64 // when a log last grew or was read, in Unix nanoseconds
	pinMu    sync.Mutex
	pinSince time.Time
	pinConns map[string]*pinConn
	dmu      sync.Mutex
	dirty    map[*roomLog]struct{} // logs to push
	pushing  map[*roomLog]struct{} // the logs of the push that runs, which trimLocked must not forget
	pushKick chan struct{}
	pinErr   error
	caps     [][]byte
	capsAt   time.Time
}

// newRoom builds the room of m and joins its gossip topic. mt holds what a reopened room kept, nil for a new one.
func (n *Node) newRoom(ctx context.Context, name string, m *settle.Member, secret [keySize]byte, mt *meta, logs [][]byte) (*Room, error) {
	v, err := newView(m, nil, n.now(), n.limits.PastEpoch)
	if err != nil {
		return nil, err
	}
	return n.assemble(ctx, name, GroupID(m.GroupID()), m, v, secret, mt, logs, nil)
}

// assemble builds the room of a view and joins its gossip topic. A reader has no member m and the key of its state records;
// it keeps nothing in the node's backend, so a restart forgets it.
func (n *Node) assemble(ctx context.Context, name string, gid GroupID, m *settle.Member, v *view, secret [keySize]byte, mt *meta, logs [][]byte, readKey *[keySize]byte) (*Room, error) {
	now := n.now()
	sec, err := logSecret(&secret)
	if err != nil {
		return nil, err
	}
	kv := newRoomKV(n.cfg.Backend, gid)
	if readKey != nil {
		kv = newRoomKV(kolostore.NewMemory(), gid)
	}
	r := &Room{n: n, gid: gid, name: name, secret: secret, self: n.cfg.Device.PublicKey(), m: m,
		gone: make(chan struct{}), keys: map[uint64][keySize]byte{v.epoch: v.key}, alts: map[uint64][][keySize]byte{}, rate: newLimiter(n.limits.Rate, n.limits.Burst),
		out: newLimiter(n.limits.Rate*4/5, max(1, n.limits.Burst-1)), ord: newOrderer(n.cfg.Device.PublicKey(), n.limits), seen: newIDSet(n.limits.MaxKnown), events: make(chan Event, n.limits.EventQueue),
		adminKick: make(chan struct{}, 1), syncKick: make(chan struct{}, 1), pushKick: make(chan struct{}, 1),
		kv: kv, logSec: sec, logs: map[logKey]*roomLog{}, dirs: map[[32]byte][]byte{}, rcpt: map[MessageID]*receipt{}, welcomed: map[[32]byte]uint64{},
		pinConns: map[string]*pinConn{}, promoting: map[[32]byte]chan *adminRequest{}, leaving: map[[32]byte]struct{}{}, badCommits: map[[32]byte]struct{}{}, settledIn: map[uint64]*seenCommit{}, dirty: map[*roomLog]struct{}{}, ever: map[[32]byte]*kologossip.Author{}, first: map[[32]byte]uint64{}, joinEpoch: v.epoch,
		links: map[uint64][]linkRec{}, past: map[uint64][]*pastSeg{}, pastBy: map[logKey]*pastSeg{}, early: map[logKey]uint64{}}
	if readKey != nil {
		r.reader, r.readKey = true, *readKey
	}
	if mt != nil {
		r.meta, r.joinEpoch = *mt, mt.JoinEpoch
	} else {
		r.meta = meta{Name: name, Secret: secret, JoinEpoch: v.epoch}
	}
	r.pins = roomPins{r}
	r.endGone = sync.OnceFunc(func() { close(r.gone) })
	r.ctx, r.cancel = context.WithCancel(context.Background())
	r.view.Store(v)
	r.notify()
	r.moved()
	r.seq.Store(uint64(now.UnixNano()))
	// A pull that failed is tried again within CatchUpWait, since a device that is catching up waits for it.
	if r.fol, err = kolosync.NewFollower(&kolosync.FollowOptions{Ciphertext: true, RetryMax: max(n.limits.CatchUpWait, time.Second)}); err != nil {
		return nil, err
	}
	fail := func(err error) (*Room, error) {
		r.cancel()
		r.closeLogs()
		r.fol.Close()
		return nil, err
	}
	if err := r.restore(v); err != nil {
		return fail(err)
	}
	for _, h := range logs {
		if hd, err := kololog.DecodeLogHeader(h); err == nil && len(hd.LogKey) == 32 {
			r.addLog([32]byte(hd.LogKey), h, true, 0)
		}
	}
	if err := r.kv.put(r.meta.encode(), recMeta); err != nil {
		return fail(err)
	}
	n.mu.Lock()
	if n.closed || n.rooms[r.gid] != nil {
		n.mu.Unlock()
		return fail(errInvalid("the node is closed or holds this room already"))
	}
	n.rooms[r.gid] = r
	n.mu.Unlock()
	n.refreshMembers()
	r.gr, err = n.joinTopic(ctx, topicOf(secret), &kologossip.TopicOptions{
		Authors: authors{r}, Validate: r.validate, MaxMessageSize: n.limits.MaxMessageSize, Seq: r.cur.Load, AllowPeer: r.allowPeer,
	})
	if err != nil {
		n.mu.Lock()
		delete(n.rooms, r.gid)
		n.mu.Unlock()
		n.refreshMembers()
		return fail(err)
	}
	r.wg.Go(r.run)
	r.wg.Go(r.syncLoop)
	r.wg.Go(r.followEvents)
	return r, nil
}

// joinTopic joins a room's gossip topic. The swarm join announces the topic, which fails while no server of the discovery
// network answers: it is tried again, each time after twice the wait before up to PinWait, until ctx ends, so a restart in
// a moment the network does not answer opens the rooms once it does.
func (n *Node) joinTopic(ctx context.Context, t kologossip.Topic, o *kologossip.TopicOptions) (*kologossip.Room, error) {
	wait := 50 * time.Millisecond
	for {
		gr, err := gossipJoin(n.cfg.Gossip, ctx, t, o)
		if !errors.Is(err, kolodht.ErrNoNodes) {
			return gr, err
		}
		select {
		case <-time.After(wait):
		case <-ctx.Done():
			return nil, err
		}
		wait = min(2*wait, n.limits.PinWait)
	}
}

// gossipJoin is Gossip.Join, which a test slows down.
var gossipJoin = (*kologossip.Gossip).Join

// allowPeer admits to the room's topic the devices of its current epoch, and of the previous one while its window lasts;
// a room with a read link, or read with one, admits any device. A node that admits any device to its connections, for a
// room with a read link or for a join, so never tells it about its closed rooms.
func (r *Room) allowPeer(peer ed25519.PublicKey) bool {
	if len(peer) != ed25519.PublicKeySize {
		return false
	}
	v := r.view.Load()
	if r.reader || v.pol.link {
		return true
	}
	d := [32]byte(peer)
	return v.author(d) != nil || v.prev != nil && r.n.now().Before(v.prevUntil) && v.prev.author(d) != nil
}

// refreshPeers asks the topic again about every connected peer, now and once the window of the previous epoch has ended.
func (r *Room) refreshPeers() {
	if r.gr == nil || r.ctx.Err() != nil {
		return
	}
	r.gr.RefreshPeers()
	time.AfterFunc(r.n.limits.PastEpoch, func() {
		if r.ctx.Err() == nil {
			r.gr.RefreshPeers()
		}
	})
}

// restore loads what a reopened room kept: its epoch keys, authors, receipts and logs, and stores what a new one starts with.
func (r *Room) restore(v *view) error {
	if err := r.kv.each(recKey, func(tail, val []byte) error {
		if len(tail) != 8 || len(val) != keySize {
			return fmt.Errorf("%w: epoch key record", ErrMalformed)
		}
		r.keys[unbe(tail)] = [keySize]byte(val)
		return nil
	}); err != nil {
		return err
	}
	if err := r.kv.each(recAlt, func(tail, val []byte) error {
		if len(tail) != 8 || len(val) == 0 || len(val)%keySize != 0 {
			return fmt.Errorf("%w: key record", ErrMalformed)
		}
		for k := range slices.Chunk(val, keySize) {
			r.alts[unbe(tail)] = append(r.alts[unbe(tail)], [keySize]byte(k))
		}
		return nil
	}); err != nil {
		return err
	}
	r.keys[v.epoch] = v.key
	r.lo = v.epoch
	for e := range r.keys {
		r.lo = min(r.lo, e)
	}
	if err := r.kv.put(v.key[:], recKey, epochKeyName(v.epoch)); err != nil {
		return err
	}
	if err := r.kv.each(recPolicy, func(tail, val []byte) error {
		p, err := decodeRoomPolicy(val)
		if err != nil || len(tail) != 8 {
			return fmt.Errorf("%w: policy record", ErrMalformed)
		}
		r.pols = append(r.pols, polRec{unbe(tail), p})
		return nil
	}); err != nil {
		return err
	}
	if err := r.notePolicy(v); err != nil {
		return err
	}
	if err := r.kv.each(recAuthor, func(tail, val []byte) error {
		a, err := decodeAuthor(val)
		if err != nil || len(tail) != 32 {
			return fmt.Errorf("%w: author record", ErrMalformed)
		}
		r.ever[[32]byte(tail)] = a
		return nil
	}); err != nil {
		return err
	}
	if err := r.kv.each(recFirst, func(tail, val []byte) error {
		if len(tail) != 32 || len(val) != 8 {
			return fmt.Errorf("%w: first epoch record", ErrMalformed)
		}
		r.first[[32]byte(tail)] = unbe(val)
		return nil
	}); err != nil {
		return err
	}
	if err := r.rememberAuthors(v); err != nil {
		return err
	}
	if err := r.loadReceipts(); err != nil {
		return err
	}
	if err := r.kv.each(recWelcome, func(tail, v []byte) error {
		if len(tail) != 32 || len(v) != 8 {
			return fmt.Errorf("%w: welcome record", ErrMalformed)
		}
		r.welcomed[[32]byte(tail)] = unbe(v)
		return nil
	}); err != nil {
		return err
	}
	return r.reopenLogs()
}

// rememberAuthors keeps the authors of a view, so the messages of a device that left are still read, and the first epoch
// each was a leaf in.
func (r *Room) rememberAuthors(v *view) error {
	r.kmu.Lock()
	defer r.kmu.Unlock()
	for d, a := range v.authors {
		if _, ok := r.first[d]; !ok {
			r.first[d] = v.epoch
			if err := r.kv.put(be(v.epoch), recFirst, d[:]); err != nil {
				return err
			}
		}
		if r.ever[d] == nil || !bytes.Equal(r.ever[d].Cert.Encode(), a.Cert.Encode()) {
			r.ever[d] = a
			if err := r.kv.putAuthor(d, a); err != nil {
				return err
			}
		}
	}
	return nil
}

// reopenLogs opens the logs a restart finds in the store: this device's own and the replicas it follows.
func (r *Room) reopenLogs() error {
	type rec struct {
		owner [32]byte
		id    kololog.LogID
		r     *logRec
	}
	var recs []rec
	if err := r.kv.each(recLog, func(tail, val []byte) error {
		lr, err := decodeLogRec(val)
		if err != nil || len(tail) != 64 {
			return fmt.Errorf("%w: log record", ErrMalformed)
		}
		recs = append(recs, rec{[32]byte(tail[:32]), kololog.LogID(tail[32:]), lr})
		return nil
	}); err != nil {
		return err
	}
	for _, x := range recs {
		var l *roomLog
		var err error
		if !x.r.Ctl && !x.r.Own && (x.r.Epoch < r.joinEpoch || x.r.Tree && (x.r.Epoch < r.view.Load().epoch || !r.servesTree())) {
			r.n.cfg.Store.Delete(x.id)
			r.kv.del(recLog, x.owner[:], x.id[:])
			continue
		}
		if x.r.Own {
			if l, err = r.openLog(x.r.Ctl, x.r.Epoch, &x.id); err == nil {
				l.tree = x.r.Tree
				r.logMu.Lock()
				err = r.trackLocked(l)
				r.logMu.Unlock()
			}
		} else {
			l, err = r.replicateKind(x.owner, x.r.Header, x.r.Ctl, x.r.Tree, x.r.Epoch)
		}
		if err != nil {
			return fmt.Errorf("room: reopening a log: %w", err)
		}
		r.logMu.Lock()
		l.closed, l.size, l.cursor, l.done = x.r.Closed, x.r.Size, x.r.Cursor, x.r.Done
		if l.own && l.ctl && (!r.meta.HasCtl || l.id == r.meta.Ctl) {
			r.ctl = l
		}
		if l.own && !l.ctl && !l.tree && !l.closed {
			r.seg = l
		}
		r.logMu.Unlock()
	}
	if r.ctl == nil {
		return nil
	}
	r.wg.Go(r.pinLoop)
	// A device that stopped may have stopped before the pins took what it wrote.
	for _, l := range r.logs {
		if l.own {
			r.pinSoon(l)
		}
	}
	return r.indexOwn()
}

// indexOwn rebuilds the index of this admin's own control log and the ids of the segment it goes on writing, and writes
// the history link and the readers' state of the current epoch when the log lacks them: a crash between applying a Commit
// and writing them loses them, and with one admin the chain of links would end there.
func (r *Room) indexOwn() error {
	v := r.view.Load()
	hasLink, hasRead := false, false
	for _, l := range r.logs {
		if !l.own || !l.ctl {
			continue
		}
		for i := uint64(0); i < l.log.Len(); i++ {
			b, err := l.log.Get(i)
			if err != nil {
				return err
			}
			rec, err := decodeCtl(b, r.n.limits.MaxMessageSize)
			if err != nil {
				return err
			}
			r.indexRecord(l, i, rec)
			hasLink = hasLink || rec.Kind == ctlLink && rec.Epoch == v.epoch
			hasRead = hasRead || rec.Kind == ctlRead && rec.Epoch == v.epoch
		}
		l.applied = l.log.Len()
	}
	if r.seg != nil {
		for i := uint64(0); i < r.seg.log.Len(); i++ {
			b, err := r.seg.log.Get(i)
			if err != nil {
				return err
			}
			if st, err := decodeStored(b); err == nil {
				r.seen.add(messageID(r.gid[:], st.Device[:], st.Seq, st.Body))
			}
		}
	}
	if !v.roles.Admin(r.self) {
		return nil
	}
	errs := []error{r.rotateCtl(v.epoch)}
	if !hasLink {
		errs = append(errs, r.writeLink(v))
	}
	if !hasRead {
		errs = append(errs, r.writeRead(v))
	}
	return errors.Join(errs...)
}

func (r *Room) closeLogs() {
	r.logMu.Lock()
	defer r.logMu.Unlock()
	for k, l := range r.logs {
		r.n.sync.Withdraw(l.owner[:], l.id)
		r.n.logRoom.Delete(l.key())
		l.close()
		if r.reader {
			r.n.cfg.Store.Delete(l.id)
		}
		delete(r.logs, k)
	}
}

// startAdmin makes this device an admin of the room over the braid b.
func (r *Room) startAdmin(b *kolobraid.Braid, order *braid.BraidOrder) error {
	if err := r.ensureCtl(); err != nil {
		return err
	}
	a, err := braid.NewAdmin(r.n.cfg.Device, order, r.m, braid.Options{Pins: r.pins, Now: r.n.now})
	if err != nil {
		return err
	}
	r.mu.Lock()
	r.b, r.admin, r.border = b, a, order
	r.bp.Store(b)
	r.mu.Unlock()
	r.n.attachAll()
	r.wg.Go(r.braidEvents)
	r.wg.Go(r.adminLoop)
	return nil
}

// ID returns the room's group id.
func (r *Room) ID() GroupID { return r.gid }

// Name returns the name the room was created with.
func (r *Room) Name() string { return r.name }

// Close leaves the room's topic and stops its work; the room's state stays in the stores.
func (r *Room) Close() error {
	r.once.Do(func() {
		r.cancel()
		r.endGone()
		r.gr.Leave()
		r.pinMu.Lock()
		for _, c := range r.pinConns {
			c.cancel()
		}
		r.pinMu.Unlock()
		r.wg.Wait()
		r.fol.Close()
		r.closeLogs()
		if r.b != nil {
			r.b.Close()
		}
		if r.m != nil {
			r.mu.Lock()
			r.m.Close()
			r.mu.Unlock()
		}
		r.n.mu.Lock()
		delete(r.n.rooms, r.gid)
		r.n.mu.Unlock()
		r.n.refreshMembers()
	})
	return nil
}

func (r *Room) notify() {
	ch := make(chan struct{})
	if old := r.changed.Swap(&ch); old != nil {
		close(*old)
	}
}

// takeToken waits until this device may publish another message: its peers refuse a device that sends faster than Rate,
// with a loss of score, so it stays below it.
func (r *Room) takeToken(ctx context.Context) error {
	wait := max(time.Millisecond, time.Duration(float64(time.Second)/(r.n.limits.Rate*4/5)))
	for !r.out.allow([32]byte(r.self), r.n.now()) {
		select {
		case <-time.After(wait):
		case <-ctx.Done():
			return ctx.Err()
		case <-r.ctx.Done():
			return ErrClosed
		}
	}
	return nil
}

// publish signs and sends one gossip body that mk builds for the sequence number the message will carry.
func (r *Room) publish(ctx context.Context, mk func(seq uint64) ([]byte, error)) (MessageID, error) {
	if err := r.takeToken(ctx); err != nil {
		return MessageID{}, err
	}
	r.sendMu.Lock()
	defer r.sendMu.Unlock()
	seq := r.seq.Add(1)
	r.cur.Store(seq)
	body, err := mk(seq)
	if err != nil {
		return MessageID{}, err
	}
	m, err := r.gr.Publish(ctx, body)
	switch {
	case errors.Is(err, kologossip.ErrTooLarge):
		return MessageID{}, fmt.Errorf("%w: %w", ErrTooLarge, err)
	case errors.Is(err, kologossip.ErrNotJoined), errors.Is(err, kologossip.ErrClosed):
		return MessageID{}, ErrClosed
	case err != nil:
		return MessageID{}, err
	}
	return m.ID, nil
}

// Send posts a message, as a reply when replyTo is not zero. It returns once the message is sent; the message shows in
// Next, here and on the other members, when the room has applied it. A device that is no admin keeps the message until a
// segment holds it, and publishes it again when none does (see Event Stored).
func (r *Room) Send(ctx context.Context, body []byte, replyTo MessageID) (MessageID, error) {
	switch {
	case r.removed.Load():
		return MessageID{}, ErrRemoved
	case r.ctx.Err() != nil:
		return MessageID{}, ErrClosed
	case len(body) > r.n.limits.MaxMessageSize:
		return MessageID{}, ErrTooLarge
	}
	v := r.view.Load()
	if r.reader || !v.pol.mayPost(r.n.cfg.Cert.Person(), r.self, v.roles) {
		return MessageID{}, ErrNotPoster
	}
	var (
		ev   Event
		st   *stored
		sent []byte
	)
	admin := r.isAdmin()
	_, err := r.publish(ctx, func(n uint64) ([]byte, error) {
		now := r.n.now()
		c := &content{ReplyTo: replyTo, Parents: r.ord.parents(), Time: now.UnixMilli(), Body: body}
		nonce, ct, err := seal(&v.key, aad(r.gid[:], v.epoch, r.self, n), c.encode())
		if err != nil {
			return nil, err
		}
		s := &sealed{Epoch: v.epoch, Nonce: nonce, CT: ct}
		if err := signSealed(r.n.cfg.Device, r.gid[:], s, n); err != nil {
			return nil, err
		}
		sent = s.encode()
		ev = Event{Kind: Message, ID: messageID(r.gid[:], r.self, n, sent), ReplyTo: replyTo, Parents: c.Parents, Person: r.n.cfg.Cert.Person(), Device: r.self,
			Time: time.UnixMilli(c.Time), Body: slices.Clone(body), Epoch: v.epoch}
		st = &stored{Device: [32]byte(r.self), Seq: n, Body: sent}
		if !admin {
			if err := r.addReceipt(ev.ID, n, sent); err != nil {
				return nil, err
			}
		}
		return sent, nil
	})
	if err != nil {
		if st != nil && !admin {
			r.dropReceipt(ev.ID)
		}
		return MessageID{}, err
	}
	r.seen.add(ev.ID)
	r.release(func() []Event { return r.ord.addOwn(ev) })
	if admin {
		if err := r.archive(st); err != nil {
			return ev.ID, fmt.Errorf("room: the message is sent but this admin could not store it: %w", err)
		}
		r.emit(Event{Kind: Stored, ID: ev.ID, Epoch: v.epoch})
	}
	return ev.ID, nil
}

// Next waits for the next event of the room: messages in display order, changes of the members, and the removal of
// this device, after which it returns ErrRemoved.
func (r *Room) Next(ctx context.Context) (Event, error) {
	select {
	case ev := <-r.events:
		return ev, nil
	default:
	}
	select {
	case ev := <-r.events:
		return ev, nil
	case <-r.gone:
		select {
		case ev := <-r.events:
			return ev, nil
		default:
		}
		if r.removed.Load() {
			return Event{}, ErrRemoved
		}
		return Event{}, ErrClosed
	case <-ctx.Done():
		return Event{}, ctx.Err()
	}
}

// release pushes what f releases from the orderer to the event queue, in order.
func (r *Room) release(f func() []Event) {
	r.outMu.Lock()
	defer r.outMu.Unlock()
	for _, ev := range f() {
		select {
		case r.events <- ev:
		case <-r.ctx.Done():
			return
		}
	}
}

func (r *Room) emit(evs ...Event) {
	r.release(func() []Event { return evs })
}

// Members lists the devices of the room's current epoch, in the order of the tree.
func (r *Room) Members() []Member {
	v := r.view.Load()
	out := make([]Member, len(v.members))
	for i, m := range v.members {
		out[i] = Member{Person: slices.Clone(m.Person), Device: slices.Clone(m.Device), Role: m.Role}
	}
	return out
}

// Status reports the room's state.
func (r *Room) Status() Status {
	r.logMu.Lock()
	logs := len(r.logs)
	r.logMu.Unlock()
	r.rmu.Lock()
	rc := len(r.rcpt)
	r.rmu.Unlock()
	r.dmu.Lock()
	perr := r.pinErr
	r.dmu.Unlock()
	return Status{Epoch: r.view.Load().epoch, Logs: logs, Receipts: rc, CatchingUp: r.catchingUp(), AdminsOnline: r.adminsOnline(), PinError: perr}
}

func (r *Room) run() {
	in := make(chan *kologossip.Message)
	r.wg.Go(func() {
		defer close(in)
		for {
			m, err := r.gr.Next(r.ctx)
			if err != nil {
				return
			}
			select {
			case in <- m:
			case <-r.ctx.Done():
				return
			}
		}
	})
	for {
		var timeout <-chan time.Time
		t := time.NewTimer(0)
		if d := r.ord.nextDeadline(); !d.IsZero() {
			t.Reset(max(0, d.Sub(r.n.now())))
			timeout = t.C
		}
		select {
		case m, ok := <-in:
			t.Stop()
			if !ok {
				return
			}
			r.handle(m)
		case <-timeout:
			r.release(func() []Event { return r.ord.expire(r.n.now()) })
		case <-r.ctx.Done():
			t.Stop()
			return
		}
	}
}

func (r *Room) handle(m *kologossip.Message) {
	if len(m.Body) == 0 {
		return
	}
	r.contact([32]byte(m.Device))
	if r.reader && m.Body[0] != kindMessage {
		return
	}
	switch m.Body[0] {
	case kindMessage:
		r.receive(m)
	case kindCommit:
		r.hintCommit(m)
	case kindLeave:
		r.handleLeave(m)
	case kindPromote, kindAdminWelcome:
		if o, err := decodeAdminOffer(m.Body); err == nil {
			if o.Kind == kindPromote {
				r.handleOffer(m, o)
			} else {
				r.handleWelcome(m, o)
			}
		}
	case kindAdminRequest:
		if q, err := decodeAdminRequest(m.Body); err == nil {
			r.handleRequest(m, q)
		}
	}
}

// receive opens a message the validator accepted, stores it when this device is an admin, and hands it to the orderer.
func (r *Room) receive(m *kologossip.Message) {
	id := messageID(r.gid[:], m.Device, m.Seq, m.Body)
	ev := Event{Kind: Message, ID: id, Person: m.Person, Device: m.Device, Time: m.Time}
	s, err := decodeSealed(m.Body)
	if err != nil {
		ev.Err = err
		r.emit(ev)
		return
	}
	ev.Epoch = s.Epoch
	r.kmu.RLock()
	key, ok := r.keys[s.Epoch]
	r.kmu.RUnlock()
	if !ok {
		ev.Err = ErrWrongEpoch
		r.emit(ev)
		return
	}
	c, err := r.openMessage(&key, s, m)
	if err != nil {
		ev.Err = err
		r.emit(ev)
		return
	}
	if v := r.view.Load(); !r.mayPostAt(v, s.Epoch, liveAt(v, s.Epoch, r.n.now()), m.Person, m.Device) {
		ev.Err = ErrNotPoster
		r.emit(ev)
		return
	}
	if !r.seen.add(id) {
		return
	}
	if err := r.archive(&stored{Device: [32]byte(m.Device), Seq: m.Seq, Body: m.Body}); err != nil && r.ctx.Err() == nil {
		r.emit(Event{Kind: Stored, ID: id, Err: fmt.Errorf("room: this admin could not store a message: %w", err)})
	}
	ev.ReplyTo, ev.Parents, ev.Time, ev.Body = c.ReplyTo, c.Parents, time.UnixMilli(c.Time), c.Body
	r.release(func() []Event { return r.ord.add(ev, r.n.now()) })
}

// hintCommit keeps the SettledCommit of an announced Commit for the sync loop, which pulls the Commit by its hash. The
// validator has checked its quorum.
func (r *Room) hintCommit(m *kologossip.Message) {
	c, err := decodeCommitRef(m.Body)
	if err != nil {
		return
	}
	st, err := settle.DecodeSettled(c.Settled, 0)
	if err != nil {
		return
	}
	r.sawSettled(st.TBS.Epoch, st.TBS.Commit)
	r.hint.Store(&hint{epoch: st.TBS.Epoch, settled: c.Settled})
	r.kick()
}

// hint is the SettledCommit an admin announced for an epoch.
type hint struct {
	epoch   uint64
	settled []byte
}

// finishRemoval ends Next once a removed device's events are queued, and forgets what the room kept: a device that was
// removed has no room to reopen and holds no key of it.
func (r *Room) finishRemoval() {
	if !r.removed.Load() {
		return
	}
	r.endGone()
	r.logMu.Lock()
	for _, l := range r.logs {
		r.forgetLocked(l, true)
	}
	r.logMu.Unlock()
	r.kv.dropAll()
}

// refreshLocked reads the room's new epoch after a Commit was applied; r.mu is held.
func (r *Room) refreshLocked() []Event {
	if r.m.Removed() {
		epoch := r.view.Load().epoch
		r.removed.Store(true)
		r.gr.Leave()
		r.n.refreshMembers()
		return []Event{{Kind: Removed, Epoch: epoch}}
	}
	v, err := newView(r.m, r.view.Load(), r.n.now(), r.n.limits.PastEpoch)
	if err != nil {
		return []Event{{Kind: MembersChanged, Err: err}}
	}
	r.kmu.Lock()
	r.keys[v.epoch] = v.key
	r.kmu.Unlock()
	if err := errors.Join(r.kv.put(v.key[:], recKey, epochKeyName(v.epoch)), r.rememberAuthors(v)); err != nil {
		return []Event{{Kind: MembersChanged, Err: err}}
	}
	// The link goes to the log before the view changes, so no segment of the new epoch comes before it.
	var rotErr error
	if v.roles.Admin(r.self) {
		rotErr = r.rotateCtl(v.epoch)
	}
	linkErr := errors.Join(rotErr, r.notePolicy(v), r.writeLink(v), r.writeRead(v))
	r.view.Store(v)
	r.notify()
	r.n.refreshMembers()
	evs := []Event{{Kind: MembersChanged, Epoch: v.epoch}}
	if linkErr != nil {
		evs = append(evs, Event{Kind: MembersChanged, Epoch: v.epoch, Err: fmt.Errorf("room: writing the records of epoch %d: %w", v.epoch, linkErr)})
	}
	return evs
}
