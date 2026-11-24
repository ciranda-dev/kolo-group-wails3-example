package room

import (
	"context"
	"fmt"
	"time"

	"github.com/ciranda-dev/kolo-pin"
)

// pinSoon marks a log of this admin to be pushed to the pins at the next turn of pinLoop.
func (r *Room) pinSoon(l *roomLog) {
	if len(r.n.cfg.Pins) == 0 {
		return
	}
	r.dmu.Lock()
	r.dirty[l] = struct{}{}
	r.dmu.Unlock()
	select {
	case r.pushKick <- struct{}{}:
	default:
	}
}

func (r *Room) dirtyAny() bool {
	r.dmu.Lock()
	defer r.dmu.Unlock()
	return len(r.dirty)+len(r.pushing) > 0
}

func (r *Room) dirtyLog(l *roomLog) bool {
	r.dmu.Lock()
	defer r.dmu.Unlock()
	_, dirty := r.dirty[l]
	_, pushing := r.pushing[l]
	return dirty || pushing
}

// pinLoop pushes the logs this admin wrote to the pins, at most once each PinGap.
func (r *Room) pinLoop() {
	for {
		select {
		case <-r.pushKick:
		case <-r.ctx.Done():
			return
		}
		select {
		case <-time.After(r.n.limits.PinGap):
		case <-r.ctx.Done():
			return
		}
		r.dmu.Lock()
		batch := r.dirty
		r.dirty, r.pushing = map[*roomLog]struct{}{}, batch
		r.dmu.Unlock()
		var perr error
		for l := range batch {
			if err := r.push(l); err != nil {
				perr = err
				r.dmu.Lock()
				r.dirty[l] = struct{}{}
				r.dmu.Unlock()
			}
		}
		r.dmu.Lock()
		r.pinErr, r.pushing = perr, nil
		retry := len(r.dirty) > 0
		r.dmu.Unlock()
		if retry && r.ctx.Err() == nil {
			select {
			case r.pushKick <- struct{}{}:
			default:
			}
		}
	}
}

// push has the pins pull and verify a log of this device.
func (r *Room) push(l *roomLog) error {
	caps, err := r.capabilities()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(r.ctx, r.n.limits.PinTTL)
	defer cancel()
	rep, err := pin.AddMany(ctx, r.n.cfg.Device, r.n.cfg.Pins, l.log, caps, 0)
	if want := min(len(r.n.cfg.Pins), pin.DefaultCopies); err == nil && rep.Accepted < want {
		// One copy is a copy, but the log stays marked until every pin Select chose has taken it.
		return fmt.Errorf("room: %d of %d pins took a log: %w", rep.Accepted, want, pin.ErrNoPeer)
	}
	return err
}

// capabilities returns one capability for each pin, issued again when half of PinTTL has passed.
func (r *Room) capabilities() ([][]byte, error) {
	r.dmu.Lock()
	defer r.dmu.Unlock()
	now := r.n.now()
	if r.caps != nil && now.Sub(r.capsAt) < r.n.limits.PinTTL/2 {
		return r.caps, nil
	}
	caps, err := pin.IssueMany(r.n.cfg.Device, r.n.cfg.Pins, pin.Capability{Expires: now.Add(r.n.limits.PinTTL)})
	if err != nil {
		return nil, err
	}
	r.caps, r.capsAt = caps, now
	return caps, nil
}
