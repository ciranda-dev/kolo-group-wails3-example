package room

import (
	"bytes"
	"fmt"

	kolocodec "github.com/ciranda-dev/kolo-codec"
)

// The first byte of a gossip body names its kind.
const (
	kindMessage byte = 1
	kindCommit  byte = 2
)

const idSize = 32

// sealed is a message body as it travels: the epoch it is sealed in, the nonce and the ciphertext.
type sealed struct {
	Epoch uint64
	Nonce [nonceSize]byte
	CT    []byte
}

func (s *sealed) encode() []byte {
	var e kolocodec.Encoder
	e.PutFixed([]byte{kindMessage})
	e.PutUvarint(s.Epoch)
	e.PutFixed(s.Nonce[:])
	e.PutFixed(s.CT)
	return e.Bytes()
}

func decodeSealed(b []byte) (*sealed, error) {
	d := kolocodec.NewDecoder(b)
	k, err := d.Fixed(1)
	if err != nil || k[0] != kindMessage {
		return nil, fmt.Errorf("%w: kind", ErrMalformed)
	}
	s := &sealed{}
	if s.Epoch, err = d.Uvarint(); err != nil {
		return nil, fmt.Errorf("%w: epoch", ErrMalformed)
	}
	n, err := d.Fixed(nonceSize)
	if err != nil {
		return nil, fmt.Errorf("%w: nonce", ErrMalformed)
	}
	copy(s.Nonce[:], n)
	if s.CT, err = d.Fixed(d.Len()); err != nil || len(s.CT) == 0 {
		return nil, fmt.Errorf("%w: ciphertext", ErrMalformed)
	}
	return s, nil
}

// content is what a sealed message holds: what it answers, what its author had shown, its time and its text.
type content struct {
	ReplyTo MessageID
	Parents []MessageID
	Time    int64 // Unix milliseconds
	Body    []byte
}

// encode returns the content padded with zeros to a Padme length.
func (c *content) encode() []byte {
	var e kolocodec.Encoder
	e.PutFixed(c.ReplyTo[:])
	e.PutUvarint(uint64(len(c.Parents)))
	for _, p := range c.Parents {
		e.PutFixed(p[:])
	}
	e.PutVarint(c.Time)
	e.PutBytes(c.Body)
	b := e.Bytes()
	return append(b, make([]byte, padme(len(b))-len(b))...)
}

func decodeContent(b []byte) (*content, error) {
	d := kolocodec.NewDecoder(b)
	c := &content{}
	r, err := d.Fixed(idSize)
	if err != nil {
		return nil, fmt.Errorf("%w: reply", ErrMalformed)
	}
	copy(c.ReplyTo[:], r)
	n, err := d.Uvarint()
	if err != nil || n > uint64(d.Len()/idSize) {
		return nil, fmt.Errorf("%w: parents", ErrMalformed)
	}
	for range n {
		p, err := d.Fixed(idSize)
		if err != nil {
			return nil, fmt.Errorf("%w: parent", ErrMalformed)
		}
		c.Parents = append(c.Parents, MessageID(p))
	}
	if c.Time, err = d.Varint(); err != nil {
		return nil, fmt.Errorf("%w: time", ErrMalformed)
	}
	if c.Body, err = d.Bytes(d.Len()); err != nil {
		return nil, fmt.Errorf("%w: body", ErrMalformed)
	}
	rest, _ := d.Fixed(d.Len())
	if len(bytes.TrimLeft(rest, "\x00")) != 0 {
		return nil, fmt.Errorf("%w: padding", ErrMalformed)
	}
	return c, nil
}

// commitMsg carries a SettledCommit with its Commit, for the members to apply.
type commitMsg struct {
	Settled, Commit []byte
}

func (c *commitMsg) encode() []byte {
	var e kolocodec.Encoder
	e.PutFixed([]byte{kindCommit})
	e.PutBytes(c.Settled)
	e.PutBytes(c.Commit)
	return e.Bytes()
}

func decodeCommit(b []byte) (*commitMsg, error) {
	d := kolocodec.NewDecoder(b)
	k, err := d.Fixed(1)
	if err != nil || k[0] != kindCommit {
		return nil, fmt.Errorf("%w: kind", ErrMalformed)
	}
	c := &commitMsg{}
	if c.Settled, err = d.Bytes(d.Len()); err != nil {
		return nil, fmt.Errorf("%w: settled commit", ErrMalformed)
	}
	if c.Commit, err = d.Bytes(d.Len()); err != nil {
		return nil, fmt.Errorf("%w: commit", ErrMalformed)
	}
	if d.Len() != 0 || len(c.Settled) == 0 || len(c.Commit) == 0 {
		return nil, fmt.Errorf("%w: commit message", ErrMalformed)
	}
	return c, nil
}

// invite is what an admin hands a new member next to its Welcome: everything the joiner needs and the Welcome lacks.
type invite struct {
	Welcome, Settled, Tree []byte
	Secret                 [keySize]byte
	Name                   string
}

const (
	inviteVersion = 0
	maxName       = 256
	maxWelcome    = 1 << 20
	maxSettled    = 1 << 20
)

func (v *invite) encode() []byte {
	var e kolocodec.Encoder
	e.PutUvarint(inviteVersion)
	e.PutBytes(v.Welcome)
	e.PutBytes(v.Settled)
	e.PutBytes(v.Tree)
	e.PutFixed(v.Secret[:])
	e.PutString(v.Name)
	return e.Bytes()
}

// decodeInvite reads an invite whose tree is at most maxTree bytes.
func decodeInvite(b []byte, maxTree int) (*invite, error) {
	d := kolocodec.NewDecoder(b)
	if ver, err := d.Uvarint(); err != nil || ver != inviteVersion {
		return nil, fmt.Errorf("%w: invite version", ErrMalformed)
	}
	v := &invite{}
	var err error
	if v.Welcome, err = d.Bytes(maxWelcome); err != nil {
		return nil, fmt.Errorf("%w: welcome", ErrMalformed)
	}
	if v.Settled, err = d.Bytes(maxSettled); err != nil {
		return nil, fmt.Errorf("%w: settled commit", ErrMalformed)
	}
	if v.Tree, err = d.Bytes(maxTree + 64); err != nil {
		return nil, fmt.Errorf("%w: tree", ErrMalformed)
	}
	s, err := d.Fixed(keySize)
	if err != nil {
		return nil, fmt.Errorf("%w: secret", ErrMalformed)
	}
	copy(v.Secret[:], s)
	if v.Name, err = d.String(maxName); err != nil {
		return nil, fmt.Errorf("%w: name", ErrMalformed)
	}
	if err := d.Done(); err != nil {
		return nil, fmt.Errorf("%w: trailing bytes", ErrMalformed)
	}
	return v, nil
}
