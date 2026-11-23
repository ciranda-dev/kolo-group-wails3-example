package main

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/ciranda-dev/kolo-group-wails3-example/internal/room"
)

// The texts a person copies between devices: a key package, a welcome and a read link, each with a prefix that says which
// it is, so one pasted in the wrong place is refused with a clear error.
const (
	prefixKeyPackage = "kolo-group-key:"
	prefixWelcome    = "kolo-group-welcome:"
	prefixLink       = "kolo-group-link:"
)

var (
	errStarted = errors.New("kolo-group: the app is already started")
	errNotYet  = errors.New("kolo-group: the app has not started")
	errBusy    = errors.New("kolo-group: another action is still running")
)

// AppInfo is what Start returns: who this device is, the network, the pins and relays of the settings, and the rooms.
type AppInfo struct {
	Version string       `json:"version"`
	Person  string       `json:"person"` // this person's key in hex
	Device  string       `json:"device"` // this device's key in hex
	Network NetworkInfo  `json:"network"`
	Pins    []PinInfo    `json:"pins"`
	Relays  []RelayState `json:"relays"`
	From    string       `json:"relaysFrom"`
	Rooms   []RoomInfo   `json:"rooms"`
}

// PinInfo is one pin of the settings: where it listens and its public key in hex.
type PinInfo struct {
	Addr string `json:"addr"`
	Key  string `json:"key"`
}

// PolicyInfo is the policy of a room as the window shows and sets it. History is "all", "since" (from the epoch Since, or
// from the next one when Since is 0) or "none"; Posters is "all", "admins" or "chosen" (the persons in Persons, in hex).
type PolicyInfo struct {
	History  string   `json:"history"`
	Since    uint64   `json:"since"`
	Posters  string   `json:"posters"`
	Persons  []string `json:"persons"`
	ReadLink bool     `json:"readLink"`
}

// RoomInfo is one line of the room list.
type RoomInfo struct {
	ID      string     `json:"id"`
	Name    string     `json:"name"`
	Epoch   uint64     `json:"epoch"`
	Members int        `json:"members"` // devices
	Admin   bool       `json:"admin"`   // this device is an admin
	Reader  bool       `json:"reader"`  // joined with a read link: it reads and cannot post
	CanPost bool       `json:"canPost"`
	Policy  PolicyInfo `json:"policy"`
}

// MemberInfo is one device of a room. Role is "member", "admin" or "settler".
type MemberInfo struct {
	Person string `json:"person"`
	Device string `json:"device"`
	Role   string `json:"role"`
	Me     bool   `json:"me"`     // this device
	Mine   bool   `json:"mine"`   // a device of this person
	Poster bool   `json:"poster"` // the room lets it post
}

// MemberPage is the admins of a room and a page of its other devices; Total counts every device.
type MemberPage struct {
	Total  int          `json:"total"`
	Admins []MemberInfo `json:"admins"`
	Items  []MemberInfo `json:"items"`
}

// MessageInfo is a message of a room, or a message that could not be read (Error set).
type MessageInfo struct {
	ID      string `json:"id"`
	ReplyTo string `json:"replyTo"`
	Person  string `json:"person"`
	Device  string `json:"device"`
	Time    int64  `json:"time"` // milliseconds since 1970
	Body    string `json:"body"`
	Epoch   uint64 `json:"epoch"`
	Mine    bool   `json:"mine"`
	Error   string `json:"error"`
	Seq     uint64 `json:"seq"`    // the order this device was told of the message in, from 1; 0 for a message of History
	Stored  bool   `json:"stored"` // a segment of an admin holds the message
}

// EventInfo is the payload of the "group:event" event. Kind is "message", "stored" (a message of ours that a segment
// holds now), "members", "removed" or "catchup"; Message is set for the first two and for a catch-up fault.
type EventInfo struct {
	Room    string      `json:"room"`
	Kind    string      `json:"kind"`
	Message MessageInfo `json:"message"`
}

// HistoryPage is a page of the messages before the join.
type HistoryPage struct {
	Messages []MessageInfo `json:"messages"`
	Done     bool          `json:"done"` // no older message can be read
}

