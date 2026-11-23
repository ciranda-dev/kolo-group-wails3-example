package main

import (
	"cmp"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"slices"

	"github.com/ciranda-dev/kolo-group-wails3-example/internal/room"
)

// info is what the window gets when it starts.
func (d *device) info() AppInfo {
	d.mu.Lock()
	relays := slices.Clone(d.relays)
	d.mu.Unlock()
	pins := []PinInfo{}
	for _, p := range d.set.pins {
		pins = append(pins, PinInfo{Addr: p.Addr, Key: hex.EncodeToString(p.Key)})
	}
	if relays == nil {
		relays = []RelayState{}
	}
	return AppInfo{Version: version, Person: hex.EncodeToString(d.person.PublicKey()), Device: hex.EncodeToString(d.key.PublicKey()),
		Network: d.set.network(), Pins: pins, Relays: relays, From: d.set.relaysFrom, Rooms: d.rooms()}
}

func (d *device) rooms() []RoomInfo {
	out := []RoomInfo{}
	for _, r := range d.node.Rooms() {
		if !d.isGone(r.ID()) {
			out = append(out, d.roomInfo(r))
		}
	}
	slices.SortFunc(out, func(a, b RoomInfo) int {
		if a.Name != b.Name {
			return cmp.Compare(a.Name, b.Name)
		}
		return cmp.Compare(a.ID, b.ID)
	})
	return out
}

// keyPackage returns a key package of this device as the text an admin adds.
func (d *device) keyPackage() (string, error) {
	kp, err := d.node.KeyPackage()
	if err != nil {
		return "", err
	}
	return prefixKeyPackage + base64.RawURLEncoding.EncodeToString(kp), nil
}

// join enters the room of a welcome and starts to deliver its events.
func (d *device) join(welcome []byte) (*room.Room, error) {
	end, err := d.begin()
	if err != nil {
		return nil, err
	}
	defer end()
	r, err := d.node.Join(d.ctx, welcome)
	if err != nil {
		return nil, err
	}
	d.watch(r)
	return r, nil
}

// room returns the room with the hex id.
func (d *device) room(id string) (*room.Room, error) {
	b, err := hex.DecodeString(id)
	if err != nil || len(b) != len(room.GroupID{}) {
		return nil, errors.New("kolo-group: a room id is 64 hex characters")
	}
	if d.isGone(room.GroupID(b)) {
		return nil, room.ErrRemoved
	}
	return d.node.Room(room.GroupID(b))
}

func (d *device) isGone(id room.GroupID) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.gone[id]
}

func (d *device) roomInfo(r *room.Room) RoomInfo {
	id := r.ID()
	d.mu.Lock()
	reader := d.readers[id]
	d.mu.Unlock()
	members := r.Members()
	pol := r.Policy()
	info := RoomInfo{ID: hex.EncodeToString(id[:]), Name: r.Name(), Epoch: r.Status().Epoch, Members: len(members), Reader: reader, Policy: policyInfo(pol)}
	me := d.key.PublicKey()
	for _, m := range members {
		if m.Device.Equal(me) {
			info.Admin = m.Role >= room.Admin
		}
	}
	info.CanPost = !reader && canPost(pol, d.person.PublicKey(), me, info.Admin)
	return info
}

// canPost applies the room policy to this device.
func canPost(p room.Policy, person, dev ed25519.PublicKey, admin bool) bool {
	if len(p.Posters) == 0 && len(p.PosterDevices) == 0 && !p.AdminsPost {
		return true
	}
	return admin && p.AdminsPost ||
		slices.ContainsFunc(p.Posters, func(k ed25519.PublicKey) bool { return k.Equal(person) }) ||
		slices.ContainsFunc(p.PosterDevices, func(k ed25519.PublicKey) bool { return k.Equal(dev) })
}

