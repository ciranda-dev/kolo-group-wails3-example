package room

import (
	"bytes"
	"errors"
	"fmt"

	kolobraid "github.com/ciranda-dev/kolo-braid"
	kolocodec "github.com/ciranda-dev/kolo-codec"
)

// The first byte of a gossip body names its kind.
const (
	kindMessage      byte = 1
	kindCommit       byte = 2
	kindPromote      byte = 3
	kindAdminRequest byte = 4
	kindAdminWelcome byte = 5
	kindLeave        byte = 6 // a member asks the admins to remove its person
)

const idSize = 32

const sigSize = 64

// sealed is a message body as it travels: the epoch it is sealed in, the nonce, the author's signature and the ciphertext.
type sealed struct {
	Epoch uint64
	Nonce [nonceSize]byte
	Sig   [sigSize]byte
	CT    []byte
}

func (s *sealed) encode() []byte {
	var e kolocodec.Encoder
	e.PutFixed([]byte{kindMessage})
	e.PutUvarint(s.Epoch)
	e.PutFixed(s.Nonce[:])
	e.PutFixed(s.Sig[:])
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
	sg, err := d.Fixed(sigSize)
	if err != nil {
		return nil, fmt.Errorf("%w: signature", ErrMalformed)
	}
	copy(s.Sig[:], sg)
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

// commitRef announces a settled Commit: the SettledCommit, which names the Commit by its hash and carries the settlers'
// signatures. The Commit itself is pulled from the control logs.
type commitRef struct{ Settled []byte }

func (c *commitRef) encode() []byte {
	var e kolocodec.Encoder
	e.PutFixed([]byte{kindCommit})
	e.PutFixed(c.Settled)
	return e.Bytes()
}

func decodeCommitRef(b []byte) (*commitRef, error) {
	if len(b) < 2 || b[0] != kindCommit {
		return nil, fmt.Errorf("%w: commit announcement", ErrMalformed)
	}
	return &commitRef{Settled: b[1:]}, nil
}

// invite is what an admin hands a new member next to its Welcome: everything the joiner needs and the Welcome lacks. The
// sealed tree is not in it: the joiner pulls it from the log that TreeHeader names, which holds TreeEntries pieces of a
// tree with hash TreeHash.
type invite struct {
	Welcome, Settled []byte
	TreeHeader       []byte
	TreeEntries      uint64
	TreeHash         [32]byte
	Secret           [keySize]byte
	Name             string
	Logs             [][]byte // the headers of the admins' control logs
}

const (
	inviteVersion = 0
	maxName       = 256
	maxWelcome    = 1 << 20
	maxSettled    = 1 << 20
	maxHeader     = 512
)

func (v *invite) encode() []byte {
	var e kolocodec.Encoder
	e.PutUvarint(inviteVersion)
	e.PutBytes(v.Welcome)
	e.PutBytes(v.Settled)
	e.PutBytes(v.TreeHeader)
	e.PutUvarint(v.TreeEntries)
	e.PutFixed(v.TreeHash[:])
	e.PutFixed(v.Secret[:])
	e.PutString(v.Name)
	e.PutUvarint(uint64(len(v.Logs)))
	for _, h := range v.Logs {
		e.PutBytes(h)
	}
	return e.Bytes()
}

// decodeInvite reads an invite whose sealed tree is at most maxTree bytes, which bounds the pieces it names.
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
	if v.TreeHeader, err = d.Bytes(maxHeader); err != nil {
		return nil, fmt.Errorf("%w: tree log", ErrMalformed)
	}
	if v.TreeEntries, err = d.Uvarint(); err != nil || v.TreeEntries > uint64(maxTree/maxChunk)+2 {
		return nil, fmt.Errorf("%w: tree pieces", ErrMalformed)
	}
	h, err := d.Fixed(32)
	if err != nil {
		return nil, fmt.Errorf("%w: tree hash", ErrMalformed)
	}
	copy(v.TreeHash[:], h)
	s, err := d.Fixed(keySize)
	if err != nil {
		return nil, fmt.Errorf("%w: secret", ErrMalformed)
	}
	copy(v.Secret[:], s)
	if v.Name, err = d.String(maxName); err != nil {
		return nil, fmt.Errorf("%w: name", ErrMalformed)
	}
	n, err := d.Uvarint()
	if err != nil || n > uint64(d.Len()) {
		return nil, fmt.Errorf("%w: logs", ErrMalformed)
	}
	for range n {
		h, err := d.Bytes(maxHeader)
		if err != nil {
			return nil, fmt.Errorf("%w: log header", ErrMalformed)
		}
		v.Logs = append(v.Logs, h)
	}
	if err := d.Done(); err != nil {
		return nil, fmt.Errorf("%w: trailing bytes", ErrMalformed)
	}
	return v, nil
}