// StatusInfo is what the status page of a room shows.
type StatusInfo struct {
	Epoch        uint64 `json:"epoch"`
	CatchingUp   bool   `json:"catchingUp"`
	Receipts     int    `json:"receipts"`
	Logs         int    `json:"logs"`
	AdminsOnline int    `json:"adminsOnline"`
	Peers        int    `json:"peers"`
	Online       bool   `json:"online"`
	NetError     string `json:"netError"`
	PinError     string `json:"pinError"`
}

// GroupService is the Wails service the window calls.
type GroupService struct {
	cfg  Config
	emit func(name string, data any) // tells the window; the app sets it
	tune tuning                      // tests only

	mu     sync.Mutex
	d      *device
	cancel context.CancelFunc // of the action that runs, if any
}

// NewGroupService returns the service for cfg.
func NewGroupService(cfg Config) *GroupService { return &GroupService{cfg: cfg} }

// Version returns the release the app reports.
func (s *GroupService) Version() string { return version }

// Start opens the data folder and starts the device. The window calls it once.
func (s *GroupService) Start() (AppInfo, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.d != nil {
		return AppInfo{}, errStarted
	}
	set, err := s.cfg.resolve()
	if err != nil {
		return AppInfo{}, err
	}
	if s.d, err = openDevice(context.Background(), set, s.emit, s.tune); err != nil {
		s.d = nil
		return AppInfo{}, err
	}
	return s.d.info(), nil
}

// ServiceShutdown stops the device when the app quits.
func (s *GroupService) ServiceShutdown() error {
	s.mu.Lock()
	d := s.d
	s.d = nil
	s.mu.Unlock()
	if d == nil {
		return nil
	}
	return d.Close()
}

func (s *GroupService) device() (*device, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.d == nil {
		return nil, errNotYet
	}
	return s.d, nil
}

// action runs f, which may wait for other devices, with a context that CancelAction ends. One action runs at a time.
func (s *GroupService) action(f func(ctx context.Context, d *device) error) error {
	d, err := s.device()
	if err != nil {
		return err
	}
	end, err := d.begin()
	if err != nil {
		return err
	}
	defer end()
	ctx, cancel := context.WithCancel(d.ctx)
	defer cancel()
	s.mu.Lock()
	if s.cancel != nil {
		s.mu.Unlock()
		return errBusy
	}
	s.cancel = cancel
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.cancel = nil
		s.mu.Unlock()
	}()
	return f(ctx, d)
}

// CancelAction ends the action that waits, such as a join that looks for the devices of its room.
func (s *GroupService) CancelAction() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cancel != nil {
		s.cancel()
	}
}

// Rooms returns the rooms of this device.
func (s *GroupService) Rooms() ([]RoomInfo, error) {
	d, err := s.device()
	if err != nil {
		return nil, err
	}
	return d.rooms(), nil
}

// CreateRoom makes a room with this device as its admin.
func (s *GroupService) CreateRoom(name string, policy PolicyInfo) (info RoomInfo, err error) {
	err = s.action(func(ctx context.Context, d *device) error {
		name = strings.TrimSpace(name)
		if name == "" {
			return errors.New("kolo-group: the room needs a name")
		}
		p, err := d.policy(nil, policy)
		if err != nil {
			return err
		}
		r, err := d.node.Create(ctx, name, p)
		if err != nil {
			return err
		}
		d.watch(r)
		info = d.roomInfo(r)
		return nil
	})
	return info, err
}

// KeyPackage returns a new key package of this device, to give to an admin who adds it to a room. Each is used once.
func (s *GroupService) KeyPackage() (string, error) {
	d, err := s.device()
	if err != nil {
		return "", err
	}
	return d.keyPackage()
}

