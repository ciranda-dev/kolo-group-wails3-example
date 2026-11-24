package room

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"errors"
	"fmt"
	"time"

	kolomls "github.com/ciranda-dev/kolo-mls"
	"github.com/ciranda-dev/kolo-mls/braid"
	"github.com/ciranda-dev/kolo-mls/cred"
	"github.com/ciranda-dev/kolo-mls/settle"
)

// braidEvents wakes the admin loop whenever the admins' braid changes.
func (r *Room) braidEvents() {
	for {
		if _, err := r.b.Next(r.ctx); err != nil {
			return
		}
		r.poke()
	}
}

func (r *Room) poke() {
	select {
	case r.kick <- struct{}{}:
	default:
	}
}

// adminLoop does what the braid's settled order asks of this admin, and sends each Commit it applies to the members.
func (r *Room) adminLoop() {
	for r.ctx.Err() == nil && !r.removed.Load() {
		r.mu.Lock()
		ap, err := r.admin.Step(r.ctx)
		var evs []Event
		if ap != nil {
			evs = r.refreshLocked()
		}
		if err != nil && r.ctx.Err() == nil {
			r.stepErr = err
			r.notify()
		}
		deadline, derr := r.admin.NextDeadline()
		if err == nil && derr != nil && r.ctx.Err() == nil {
			r.stepErr = derr
			r.notify()
		}
		r.mu.Unlock()
		if ap != nil {
			r.announce(ap)
			r.emit(evs...)
			r.finishRemoval()
		}
		var timeout <-chan time.Time
		t := time.NewTimer(0)
		if !deadline.IsZero() {
			t.Reset(max(0, deadline.Sub(r.n.now())))
			timeout = t.C
		}
		select {
		case <-r.kick:
		case <-timeout:
		case <-r.ctx.Done():
		}
		t.Stop()
	}
}

// announce sends a settled Commit to the members on the room's topic.
func (r *Room) announce(ap *braid.Applied) {
	_, err := r.publish(r.ctx, func(uint64) ([]byte, error) {
		return (&commitMsg{Settled: ap.Settled, Commit: ap.Commit}).encode(), nil
	})
	if err != nil && r.ctx.Err() == nil {
		r.emit(Event{Kind: MembersChanged, Epoch: ap.Epoch + 1, Err: err})
	}
}

// commit makes this admin's Commit of o, waits until it settles and applies, and returns the Welcome of the members it
// adds and the epoch it was made in.
func (r *Room) commit(ctx context.Context, o kolomls.CommitOptions) (welcome []byte, epoch uint64, err error) {
	r.opMu.Lock()
	defer r.opMu.Unlock()
	if r.admin == nil {
		return nil, 0, ErrNotAdmin
	}
	if r.removed.Load() {
		return nil, 0, ErrRemoved
	}
	r.mu.Lock()
	r.stepErr = nil
	epoch = r.m.Epoch()
	pc, err := r.m.Commit(o)
	if err != nil {
		r.mu.Unlock()
		return nil, 0, err
	}
	// The Commit and its SettledCommit travel in one gossip message; Commits by reference are not built yet.
	if len(pc.Commit())+settledBound(len(r.view.Load().roles.Settlers))+64 > r.n.limits.MaxMessageSize-envelopeReserve {
		err = r.m.DiscardPending()
		r.mu.Unlock()
		return nil, 0, errors.Join(ErrTooLarge, err)
	}
	hash := sha256.Sum256(pc.Commit())
	welcome = pc.Welcome()
	err = r.admin.Reference(ctx)
	r.mu.Unlock()
	if err != nil {
		return nil, 0, err
	}
	r.poke()
	for {
		ch := *r.changed.Load()
		if r.view.Load().epoch > epoch {
			break
		}
		if r.removed.Load() {
			return nil, 0, ErrRemoved
		}
		r.mu.Lock()
		err := r.stepErr
		r.mu.Unlock()
		if err != nil {
			return nil, 0, err
		}
		select {
		case <-ch:
		case <-ctx.Done():
			return nil, 0, ctx.Err()
		}
	}
	settled, err := r.pins.Get(ctx, settle.SettledKey(r.gid[:], epoch), 0)
	if err != nil {
		return nil, 0, err
	}
	st, err := settle.DecodeSettled(settled, 0)
	if err != nil {
		return nil, 0, err
	}
	if st.TBS.Commit != hash {
		return nil, 0, errors.New("room: another Commit settled this epoch")
	}
	return welcome, epoch, nil
}

// settledBound is kolo-mls's bound on a SettledCommit of n settlers.
func settledBound(n int) int { return 512 + 96*n }

// Add adds the owners of the key packages and returns the welcome for them: give the same welcome to each, who joins
// with Node.Join. Only an admin device adds, and it returns once the new epoch is settled and applied here.
func (r *Room) Add(ctx context.Context, keyPackages ...[]byte) (welcome []byte, err error) {
	if len(keyPackages) == 0 {
		return nil, errInvalid("no key package")
	}
	w, epoch, err := r.commit(ctx, kolomls.CommitOptions{Add: keyPackages})
	if err != nil {
		return nil, err
	}
	settled, err := r.pins.Get(ctx, settle.SettledKey(r.gid[:], epoch), 0)
	if err != nil {
		return nil, err
	}
	tree, err := r.pins.Get(ctx, settle.TreeKey(r.gid[:], epoch+1), r.n.limits.MaxTreeBytes+64)
	if err != nil {
		return nil, err
	}
	return (&invite{Welcome: w, Settled: settled, Tree: tree, Secret: r.secret, Name: r.name}).encode(), nil
}

// Remove removes every device of a person from the room. Only an admin device removes, and the person must have no
// admin device.
func (r *Room) Remove(ctx context.Context, person ed25519.PublicKey) error {
	r.mu.Lock()
	var leaves []uint32
	t := r.m.Tree()
	for _, l := range t.Members() {
		c, err := cred.Decode(t.Leaf(l).Credential)
		if err != nil {
			r.mu.Unlock()
			return err
		}
		if bytes.Equal(c.Person(), person) {
			leaves = append(leaves, l)
		}
	}
	r.mu.Unlock()
	if len(leaves) == 0 {
		return fmt.Errorf("%w: no device of this person", ErrNotMember)
	}
	_, _, err := r.commit(ctx, kolomls.CommitOptions{Remove: leaves})
	return err
}
