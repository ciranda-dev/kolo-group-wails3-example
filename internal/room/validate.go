package room

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"time"

	kologossip "github.com/ciranda-dev/kolo-gossip"
	kololog "github.com/ciranda-dev/kolo-log"
	"github.com/ciranda-dev/kolo-mls/settle"
)

// limiter is a token bucket per device.
type limiter struct {
	mu      sync.Mutex
	rate    float64
	burst   float64
	buckets map[[32]byte]*bucket
}

type bucket struct {
	tokens float64
	last   time.Time
}

func newLimiter(rate float64, burst int) *limiter {
	return &limiter{rate: rate, burst: float64(burst), buckets: map[[32]byte]*bucket{}}
}

// allow takes one token of device, false when it has none.
func (l *limiter) allow(device [32]byte, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	b := l.buckets[device]
	if b == nil {
		b = &bucket{tokens: l.burst, last: now}
		l.buckets[device] = b
	}
	if d := now.Sub(b.last); d > 0 {
		b.tokens = min(l.burst, b.tokens+d.Seconds()*l.rate)
		b.last = now
	}
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// validate is the room's gossip validator: Reject for what no honest member sends, Ignore for what is only out of
// date, Accept otherwise. It opens the body, so a body that does not open is not forwarded.
func (r *Room) validate(_ context.Context, m *kologossip.Message) kologossip.Verdict {
	switch err := r.check(m); {
	case err == nil:
		return kologossip.Accept
	case errors.Is(err, ErrWrongEpoch):
		return kologossip.Ignore
	}
	return kologossip.Reject
}

// check returns why the room refuses m, nil when it passes.
func (r *Room) check(m *kologossip.Message) error {
	if len(m.Body) == 0 || len(m.Body) > r.n.limits.MaxMessageSize || len(m.Device) != 32 {
		return ErrMalformed
	}
	v := r.view.Load()
	now := r.n.now()
	switch m.Body[0] {
	case kindMessage:
		s, err := decodeSealed(m.Body)
		if err != nil {
			return err
		}
		key, err := r.keyFor(v, s.Epoch, [32]byte(m.Device), now)
		if err != nil {
			return err
		}
		if !r.mayPostAt(v, s.Epoch, liveAt(v, s.Epoch, now), m.Person, m.Device) {
			return ErrNotPoster
		}
		// The token is taken before the signature and the AEAD, so a flood costs its author before it costs this node.
		if !r.rate.allow([32]byte(m.Device), now) {
			return ErrRate
		}
		if err := verifySealed(m.Device, r.gid[:], s, m.Seq); err != nil {
			return err
		}
		_, err = r.openMessage(&key, s, m)
		return err
	case kindCommit:
		c, err := decodeCommitRef(m.Body)
		if err != nil {
			return err
		}
		if !v.roles.Admin(m.Device) {
			return ErrNotAdmin
		}
		st, err := settle.DecodeSettled(c.Settled, len(v.roles.Settlers))
		if err != nil {
			return ErrMalformed
		}
		if string(st.TBS.GroupID) != string(r.gid[:]) || st.TBS.Epoch != v.epoch {
			return ErrWrongEpoch
		}
		if !r.rate.allow([32]byte(m.Device), now) {
			return ErrRate
		}
		if st.Valid(v.roles, func(d [32]byte) bool { return v.author(d) != nil }) < v.roles.Q() {
			return ErrCommitRef
		}
		return nil
	case kindPromote, kindAdminWelcome:
		if !v.roles.Admin(m.Device) {
			return ErrNotAdmin
		}
		if _, err := decodeAdminOffer(m.Body); err != nil {
			return err
		}
		if !r.rate.allow([32]byte(m.Device), now) {
			return ErrRate
		}
		return nil
	case kindLeave:
		if len(m.Body) != 1 {
			return ErrMalformed
		}
		if v.author([32]byte(m.Device)) == nil {
			return ErrNotMember
		}
		if !r.rate.allow([32]byte(m.Device), now) {
			return ErrRate
		}
		return nil
	case kindAdminRequest:
		q, err := decodeAdminRequest(m.Body)
		if err != nil {
			return err
		}
		if hd, err := kololog.DecodeLogHeader(q.Header); err != nil || !bytes.Equal(hd.LogKey, m.Device) {
			return ErrMalformed
		}
		if v.author([32]byte(m.Device)) == nil {
			return ErrWrongEpoch
		}
		if !r.rate.allow([32]byte(m.Device), now) {
			return ErrRate
		}
		return nil
	}
	return ErrMalformed
}

// keyFor returns the key a message of epoch is sealed under: the current epoch's, the previous one's while its window
// lasts and, for a device that is a member of it, any older one the room holds, which a message published again needs.
func (r *Room) keyFor(v *view, epoch uint64, device [32]byte, now time.Time) ([keySize]byte, error) {
	if ev := v.at(epoch, now); ev != nil {
		if ev.author(device) == nil {
			return [keySize]byte{}, ErrWrongEpoch
		}
		return ev.key, nil
	}
	r.kmu.RLock()
	defer r.kmu.RUnlock()
	if k, ok := r.keys[epoch]; ok && epoch < v.epoch && epoch >= r.joinEpoch && v.author(device) != nil {
		return k, nil
	}
	return [keySize]byte{}, ErrWrongEpoch
}

// openMessage opens a sealed message under key and checks what it holds against its envelope.
func (r *Room) openMessage(key *[keySize]byte, s *sealed, m *kologossip.Message) (*content, error) {
	c, err := r.openContent(key, s, m.Device, m.Seq)
	if err != nil {
		return nil, err
	}
	if c.Time-m.Time.UnixMilli() > r.n.limits.MaxSkew.Milliseconds() {
		return nil, ErrBadTime
	}
	return c, nil
}

// openContent opens a sealed message and checks what it holds. A message published again is older than its envelope, so
// only a time ahead of the envelope's is refused.
func (r *Room) openContent(key *[keySize]byte, s *sealed, device []byte, seq uint64) (*content, error) {
	pt, err := open(key, s.Nonce, aad(r.gid[:], s.Epoch, device, seq), s.CT)
	if err != nil {
		return nil, err
	}
	c, err := decodeContent(pt)
	if err != nil {
		return nil, err
	}
	if len(c.Parents) > r.n.limits.MaxParents {
		return nil, ErrTooManyParents
	}
	return c, nil
}
