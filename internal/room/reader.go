package room

import (
	"context"
	"crypto/hkdf"
	"crypto/sha256"
	"errors"
	"fmt"
	"time"

	kolocodec "github.com/ciranda-dev/kolo-codec"
	kologossip "github.com/ciranda-dev/kolo-gossip"
	"github.com/ciranda-dev/kolo-mls/settle"
)

const (
	readLabel   = "kolo-group/read/v0"
	readADLabel = "kolo-group/read-state/v0"
	linkVersion = 0
)

// readKeyOf derives the key that seals the state records from the room's read secret.
func readKeyOf(secret *[keySize]byte) ([keySize]byte, error) {
	b, err := hkdf.Key(sha256.New, secret[:], nil, readLabel, keySize)
	if err != nil {
		return [keySize]byte{}, err
	}
	defer clear(b)
	return [keySize]byte(b), nil
}

// readState is what a reader needs of an epoch: its key, the roles and policy that came with it and the certificates of
// the admins and of the posters, the only authors a reader has to check.
type readState struct {
	Key     [keySize]byte
	Roles   []byte
	Policy  []byte
	Authors [][]byte // the person's key followed by the device's certificate
}

func (s *readState) encode() []byte {
	var e kolocodec.Encoder
	e.PutFixed(s.Key[:])
	e.PutBytes(s.Roles)
	e.PutBytes(s.Policy)
	e.PutUvarint(uint64(len(s.Authors)))
	for _, a := range s.Authors {
		e.PutBytes(a)
	}
	return e.Bytes()
}

func decodeReadState(b []byte) (*readState, error) {
	d := kolocodec.NewDecoder(b)
	s := &readState{}
	k, err := d.Fixed(keySize)
	if err != nil {
		return nil, fmt.Errorf("%w: state key", ErrMalformed)
	}
	copy(s.Key[:], k)
	if s.Roles, err = d.Bytes(d.Len()); err != nil {
		return nil, fmt.Errorf("%w: state roles", ErrMalformed)
	}
	if s.Policy, err = d.Bytes(d.Len()); err != nil {
		return nil, fmt.Errorf("%w: state policy", ErrMalformed)
	}
	n, err := d.UvarintMax(uint64(d.Len()))
	if err != nil {
		return nil, fmt.Errorf("%w: state authors", ErrMalformed)
	}
	for range n {
		a, err := d.Bytes(d.Len())
		if err != nil {
			return nil, fmt.Errorf("%w: state author", ErrMalformed)
		}
		s.Authors = append(s.Authors, a)
	}
	if err := d.Done(); err != nil {
		return nil, fmt.Errorf("%w: state trailing bytes", ErrMalformed)
	}
	return s, nil
}

func readAD(gid []byte, epoch uint64) []byte {
	var e kolocodec.Encoder
	e.PutString(readADLabel)
	e.PutBytes(gid)
	e.PutUvarint(epoch)
	return e.Bytes()
}

// sealRead seals the state of an epoch under the read key.
func sealRead(gid []byte, epoch uint64, rk *[keySize]byte, s *readState) ([]byte, error) {
	nonce, ct, err := seal(rk, readAD(gid, epoch), s.encode())
	if err != nil {
		return nil, err
	}
	return append(nonce[:], ct...), nil
}

// openRead returns the state that a record of epoch holds, or ErrOpen.
func openRead(gid []byte, epoch uint64, rk *[keySize]byte, b []byte) (*readState, error) {
	if len(b) <= nonceSize {
		return nil, fmt.Errorf("%w: state record", ErrMalformed)
	}
	pt, err := open(rk, [nonceSize]byte(b[:nonceSize]), readAD(gid, epoch), b[nonceSize:])
	if err != nil {
		return nil, err
	}
	return decodeReadState(pt)
}

// readLink is what a read link holds: the room's secret and read secret, the state of an epoch so a reader can start
// at once, and the headers of the admins' control logs, where the states of the later epochs are written.
type readLink struct {
	GID        [32]byte
	Secret     [keySize]byte
	ReadSecret [keySize]byte
	Name       string
	Epoch      uint64
	State      []byte
	Logs       [][]byte
}

