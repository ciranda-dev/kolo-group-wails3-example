// Package room is the group room of Kolo Group: the narrow API of decision 0024 section 9. Every method returns
// ErrNotImplemented until the room is built.
package room

import (
	"context"
	"crypto/ed25519"
	"errors"
	"net/netip"
	"time"

	kologossip "github.com/ciranda-dev/kolo-gossip"
	koloid "github.com/ciranda-dev/kolo-id"
	kolomls "github.com/ciranda-dev/kolo-mls"
	"github.com/ciranda-dev/kolo-pin"
	kolostore "github.com/ciranda-dev/kolo-store"
	koloswarm "github.com/ciranda-dev/kolo-swarm"
)

// ErrNotImplemented is returned by every method that is not built yet.
var ErrNotImplemented = errors.New("room: not implemented")

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

// The kinds of Event.
const (
	Message EventKind = iota
	Stored
	MembersChanged
	Removed
	CatchingUp
)

// Limits holds the configurable safety bounds of a node; a zero field takes its documented default.
type Limits struct{}

// Config is what Open needs.
type Config struct {
	Device   *koloid.Identity
	Cert     *koloid.DeviceCert
	Swarm    *koloswarm.Swarm
	Gossip   *kologossip.Gossip
	Store    *kolostore.Store
	MLSStore kolomls.Store
	Pins     []pin.Peer
	Now      func() time.Time
	Limits   Limits
}

// Policy is the room setting that a Commit changes.
type Policy struct {
	History  HistoryRule
	Since    uint64              // with SinceEpoch
	Posters  []ed25519.PublicKey // persons; empty means every member
	ReadLink bool
}

// Member is one person of a room.
type Member struct {
	Person ed25519.PublicKey
	Device ed25519.PublicKey
	Role   Role
}

// Status is what a room reports about itself.
type Status struct {
	Epoch uint64
}

// Event is something that happened in a room.
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

// Node holds the rooms of one device.
type Node struct{}

// Open starts a node.
func Open(ctx context.Context, cfg *Config) (*Node, error) { return nil, ErrNotImplemented }

// ServeConn serves one connection; a tag byte on each stream names its protocol.
func (n *Node) ServeConn(ctx context.Context, conn koloswarm.Conn, outbound bool) error {
	return ErrNotImplemented
}

// Allow is the admission rule for koloswarm.Config.Allow.
func (n *Node) Allow(key ed25519.PublicKey, addr netip.AddrPort) bool { return false }

// KeyPackage returns a key package that an admin can add to a room.
func (n *Node) KeyPackage() ([]byte, error) { return nil, ErrNotImplemented }

// Create makes a room with this device as its first member.
func (n *Node) Create(ctx context.Context, name string, p Policy) (*Room, error) {
	return nil, ErrNotImplemented
}

// Join enters a room from a welcome.
func (n *Node) Join(ctx context.Context, welcome []byte) (*Room, error) {
	return nil, ErrNotImplemented
}

// JoinLink enters a room that has a read link; the device only reads.
func (n *Node) JoinLink(ctx context.Context, link []byte) (*Room, error) {
	return nil, ErrNotImplemented
}

// Room returns the room with the given id.
func (n *Node) Room(id GroupID) (*Room, error) { return nil, ErrNotImplemented }

// Rooms returns every room of the node.
func (n *Node) Rooms() []*Room { return nil }

// Close stops the node.
func (n *Node) Close() error { return ErrNotImplemented }

// Room is one group.
type Room struct{}

// Send posts a message, as a reply when replyTo is not zero.
func (r *Room) Send(ctx context.Context, body []byte, replyTo MessageID) (MessageID, error) {
	return MessageID{}, ErrNotImplemented
}

// Next waits for the next event of the room.
func (r *Room) Next(ctx context.Context) (Event, error) { return Event{}, ErrNotImplemented }

// History returns up to n events that come before the given message.
func (r *Room) History(ctx context.Context, before MessageID, n int) ([]Event, error) {
	return nil, ErrNotImplemented
}

// Members lists the room's members.
func (r *Room) Members() []Member { return nil }

// Add adds the owners of the key packages and returns the welcome for them.
func (r *Room) Add(ctx context.Context, keyPackages ...[]byte) (welcome []byte, err error) {
	return nil, ErrNotImplemented
}

// Remove removes a person from the room.
func (r *Room) Remove(ctx context.Context, person ed25519.PublicKey) error {
	return ErrNotImplemented
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
func (r *Room) Status() Status { return Status{} }