// AddMembers adds the devices of the key packages to the room and returns the welcome for all of them.
func (s *GroupService) AddMembers(roomID string, keyPackages []string) (welcome string, err error) {
	err = s.action(func(ctx context.Context, d *device) error {
		r, err := d.room(roomID)
		if err != nil {
			return err
		}
		var kps [][]byte
		for _, text := range keyPackages {
			if text = strings.TrimSpace(text); text == "" {
				continue
			}
			kp, err := decodeText(prefixKeyPackage, text)
			if err != nil {
				return err
			}
			kps = append(kps, kp)
		}
		if len(kps) == 0 {
			return errors.New("kolo-group: give at least one key package")
		}
		w, err := r.Add(ctx, kps...)
		if err != nil {
			return err
		}
		welcome = prefixWelcome + base64.RawURLEncoding.EncodeToString(w)
		return nil
	})
	return welcome, err
}

// JoinRoom enters the room of a welcome that an admin made for a key package of this device. A join that waits for the
// devices of the room can be stopped with CancelAction.
func (s *GroupService) JoinRoom(welcome string) (info RoomInfo, err error) {
	err = s.action(func(ctx context.Context, d *device) error {
		w, err := decodeText(prefixWelcome, welcome)
		if err != nil {
			return err
		}
		r, err := d.node.Join(ctx, w)
		if err != nil {
			return err
		}
		d.watch(r)
		info = d.roomInfo(r)
		return nil
	})
	return info, err
}

// JoinLink enters a room with a read link: the device reads and cannot post. A restart does not keep the room.
func (s *GroupService) JoinLink(link string) (info RoomInfo, err error) {
	err = s.action(func(ctx context.Context, d *device) error {
		l, err := decodeText(prefixLink, link)
		if err != nil {
			return err
		}
		r, err := d.node.JoinLink(ctx, l)
		if err != nil {
			return err
		}
		d.mu.Lock()
		d.readers[r.ID()] = true
		d.mu.Unlock()
		d.watch(r)
		info = d.roomInfo(r)
		return nil
	})
	return info, err
}

// RoomLink returns the read link of a room that has one.
func (s *GroupService) RoomLink(roomID string) (string, error) {
	d, err := s.device()
	if err != nil {
		return "", err
	}
	r, err := d.room(roomID)
	if err != nil {
		return "", err
	}
	l, err := r.Link()
	if err != nil {
		return "", err
	}
	return prefixLink + base64.RawURLEncoding.EncodeToString(l), nil
}

// Send posts a message to the room, as a reply when replyTo is a message id.
func (s *GroupService) Send(roomID, body, replyTo string) (string, error) {
	d, err := s.device()
	if err != nil {
		return "", err
	}
	r, err := d.room(roomID)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(body) == "" {
		return "", errors.New("kolo-group: the message is empty")
	}
	var reply room.MessageID
	if replyTo != "" {
		if reply, err = parseID(replyTo); err != nil {
			return "", err
		}
	}
	id, err := r.Send(d.ctx, []byte(body), reply)
	return hex.EncodeToString(id[:]), err
}

// Messages returns up to n of the messages this device was told of in the room, numbered below before (0 for the newest), oldest first.
func (s *GroupService) Messages(roomID string, before uint64, n int) ([]MessageInfo, error) {
	d, err := s.device()
	if err != nil {
		return nil, err
	}
	if _, err := d.room(roomID); err != nil {
		return nil, err
	}
	return d.msgs.page(roomID, before, n)
}

// History returns up to n messages older than before (the id of the oldest message shown; empty for the newest), oldest first.
func (s *GroupService) History(roomID, before string, n int) (HistoryPage, error) {
	d, err := s.device()
	if err != nil {
		return HistoryPage{}, err
	}
	r, err := d.room(roomID)
	if err != nil {
		return HistoryPage{}, err
	}
	var from room.MessageID
	if before != "" {
		if from, err = parseID(before); err != nil {
			return HistoryPage{}, err
		}
	}
	evs, err := r.History(d.ctx, from, n)
	page := HistoryPage{Messages: []MessageInfo{}}
	for _, ev := range evs {
		page.Messages = append(page.Messages, messageInfo(ev, d.person.PublicKey()))
	}
	if errors.Is(err, room.ErrHistory) {
		page.Done, err = true, nil
	}
	page.Done = page.Done || err == nil && len(evs) == 0
	return page, err
}