func (l *readLink) encode() []byte {
	var e kolocodec.Encoder
	e.PutUvarint(linkVersion)
	e.PutFixed(l.GID[:])
	e.PutFixed(l.Secret[:])
	e.PutFixed(l.ReadSecret[:])
	e.PutString(l.Name)
	e.PutUvarint(l.Epoch)
	e.PutBytes(l.State)
	e.PutUvarint(uint64(len(l.Logs)))
	for _, h := range l.Logs {
		e.PutBytes(h)
	}
	return e.Bytes()
}

// decodeReadLink reads a link whose state is at most maxState bytes.
func decodeReadLink(b []byte, maxState int) (*readLink, error) {
	d := kolocodec.NewDecoder(b)
	if v, err := d.Uvarint(); err != nil || v != linkVersion {
		return nil, fmt.Errorf("%w: link version", ErrMalformed)
	}
	l := &readLink{}
	for _, f := range [][]byte{l.GID[:], l.Secret[:], l.ReadSecret[:]} {
		x, err := d.Fixed(len(f))
		if err != nil {
			return nil, fmt.Errorf("%w: link key", ErrMalformed)
		}
		copy(f, x)
	}
	var err error
	if l.Name, err = d.String(maxName); err != nil {
		return nil, fmt.Errorf("%w: link name", ErrMalformed)
	}
	if l.Epoch, err = d.Uvarint(); err != nil {
		return nil, fmt.Errorf("%w: link epoch", ErrMalformed)
	}
	if l.State, err = d.Bytes(maxState); err != nil {
		return nil, fmt.Errorf("%w: link state", ErrMalformed)
	}
	n, err := d.UvarintMax(uint64(d.Len()))
	if err != nil {
		return nil, fmt.Errorf("%w: link logs", ErrMalformed)
	}
	for range n {
		h, err := d.Bytes(maxHeader)
		if err != nil {
			return nil, fmt.Errorf("%w: link log header", ErrMalformed)
		}
		l.Logs = append(l.Logs, h)
	}
	if err := d.Done(); err != nil {
		return nil, fmt.Errorf("%w: link trailing bytes", ErrMalformed)
	}
	return l, nil
}

// readStateOf returns the state of the epoch of v: the certificates of its admins and of the devices that may post.
func readStateOf(v *view) *readState {
	s := &readState{Key: v.key, Roles: v.roles.Encode(), Policy: v.pol.encode()}
	for dev, a := range v.authors {
		if v.roles.Admin(dev[:]) || v.pol.mayPost(a.Person, dev[:], v.roles) {
			s.Authors = append(s.Authors, append(append([]byte(nil), a.Person...), a.Cert.Encode()...))
		}
	}
	return s
}

// readerView is the view of a reader at epoch from a state: it knows the admins and the posters and no other member.
func readerView(s *readState, epoch uint64, old *view, now time.Time, window time.Duration) (*view, error) {
	roles, err := settle.DecodeRoles(s.Roles, settle.Limits{})
	if err != nil {
		return nil, err
	}
	pol, err := decodeRoomPolicy(s.Policy)
	if err != nil {
		return nil, err
	}
	v := &view{epoch: epoch, key: s.Key, roles: roles, pol: pol, authors: map[[32]byte]*kologossip.Author{}}
	for _, raw := range s.Authors {
		a, err := decodeAuthor(raw)
		if err != nil {
			return nil, err
		}
		dev := [32]byte(a.Cert.Device())
		v.authors[dev] = a
		v.members = append(v.members, Member{Person: a.Person, Device: a.Cert.Device(), Role: roleOf(roles, dev[:])})
	}
	v.keepPrev(old, now, window)
	return v, nil
}

