package room

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"

	kolocodec "github.com/ciranda-dev/kolo-codec"
	kologossip "github.com/ciranda-dev/kolo-gossip"
	koloid "github.com/ciranda-dev/kolo-id"
	kololog "github.com/ciranda-dev/kolo-log"
	kolostore "github.com/ciranda-dev/kolo-store"
)

// roomKV is what a room keeps in the node's backend, under its own prefix, so a restart reopens it.
type roomKV struct {
	b kolostore.Backend
	p []byte
}

func newRoomKV(b kolostore.Backend, gid GroupID) roomKV {
	return roomKV{b: b, p: []byte("g/" + hex.EncodeToString(gid[:]) + "/")}
}

func (k roomKV) key(kind string, tail ...[]byte) []byte {
	return bytes.Join(append([][]byte{append(bytes.Clone(k.p), kind...)}, tail...), nil)
}

func (k roomKV) get(kind string, tail ...[]byte) ([]byte, error) {
	v, err := k.b.Get(k.key(kind, tail...))
	if errors.Is(err, kolostore.ErrNotFound) {
		return nil, nil
	}
	return v, err
}

func (k roomKV) put(v []byte, kind string, tail ...[]byte) error {
	return k.b.Put(k.key(kind, tail...), v)
}

func (k roomKV) del(kind string, tail ...[]byte) error { return k.b.Delete(k.key(kind, tail...)) }

func (k roomKV) dropAll() error {
	end := bytes.Clone(k.p)
	end[len(end)-1]++
	bt := k.b.NewBatch()
	bt.DeleteRange(k.p, end)
	return bt.Commit()
}

// each calls fn for every record of kind, with the rest of its key.
func (k roomKV) each(kind string, fn func(tail, v []byte) error) error {
	start := k.key(kind)
	end := bytes.Clone(start)
	end[len(end)-1]++
	var err error
	if serr := k.b.Scan(start, end, false, 0, func(key, v []byte) bool {
		err = fn(bytes.Clone(key[len(start):]), bytes.Clone(v))
		return err == nil
	}); serr != nil {
		return serr
	}
	return err
}

// The record kinds of a room: its meta, epoch keys, policies, logs, authors, receipts and local pins.
const (
	recMeta    = "m"
	recKey     = "k/"
	recPolicy  = "p/"
	recLog     = "l/"
	recAuthor  = "a/"
	recReceipt = "x/"
	recObject  = "t/"
	recWelcome = "w/" // a device whose Welcome this admin made and that has not made contact
	recFirst   = "f/" // the first epoch a device was a leaf in, as this device saw it
)

const metaVersion = 0

// meta is what a room needs to reopen: its name and secret, the braid prefix of an admin and the logs it wrote.
type meta struct {
	Name       string
	Secret     [keySize]byte
	Prefix     string
	JoinEpoch  uint64
	Ctl        kololog.LogID
	HasCtl     bool
	Seg        kololog.LogID
	HasSeg     bool
	Braid      bool
	BraidReady bool
}

func (m *meta) encode() []byte {
	var e kolocodec.Encoder
	e.PutUvarint(metaVersion)
	e.PutString(m.Name)
	e.PutFixed(m.Secret[:])
	e.PutString(m.Prefix)
	e.PutUvarint(m.JoinEpoch)
	e.PutFixed(m.Ctl[:])
	e.PutFixed(m.Seg[:])
	flags := byte(0)
	for i, f := range []bool{m.HasCtl, m.HasSeg, m.Braid, m.BraidReady} {
		if f {
			flags |= 1 << i
		}
	}
	e.PutFixed([]byte{flags})
	return e.Bytes()
}