// stored is a message as an admin keeps it in a segment: the author's device, its sequence number and the sealed body
// exactly as it travelled, which holds the author's signature.
type stored struct {
	Device [32]byte
	Seq    uint64
	Body   []byte
	sealed *sealed
}

func (s *stored) encode() []byte {
	var e kolocodec.Encoder
	e.PutFixed(s.Device[:])
	e.PutUvarint(s.Seq)
	e.PutFixed(s.Body)
	return e.Bytes()
}

func decodeStored(b []byte) (*stored, error) {
	d := kolocodec.NewDecoder(b)
	dev, err := d.Fixed(32)
	if err != nil {
		return nil, fmt.Errorf("%w: stored device", ErrMalformed)
	}
	s := &stored{}
	copy(s.Device[:], dev)
	if s.Seq, err = d.Uvarint(); err != nil {
		return nil, fmt.Errorf("%w: stored sequence", ErrMalformed)
	}
	s.Body, _ = d.Fixed(d.Len())
	if s.sealed, err = decodeSealed(s.Body); err != nil {
		return nil, err
	}
	return s, nil
}

// The kinds of a control log record.
const (
	ctlPut   byte = 1 // an object for settle.Pins: a Commit or a SettledCommit
	ctlOpen  byte = 2 // a segment opened, with its log header
	ctlClose byte = 3 // a segment closed, with its final size
	ctlDir   byte = 4 // the control log of another admin
	ctlLink  byte = 5 // the history link of an epoch
	ctlRead  byte = 6 // the state of an epoch sealed under the room's read secret, for the readers of a read link
	ctlChunk byte = 7 // a piece of a Commit or of a sealed tree, under the hash of the whole
	ctlTree  byte = 8 // the log that holds the sealed tree of an epoch
)

// maxChunk is the most bytes of one piece: kolo-sync answers a request for entries with about 1 MiB, so a piece fits one answer.
const maxChunk = 1 << 20

// ctlRecord is one entry of an admin's control log.
type ctlRecord struct {
	Kind   byte
	Key    string   // ctlPut
	Value  []byte   // ctlPut, ctlLink, ctlRead
	Epoch  uint64   // ctlOpen, ctlLink, ctlRead
	Header []byte   // ctlOpen, ctlDir, ctlTree: the signed log header, so a reader opens the replica without asking anyone
	ID     [32]byte // ctlClose: the log; ctlChunk: the hash of the whole
	Size   uint64   // ctlClose
	Index  uint64   // ctlChunk: this piece, of Count
	Count  uint64
}

func (c *ctlRecord) encode() []byte {
	var e kolocodec.Encoder
	e.PutFixed([]byte{c.Kind})
	switch c.Kind {
	case ctlPut:
		e.PutString(c.Key)
		e.PutBytes(c.Value)
	case ctlOpen:
		e.PutUvarint(c.Epoch)
		e.PutBytes(c.Header)
	case ctlClose:
		e.PutFixed(c.ID[:])
		e.PutUvarint(c.Size)
	case ctlDir:
		e.PutBytes(c.Header)
	case ctlLink:
		e.PutUvarint(c.Epoch)
		e.PutFixed(c.Value)
	case ctlRead:
		e.PutUvarint(c.Epoch)
		e.PutBytes(c.Value)
	case ctlChunk:
		e.PutFixed(c.ID[:])
		e.PutUvarint(c.Index)
		e.PutUvarint(c.Count)
		e.PutBytes(c.Value)
	case ctlTree:
		e.PutUvarint(c.Epoch)
		e.PutBytes(c.Header)
	}
	return e.Bytes()
}