// writeRead writes the state of the epoch of v to this admin's control log, for the readers of the read link; a room
// without one writes nothing. It goes before any segment of the epoch, as the history link does.
func (r *Room) writeRead(v *view) error {
	r.logMu.Lock()
	admin := r.ctl != nil
	r.logMu.Unlock()
	if !admin || !v.pol.link {
		return nil
	}
	rk, err := readKeyOf(&v.pol.secret)
	if err != nil {
		return err
	}
	b, err := sealRead(r.gid[:], v.epoch, &rk, readStateOf(v))
	if err != nil {
		return err
	}
	if len(b) > r.n.limits.MaxMessageSize {
		return fmt.Errorf("%w: the state of epoch %d for the readers is %d bytes", ErrTooLarge, v.epoch, len(b))
	}
	return r.appendCtl(&ctlRecord{Kind: ctlRead, Epoch: v.epoch, Value: b})
}

// addRead takes the state of a later epoch that an admin's control log holds, if this room is a reader.
func (r *Room) addRead(rec *ctlRecord) {
	cur := r.view.Load()
	if !r.reader || rec.Epoch <= cur.epoch {
		return
	}
	s, err := openRead(r.gid[:], rec.Epoch, &r.readKey, rec.Value)
	var v *view
	if err == nil {
		v, err = readerView(s, rec.Epoch, cur, r.n.now(), r.n.limits.PastEpoch)
	}
	if err == nil {
		r.kmu.Lock()
		r.keys[v.epoch] = v.key
		r.kmu.Unlock()
		err = r.kv.put(v.key[:], recKey, epochKeyName(v.epoch))
	}
	if err == nil {
		err = errors.Join(r.notePolicy(v), r.rememberAuthors(v))
	}
	if err != nil {
		r.emit(Event{Kind: CatchingUp, Epoch: rec.Epoch, Err: fmt.Errorf("%w: the state of epoch %d: %w", ErrCatchUp, rec.Epoch, err)})
		return
	}
	r.view.Store(v)
	r.notify()
	r.n.refreshMembers()
	r.refreshPeers()
	r.emit(Event{Kind: MembersChanged, Epoch: v.epoch})
	r.kick()
}

// Link returns the read link of a room that has one: whoever holds it reads the room's messages and history and cannot
// post. The link stays the same until the room turns its read link off and on again.
func (r *Room) Link() ([]byte, error) {
	v := r.view.Load()
	switch {
	case r.removed.Load():
		return nil, ErrRemoved
	case r.reader:
		return nil, ErrNotMember
	case !v.pol.link:
		return nil, ErrNoLink
	}
	rk, err := readKeyOf(&v.pol.secret)
	if err != nil {
		return nil, err
	}
	state, err := sealRead(r.gid[:], v.epoch, &rk, readStateOf(v))
	if err != nil {
		return nil, err
	}
	return (&readLink{GID: r.gid, Secret: r.secret, ReadSecret: v.pol.secret, Name: r.name, Epoch: v.epoch, State: state, Logs: r.controlHeaders()}).encode(), nil
}

// JoinLink enters a room from its read link: the device reads the room live and its history as far as the room's history
// rule allows, and cannot post. The room is not kept: after a restart the device joins with the link again.
func (n *Node) JoinLink(ctx context.Context, link []byte) (*Room, error) {
	l, err := decodeReadLink(link, n.limits.MaxMessageSize)
	if err != nil {
		return nil, err
	}
	rk, err := readKeyOf(&l.ReadSecret)
	if err != nil {
		return nil, err
	}
	s, err := openRead(l.GID[:], l.Epoch, &rk, l.State)
	if err != nil {
		return nil, err
	}
	v, err := readerView(s, l.Epoch, nil, n.now(), n.limits.PastEpoch)
	if err != nil {
		return nil, err
	}
	if !v.pol.link || v.pol.secret != l.ReadSecret {
		return nil, fmt.Errorf("%w: the link's room has no read link", ErrMalformed)
	}
	return n.assemble(ctx, l.Name, GroupID(l.GID), nil, v, l.Secret, nil, l.Logs, &rk)
}
