// Package room is the group room of Kolo Group: the narrow API of decision 0024 section 9. A method that is not built
// yet returns ErrNotImplemented.
package room

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	kolobraid "github.com/ciranda-dev/kolo-braid"
	kologossip "github.com/ciranda-dev/kolo-gossip"
	koloid "github.com/ciranda-dev/kolo-id"
	kolomls "github.com/ciranda-dev/kolo-mls"
	"github.com/ciranda-dev/kolo-mls/braid"
	"github.com/ciranda-dev/kolo-mls/settle"
	"github.com/ciranda-dev/kolo-pin"
	kolostore "github.com/ciranda-dev/kolo-store"
	koloswarm "github.com/ciranda-dev/kolo-swarm"
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

// The kinds of Event; Stored and CatchingUp are not built yet.
const (
	Message EventKind = iota
	Stored
	MembersChanged
	Removed
	CatchingUp
)

// Config is what Open needs. Store and Pins are for the segments and pins, which are not built yet.
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
	Now         func() time.Time
	Limits      Limits
}

// Policy is the room setting that a Commit changes; only the zero value is built.
type Policy struct {
	History  HistoryRule
	Since    uint64              // with SinceEpoch
	Posters  []ed25519.PublicKey // persons; empty means every member
	ReadLink bool
}

// Member is one device of a room and its person.
type Member struct {
	Person ed25519.PublicKey
	Device ed25519.PublicKey
	Role   Role
}

// Status is what a room reports about itself.
type Status struct {
	Epoch uint64
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

// envelopeReserve bounds what gossip's envelope adds to a body; TestEnvelopeOverhead measures 178 bytes.
const envelopeReserve = 256

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

	rate   *limiter
	ord    *orderer
	events chan Event
	outMu  sync.Mutex

	sendMu sync.Mutex
	seq    atomic.Uint64
	cur    atomic.Uint64

	opMu    sync.Mutex
	mu      sync.Mutex // guards m and admin
	m       *settle.Member
	admin   *braid.Admin
	b       *kolobraid.Braid
	pins    settle.Pins
	kick    chan struct{}
	stepErr error // guarded by mu
}

// newRoom builds the room of m and joins its gossip topic.
func (n *Node) newRoom(ctx context.Context, name string, m *settle.Member, secret [keySize]byte) (*Room, error) {
	now := n.now()
	v, err := newView(m, nil, now, n.limits.PastEpoch)
	if err != nil {
		return nil, err
	}
	r := &Room{n: n, gid: GroupID(m.GroupID()), name: name, secret: secret, self: n.cfg.Device.PublicKey(), m: m,
		gone: make(chan struct{}), keys: map[uint64][keySize]byte{v.epoch: v.key}, rate: newLimiter(n.limits.Rate, n.limits.Burst),
		ord: newOrderer(n.cfg.Device.PublicKey(), n.limits), events: make(chan Event, n.limits.EventQueue), kick: make(chan struct{}, 1)}
	r.endGone = sync.OnceFunc(func() { close(r.gone) })
	r.ctx, r.cancel = context.WithCancel(context.Background())
	r.view.Store(v)
	r.notify()
	r.seq.Store(uint64(now.UnixNano()))
	n.mu.Lock()
	if n.closed || n.rooms[r.gid] != nil {
		n.mu.Unlock()
		r.cancel()
		return nil, errInvalid("the node is closed or holds this room already")
	}
	n.rooms[r.gid] = r
	n.mu.Unlock()
	n.refreshMembers()
	r.gr, err = n.cfg.Gossip.Join(ctx, topicOf(secret), &kologossip.TopicOptions{
		Authors: authors{r}, Validate: r.validate, MaxMessageSize: n.limits.MaxMessageSize, Seq: r.cur.Load,
	})
	if err != nil {
		n.mu.Lock()
		delete(n.rooms, r.gid)
		n.mu.Unlock()
		n.refreshMembers()
		r.cancel()
		return nil, err
	}
	r.wg.Go(r.run)
	return r, nil
}

