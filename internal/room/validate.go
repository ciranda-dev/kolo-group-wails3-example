package room

import (
	"context"
	"errors"
	"sync"
	"time"

	kologossip "github.com/ciranda-dev/kolo-gossip"
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
		ev := v.at(s.Epoch, now)
		if ev == nil || ev.author([32]byte(m.Device)) == nil {
			return ErrWrongEpoch
		}
		if !r.rate.allow([32]byte(m.Device), now) {
			return ErrRate
		}
		_, err = r.openMessage(&ev.key, s, m)
		return err
	case kindCommit:
		c, err := decodeCommit(m.Body)
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
		return nil
	}
	return ErrMalformed
}

// openMessage opens a sealed message under key and checks what it holds.
func (r *Room) openMessage(key *[keySize]byte, s *sealed, m *kologossip.Message) (*content, error) {
	pt, err := open(key, s.Nonce, aad(r.gid[:], s.Epoch, m.Device, m.Seq), s.CT)
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
	if d := time.UnixMilli(c.Time).Sub(m.Time); d > r.n.limits.MaxSkew || -d > r.n.limits.MaxSkew {
		return nil, ErrBadTime
	}
	return c, nil
}