func decodeMeta(b []byte) (*meta, error) {
	d := kolocodec.NewDecoder(b)
	if v, err := d.Uvarint(); err != nil || v != metaVersion {
		return nil, fmt.Errorf("%w: meta version", ErrMalformed)
	}
	m := &meta{}
	var err error
	if m.Name, err = d.String(maxName); err != nil {
		return nil, fmt.Errorf("%w: meta name", ErrMalformed)
	}
	s, err := d.Fixed(keySize)
	if err != nil {
		return nil, fmt.Errorf("%w: meta secret", ErrMalformed)
	}
	copy(m.Secret[:], s)
	if m.Prefix, err = d.String(maxName); err != nil {
		return nil, fmt.Errorf("%w: meta prefix", ErrMalformed)
	}
	if m.JoinEpoch, err = d.Uvarint(); err != nil {
		return nil, fmt.Errorf("%w: meta epoch", ErrMalformed)
	}
	for _, id := range []*kololog.LogID{&m.Ctl, &m.Seg} {
		x, err := d.Fixed(32)
		if err != nil {
			return nil, fmt.Errorf("%w: meta log", ErrMalformed)
		}
		copy(id[:], x)
	}
	f, err := d.Fixed(1)
	if err != nil || d.Done() != nil || f[0]&^0xf != 0 {
		return nil, fmt.Errorf("%w: meta flags", ErrMalformed)
	}
	m.HasCtl, m.HasSeg, m.Braid, m.BraidReady = f[0]&1 != 0, f[0]&2 != 0, f[0]&4 != 0, f[0]&8 != 0
	return m, nil
}

// logRec is what a room keeps of one log it holds: enough to reopen it and to go on reading it.
type logRec struct {
	Ctl    bool
	Own    bool
	Tree   bool // the log holds the sealed tree of Epoch
	Epoch  uint64
	Closed bool
	Size   uint64
	Cursor uint64
	Header []byte
}

func (l *logRec) encode() []byte {
	var e kolocodec.Encoder
	flags := byte(0)
	for i, f := range []bool{l.Ctl, l.Own, l.Closed, l.Tree} {
		if f {
			flags |= 1 << i
		}
	}
	e.PutFixed([]byte{flags})
	e.PutUvarint(l.Epoch)
	e.PutUvarint(l.Size)
	e.PutUvarint(l.Cursor)
	e.PutBytes(l.Header)
	return e.Bytes()
}

func decodeLogRec(b []byte) (*logRec, error) {
	d := kolocodec.NewDecoder(b)
	f, err := d.Fixed(1)
	if err != nil {
		return nil, fmt.Errorf("%w: log record", ErrMalformed)
	}
	if f[0]&^0xf != 0 {
		return nil, fmt.Errorf("%w: log record flags", ErrMalformed)
	}
	l := &logRec{Ctl: f[0]&1 != 0, Own: f[0]&2 != 0, Closed: f[0]&4 != 0, Tree: f[0]&8 != 0}
	if l.Epoch, err = d.Uvarint(); err == nil {
		if l.Size, err = d.Uvarint(); err == nil {
			if l.Cursor, err = d.Uvarint(); err == nil {
				l.Header, err = d.Bytes(maxHeader)
			}
		}
	}
	if err != nil || d.Done() != nil {
		return nil, fmt.Errorf("%w: log record", ErrMalformed)
	}
	return l, nil
}

func epochKeyName(epoch uint64) []byte { return binary.BigEndian.AppendUint64(nil, epoch) }

func (k roomKV) putAuthor(device [32]byte, a *kologossip.Author) error {
	return k.put(append(bytes.Clone(a.Person), a.Cert.Encode()...), recAuthor, device[:])
}

func decodeAuthor(v []byte) (*kologossip.Author, error) {
	if len(v) < 32 {
		return nil, fmt.Errorf("%w: author record", ErrMalformed)
	}
	c, err := koloid.DecodeDeviceCert(v[32:])
	if err != nil {
		return nil, fmt.Errorf("%w: author certificate: %w", ErrMalformed, err)
	}
	return &kologossip.Author{Person: bytes.Clone(v[:32]), Cert: c}, nil
}