// startAdmin makes this device the admin of the room over the braid b.
func (r *Room) startAdmin(b *kolobraid.Braid, order braid.Order, backend kolostore.Backend) error {
	r.pins = pins{backend}
	a, err := braid.NewAdmin(r.n.cfg.Device, order, r.m, braid.Options{Pins: r.pins, Now: r.n.now})
	if err != nil {
		return err
	}
	r.b, r.admin = b, a
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
		r.wg.Wait()
		if r.b != nil {
			r.b.Close()
		}
		r.mu.Lock()
		r.m.Close()
		r.mu.Unlock()
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

// publish signs and sends one gossip body that mk builds for the sequence number the message will carry.
func (r *Room) publish(ctx context.Context, mk func(seq uint64) ([]byte, error)) (MessageID, error) {
	r.sendMu.Lock()
	defer r.sendMu.Unlock()
	seq := r.seq.Add(1)
	r.cur.Store(seq)
	body, err := mk(seq)
	if err != nil {
		return MessageID{}, err
	}
	id, err := r.gr.Publish(ctx, body)
	switch {
	case errors.Is(err, kologossip.ErrTooLarge):
		return MessageID{}, fmt.Errorf("%w: %w", ErrTooLarge, err)
	case errors.Is(err, kologossip.ErrNotJoined), errors.Is(err, kologossip.ErrClosed):
		return MessageID{}, ErrClosed
	}
	return id, err
}

// Send posts a message, as a reply when replyTo is not zero. It returns once the message is sent; the message shows in
// Next, here and on the other members, when the room has applied it.
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
	var ev Event
	id, err := r.publish(ctx, func(seq uint64) ([]byte, error) {
		now := r.n.now()
		c := &content{ReplyTo: replyTo, Parents: r.ord.parents(), Time: now.UnixMilli(), Body: body}
		nonce, ct, err := seal(&v.key, aad(r.gid[:], v.epoch, r.self, seq), c.encode())
		if err != nil {
			return nil, err
		}
		ev = Event{Kind: Message, ReplyTo: replyTo, Parents: c.Parents, Person: r.n.cfg.Cert.Person(), Device: r.self,
			Time: time.UnixMilli(c.Time), Body: slices.Clone(body), Epoch: v.epoch}
		return (&sealed{Epoch: v.epoch, Nonce: nonce, CT: ct}).encode(), nil
	})
	if err != nil {
		return MessageID{}, err
	}
	ev.ID = id
	r.release(func() []Event { return r.ord.addOwn(ev) })
	return id, nil
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

// History returns up to n events that come before the given message.
func (r *Room) History(ctx context.Context, before MessageID, n int) ([]Event, error) {
	return nil, ErrNotImplemented
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

// Promote gives a device the role Admin or Settler.
func (r *Room) Promote(ctx context.Context, device ed25519.PublicKey, role Role) error {
	return ErrNotImplemented
}

// SetPolicy changes the policy in the next Commit.
func (r *Room) SetPolicy(ctx context.Context, p Policy) error { return ErrNotImplemented }

// Link returns the read link of a room that has one.
func (r *Room) Link() ([]byte, error) { return nil, ErrNotImplemented }

// Leave removes this device from the room.
func (r *Room) Leave(ctx context.Context) error { return ErrNotImplemented }

// Status reports the room's state.
func (r *Room) Status() Status { return Status{Epoch: r.view.Load().epoch} }

// run reads the room's gossip until the room ends.
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
	switch m.Body[0] {
	case kindMessage:
		r.receive(m)
	case kindCommit:
		r.applyCommit(m)
	}
}

// receive opens a message the validator accepted and hands it to the orderer.
func (r *Room) receive(m *kologossip.Message) {
	ev := Event{Kind: Message, ID: m.ID, Person: m.Person, Device: m.Device, Time: m.Time}
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
	ev.ReplyTo, ev.Parents, ev.Time, ev.Body = c.ReplyTo, c.Parents, time.UnixMilli(c.Time), c.Body
	r.release(func() []Event { return r.ord.add(ev, r.n.now()) })
}

// applyCommit applies a settled Commit that an admin sent.
func (r *Room) applyCommit(m *kologossip.Message) {
	c, err := decodeCommit(m.Body)
	if err != nil {
		return
	}
	st, err := settle.DecodeSettled(c.Settled, 0)
	if err != nil {
		return
	}
	r.mu.Lock()
	if r.m.Removed() || r.m.Epoch() != st.TBS.Epoch {
		r.mu.Unlock()
		return
	}
	_, err = r.m.ApplySettled(c.Settled, c.Commit)
	var evs []Event
	if err == nil {
		evs = r.refreshLocked()
	} else {
		evs = []Event{{Kind: MembersChanged, Epoch: st.TBS.Epoch, Err: err}}
	}
	r.mu.Unlock()
	r.emit(evs...)
	r.finishRemoval()
}

// finishRemoval ends Next once a removed device's events are queued.
func (r *Room) finishRemoval() {
	if r.removed.Load() {
		r.endGone()
	}
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
	r.view.Store(v)
	r.notify()
	r.n.refreshMembers()
	return []Event{{Kind: MembersChanged, Epoch: v.epoch}}
}