// Members returns the admins of the room and up to n of its other devices from offset on (all of them when n is 0).
func (s *GroupService) Members(roomID string, offset, n int) (MemberPage, error) {
	d, err := s.device()
	if err != nil {
		return MemberPage{}, err
	}
	r, err := d.room(roomID)
	if err != nil {
		return MemberPage{}, err
	}
	return d.members(r, offset, n), nil
}

// Promote makes an online member device an admin: it then writes segments and commits too.
func (s *GroupService) Promote(roomID, deviceKey string) error {
	return s.roomAction(roomID, func(ctx context.Context, _ *device, r *room.Room) error {
		key, err := parseKey(deviceKey)
		if err != nil {
			return err
		}
		return r.Promote(ctx, key, room.Admin)
	})
}

// Remove removes every device of a person from the room.
func (s *GroupService) Remove(roomID, person string) error {
	return s.roomAction(roomID, func(ctx context.Context, _ *device, r *room.Room) error {
		key, err := parseKey(person)
		if err != nil {
			return err
		}
		return r.Remove(ctx, key)
	})
}

// SetPolicy changes who may post, the history rule and the read link, for the epochs from the next on.
func (s *GroupService) SetPolicy(roomID string, policy PolicyInfo) error {
	return s.roomAction(roomID, func(ctx context.Context, d *device, r *room.Room) error {
		p, err := d.policy(r, policy)
		if err != nil {
			return err
		}
		return r.SetPolicy(ctx, p)
	})
}

// Leave asks the admins to remove this person and stops the room on this device. It waits for an admin; CancelAction ends the wait.
func (s *GroupService) Leave(roomID string) error {
	return s.roomAction(roomID, func(ctx context.Context, d *device, r *room.Room) error {
		err := r.Leave(ctx)
		if err == nil {
			d.markGone(r.ID())
		}
		return err
	})
}

// RoomStatus reports the state of a room and of the network.
func (s *GroupService) RoomStatus(roomID string) (StatusInfo, error) {
	d, err := s.device()
	if err != nil {
		return StatusInfo{}, err
	}
	r, err := d.room(roomID)
	if err != nil {
		return StatusInfo{}, err
	}
	st := r.Status()
	out := StatusInfo{Epoch: st.Epoch, CatchingUp: st.CatchingUp, Receipts: st.Receipts, Logs: st.Logs, AdminsOnline: st.AdminsOnline, Peers: d.Peers()}
	out.Online, out.NetError = d.netStatus()
	if st.PinError != nil {
		out.PinError = st.PinError.Error()
	}
	return out, nil
}

func (s *GroupService) roomAction(roomID string, f func(ctx context.Context, d *device, r *room.Room) error) error {
	return s.action(func(ctx context.Context, d *device) error {
		r, err := d.room(roomID)
		if err != nil {
			return err
		}
		return f(ctx, d, r)
	})
}

// decodeText reads a text a person copied: the prefix says what it is, the rest is base64.
func decodeText(prefix, text string) ([]byte, error) {
	text = strings.Join(strings.Fields(text), "")
	rest, ok := strings.CutPrefix(text, prefix)
	if !ok {
		return nil, fmt.Errorf("kolo-group: this is not a %s text: it must start with %s", strings.TrimSuffix(strings.TrimPrefix(prefix, "kolo-group-"), ":"), prefix)
	}
	b, err := base64.RawURLEncoding.DecodeString(rest)
	if err != nil || len(b) == 0 {
		return nil, fmt.Errorf("kolo-group: the text after %s is damaged: copy it again whole", prefix)
	}
	return b, nil
}

func parseKey(s string) (ed25519.PublicKey, error) {
	b, err := hex.DecodeString(s)
	if err != nil || len(b) != ed25519.PublicKeySize {
		return nil, errors.New("kolo-group: a key is 64 hex characters")
	}
	return b, nil
}

func parseID(s string) (id room.MessageID, err error) {
	b, err := hex.DecodeString(s)
	if err != nil || len(b) != len(id) {
		return id, errors.New("kolo-group: a message id is 64 hex characters")
	}
	return room.MessageID(b), nil
}