// decodeCtl reads a control record whose value is at most maxValue bytes.
func decodeCtl(b []byte, maxValue int) (*ctlRecord, error) {
	d := kolocodec.NewDecoder(b)
	k, err := d.Fixed(1)
	if err != nil {
		return nil, fmt.Errorf("%w: control kind", ErrMalformed)
	}
	c := &ctlRecord{Kind: k[0]}
	switch c.Kind {
	case ctlPut:
		if c.Key, err = d.String(maxKey); err == nil {
			c.Value, err = d.Bytes(maxValue)
		}
	case ctlOpen:
		if c.Epoch, err = d.Uvarint(); err == nil {
			c.Header, err = d.Bytes(maxHeader)
		}
	case ctlClose:
		var id []byte
		if id, err = d.Fixed(32); err == nil {
			copy(c.ID[:], id)
			c.Size, err = d.Uvarint()
		}
	case ctlDir:
		c.Header, err = d.Bytes(maxHeader)
	case ctlLink:
		if c.Epoch, err = d.Uvarint(); err == nil {
			c.Value, err = d.Fixed(linkSize)
		}
	case ctlRead:
		if c.Epoch, err = d.Uvarint(); err == nil {
			c.Value, err = d.Bytes(maxValue)
		}
	case ctlChunk:
		var id []byte
		if id, err = d.Fixed(32); err == nil {
			copy(c.ID[:], id)
			if c.Index, err = d.Uvarint(); err == nil {
				if c.Count, err = d.Uvarint(); err == nil {
					c.Value, err = d.Bytes(maxChunk)
				}
			}
		}
		if err == nil && (c.Count == 0 || c.Index >= c.Count || len(c.Value) == 0) {
			err = errors.New("piece out of range")
		}
	case ctlTree:
		if c.Epoch, err = d.Uvarint(); err == nil {
			c.Header, err = d.Bytes(maxHeader)
		}
	default:
		err = errors.New("unknown kind")
	}
	if err == nil {
		err = d.Done()
	}
	if err != nil {
		return nil, fmt.Errorf("%w: control record: %w", ErrMalformed, err)
	}
	return c, nil
}

const (
	maxKey     = 256
	maxRequest = kolobraid.MaxJoinRequest
	maxBraid   = kolobraid.MaxWelcome
)

// adminRequest is what a device sends to join the admins' braid: the braid's join request and the header of its control log.
type adminRequest struct{ Request, Header []byte }

func (r *adminRequest) encode() []byte {
	var e kolocodec.Encoder
	e.PutFixed([]byte{kindAdminRequest})
	e.PutBytes(r.Request)
	e.PutBytes(r.Header)
	return e.Bytes()
}

func decodeAdminRequest(b []byte) (*adminRequest, error) {
	d := kolocodec.NewDecoder(b)
	if k, err := d.Fixed(1); err != nil || k[0] != kindAdminRequest {
		return nil, fmt.Errorf("%w: kind", ErrMalformed)
	}
	r := &adminRequest{}
	var err error
	if r.Request, err = d.Bytes(maxRequest); err != nil || len(r.Request) == 0 {
		return nil, fmt.Errorf("%w: join request", ErrMalformed)
	}
	if r.Header, err = d.Bytes(maxHeader); err != nil || len(r.Header) == 0 {
		return nil, fmt.Errorf("%w: log header", ErrMalformed)
	}
	if d.Done() != nil {
		return nil, fmt.Errorf("%w: trailing bytes", ErrMalformed)
	}
	return r, nil
}

// adminOffer is an admin's offer of the role to a device (kindPromote), or its braid welcome for one (kindAdminWelcome).
type adminOffer struct {
	Kind    byte
	Device  [32]byte
	Welcome []byte // kindAdminWelcome
}

func (o *adminOffer) encode() []byte {
	var e kolocodec.Encoder
	e.PutFixed([]byte{o.Kind})
	e.PutFixed(o.Device[:])
	if o.Kind == kindAdminWelcome {
		e.PutBytes(o.Welcome)
	}
	return e.Bytes()
}

func decodeAdminOffer(b []byte) (*adminOffer, error) {
	d := kolocodec.NewDecoder(b)
	k, err := d.Fixed(1)
	if err != nil || (k[0] != kindPromote && k[0] != kindAdminWelcome) {
		return nil, fmt.Errorf("%w: kind", ErrMalformed)
	}
	o := &adminOffer{Kind: k[0]}
	dev, err := d.Fixed(32)
	if err != nil {
		return nil, fmt.Errorf("%w: device", ErrMalformed)
	}
	copy(o.Device[:], dev)
	if o.Kind == kindAdminWelcome {
		if o.Welcome, err = d.Bytes(maxBraid); err != nil || len(o.Welcome) == 0 {
			return nil, fmt.Errorf("%w: welcome", ErrMalformed)
		}
	}
	if d.Done() != nil {
		return nil, fmt.Errorf("%w: trailing bytes", ErrMalformed)
	}
	return o, nil
}

// pieces cuts b, the object with hash h, into the control records that carry it.
func pieces(h [32]byte, b []byte) []*ctlRecord {
	n := (len(b) + maxChunk - 1) / maxChunk
	out := make([]*ctlRecord, n)
	for i := range out {
		out[i] = &ctlRecord{Kind: ctlChunk, ID: h, Index: uint64(i), Count: uint64(n), Value: b[i*maxChunk : min(len(b), (i+1)*maxChunk)]}
	}
	return out
}
