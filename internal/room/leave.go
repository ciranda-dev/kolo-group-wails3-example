package room

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"time"

	kologossip "github.com/ciranda-dev/kolo-gossip"
)

// Leave asks the admins to remove this device's person and returns once they have, or once ctx ends. Either way the room
// stops on this device: Next returns a Removed event and then ErrRemoved, and the room forgets its state. The request is a
// gossip message sent again every RepublishAfter, so it reaches an admin that comes online while Leave waits; when ctx ends
// first Leave returns its error and the person stays in the room until an admin removes it. An admin device cannot leave.
func (r *Room) Leave(ctx context.Context) error {
	switch {
	case r.removed.Load():
		return ErrRemoved
	case r.ctx.Err() != nil:
		return ErrClosed
	case r.isAdmin():
		return errInvalid("an admin device cannot leave")
	}
	var err error
	if !r.reader {
		err = r.askToLeave(ctx)
	}
	if r.removed.CompareAndSwap(false, true) {
		r.gr.Leave()
		r.n.refreshMembers()
		r.applied([]Event{{Kind: Removed, Epoch: r.view.Load().epoch}})
	}
	return err
}

// askToLeave sends the request until the room reports the removal of this device.
func (r *Room) askToLeave(ctx context.Context) error {
	for {
		if _, err := r.publish(ctx, func(uint64) ([]byte, error) { return []byte{kindLeave}, nil }); err != nil {
			return err
		}
		select {
		case <-r.gone:
			return nil
		case <-time.After(r.n.limits.RepublishAfter):
		case <-ctx.Done():
			return ctx.Err()
		case <-r.ctx.Done():
			return ErrClosed
		}
	}
}

// handleLeave removes the person of a member that asked to leave, when this admin is the one that acts: the admin of the
// lowest device key among those connected, itself included, so two admins do not make two Commits for one request.
func (r *Room) handleLeave(m *kologossip.Message) {
	if !r.isAdmin() || r.leaveActor() != [32]byte(r.self) {
		return
	}
	person := bytes.Clone(m.Person)
	r.wg.Go(func() {
		ctx, cancel := context.WithTimeout(r.ctx, r.n.limits.CatchUpWait)
		defer cancel()
		if err := r.Remove(ctx, person); err != nil && r.ctx.Err() == nil && !errors.Is(err, ErrNotMember) {
			r.emit(Event{Kind: MembersChanged, Err: fmt.Errorf("room: a member asked to leave: %w", err)})
		}
	})
}

// leaveActor returns the admin device that answers a request to leave.
func (r *Room) leaveActor() [32]byte {
	r.n.mu.Lock()
	defer r.n.mu.Unlock()
	for _, k := range r.view.Load().roles.Admins {
		if _, on := r.n.conns[k]; on || k == [32]byte(r.self) {
			return k
		}
	}
	return [32]byte(r.self)
}