// members returns the admins and a page of the other devices of the room, in the order of the tree.
func (d *device) members(r *room.Room, offset, n int) MemberPage {
	pol := r.Policy()
	page := MemberPage{Admins: []MemberInfo{}, Items: []MemberInfo{}}
	others := 0
	for _, m := range r.Members() {
		page.Total++
		role := "member"
		switch m.Role {
		case room.Settler:
			role = "settler"
		case room.Admin:
			role = "admin"
		}
		info := MemberInfo{Person: hex.EncodeToString(m.Person), Device: hex.EncodeToString(m.Device), Role: role,
			Me: m.Device.Equal(d.key.PublicKey()), Mine: m.Person.Equal(d.person.PublicKey()), Poster: canPost(pol, m.Person, m.Device, m.Role >= room.Admin)}
		if m.Role >= room.Admin {
			page.Admins = append(page.Admins, info)
			continue
		}
		if others++; others > offset && (n <= 0 || len(page.Items) < n) {
			page.Items = append(page.Items, info)
		}
	}
	return page
}

func policyInfo(p room.Policy) PolicyInfo {
	out := PolicyInfo{History: "all", Posters: "all", Persons: []string{}, ReadLink: p.ReadLink}
	switch p.History {
	case room.SinceEpoch:
		out.History, out.Since = "since", p.Since
	case room.NoHistory:
		out.History = "none"
	}
	switch {
	case len(p.Posters) > 0 || len(p.PosterDevices) > 0:
		out.Posters = "chosen"
		for _, k := range p.Posters {
			out.Persons = append(out.Persons, hex.EncodeToString(k))
		}
	case p.AdminsPost:
		out.Posters = "admins"
	}
	return out
}

// policy turns what the window sends into a room.Policy. For a room that exists, "since" with no epoch means from the
// epoch the Commit that sets it makes.
func (d *device) policy(r *room.Room, in PolicyInfo) (room.Policy, error) {
	p := room.Policy{ReadLink: in.ReadLink}
	switch in.History {
	case "all", "":
	case "since":
		p.History, p.Since = room.SinceEpoch, in.Since
		if p.Since == 0 && r != nil {
			p.Since = r.Status().Epoch + 1
		}
	case "none":
		p.History = room.NoHistory
	default:
		return p, errors.New("kolo-group: the history rule is all, since or none")
	}
	switch in.Posters {
	case "all", "":
	case "admins":
		p.AdminsPost = true
	case "chosen":
		for _, s := range in.Persons {
			k, err := parseKey(s)
			if err != nil {
				return p, err
			}
			p.Posters = append(p.Posters, k)
		}
		if len(p.Posters) == 0 {
			return p, errors.New("kolo-group: choose at least one person who may post")
		}
	default:
		return p, errors.New("kolo-group: who may post is all, admins or chosen")
	}
	return p, nil
}

func messageInfo(ev room.Event, person ed25519.PublicKey) MessageInfo {
	m := MessageInfo{ID: idText(ev.ID), ReplyTo: idText(ev.ReplyTo), Person: hex.EncodeToString(ev.Person), Device: hex.EncodeToString(ev.Device),
		Body: string(ev.Body), Epoch: ev.Epoch, Mine: len(person) > 0 && ev.Person.Equal(person)}
	if !ev.Time.IsZero() {
		m.Time = ev.Time.UnixMilli()
	}
	if ev.Err != nil {
		m.Error = ev.Err.Error()
	}
	return m
}

// idText is the hex of a message id, empty for the zero id.
func idText(id room.MessageID) string {
	if id == (room.MessageID{}) {
		return ""
	}
	return hex.EncodeToString(id[:])
}

func eventInfo(id room.GroupID, ev room.Event, person ed25519.PublicKey) EventInfo {
	out := EventInfo{Room: hex.EncodeToString(id[:]), Message: messageInfo(ev, person)}
	switch ev.Kind {
	case room.Message:
		out.Kind = "message"
	case room.Stored:
		out.Kind = "stored"
	case room.MembersChanged:
		out.Kind = "members"
	case room.Removed:
		out.Kind = "removed"
	default:
		out.Kind = "catchup"
	}
	return out
}
