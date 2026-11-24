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
	case r.adminKick <- struct{}{}:
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
			r.applied(evs)
		}
		var timeout <-chan time.Time
		t := time.NewTimer(0)
		if !deadline.IsZero() {
			t.Reset(max(0, deadline.Sub(r.n.now())))
			timeout = t.C
		}
		select {
		case <-r.adminKick:
		case <-timeout:
		case <-r.ctx.Done():
		}
		t.Stop()
	}
}

// announce tells the members on the room's topic that a Commit settled; they pull it from the control logs.
func (r *Room) announce(ap *braid.Applied) {
	_, err := r.publish(r.ctx, func(uint64) ([]byte, error) {
		return (&commitRef{Settled: ap.Settled}).encode(), nil
	})
	if err != nil && r.ctx.Err() == nil {
		r.emit(Event{Kind: MembersChanged, Epoch: ap.Epoch + 1, Err: err})
	}
}

// commit makes this admin's Commit of o, waits until it settles and applies, and returns the Welcome of the members it
// adds and the epoch it was made in.
func (r *Room) commit(ctx context.Context, o kolomls.CommitOptions) (welcome, settled []byte, epoch uint64, err error) {
	r.opMu.Lock()
	defer r.opMu.Unlock()
	r.mu.Lock()
	if r.admin == nil {
		r.mu.Unlock()
		return nil, nil, 0, ErrNotAdmin
	}
	if r.removed.Load() {
		r.mu.Unlock()
		return nil, nil, 0, ErrRemoved
	}
	r.stepErr = nil
	epoch = r.m.Epoch()
	var (
		hash  [32]byte
		added [][32]byte
	)
	if o.SetExtensions && r.rolesChange(o.Extensions) {
		// A roles change is small, and the admin checks it against the braid before it commits.
		if welcome, err = r.admin.Commit(ctx, o); err != nil {
			r.mu.Unlock()
			return nil, nil, 0, err
		}
		hash = sha256.Sum256(r.m.Pending().Commit())
	} else {
		pc, err := r.m.Commit(o)
		if err != nil {
			r.mu.Unlock()
			return nil, nil, 0, err
		}
		hash = sha256.Sum256(pc.Commit())
		welcome = pc.Welcome()
		added = r.newDevices(r.m.PendingLeaves())
		err = r.admin.Reference(ctx)
	}
	r.mu.Unlock()
	if err != nil {
		return nil, nil, 0, err
	}
	r.poke()
	for {
		ch := *r.changed.Load()
		if r.view.Load().epoch > epoch {
			break
		}
		if r.removed.Load() {
			return nil, nil, 0, ErrRemoved
		}
		r.mu.Lock()
		err := r.stepErr
		r.mu.Unlock()
		if err != nil {
			return nil, nil, 0, err
		}
		select {
		case <-ch:
		case <-ctx.Done():
			return nil, nil, 0, ctx.Err()
		}
	}
	if settled, err = r.settledFor(ctx, epoch, hash); err != nil {
		return nil, nil, 0, err
	}
	if err := r.keepWelcome(epoch, added); err != nil {
		return nil, nil, 0, err
	}
	return welcome, settled, epoch, nil
}

// newDevices returns the devices of leaves that the room does not hold yet.
func (r *Room) newDevices(leaves []*kolomls.LeafNode) [][32]byte {
	var out [][32]byte
	for _, l := range leaves {
		if c, err := cred.Decode(l.Credential); err == nil && r.view.Load().author([32]byte(c.Device())) == nil {
			out = append(out, [32]byte(c.Device()))
		}
	}
	return out
}

// keepWelcome records the devices that the Welcome of epoch is for, until each has made contact.
func (r *Room) keepWelcome(epoch uint64, devices [][32]byte) error {
	r.wmu.Lock()
	defer r.wmu.Unlock()
	for _, d := range devices {
		if err := r.kv.put(be(epoch), recWelcome, d[:]); err != nil {
			return err
		}
		r.welcomed[d] = epoch
	}
	return nil
}

// contact takes a Welcome as used once the device it is for sends a message: the tree it named then leaves this admin
// unless it is the latest (braid.Admin.ReleaseWelcome). A device that only connects may still be pulling the tree.
func (r *Room) contact(device [32]byte) {
	r.wmu.Lock()
	epoch, ok := r.welcomed[device]
	if !ok {
		r.wmu.Unlock()
		return
	}
	delete(r.welcomed, device)
	r.kv.del(recWelcome, device[:])
	last := true
	for _, e := range r.welcomed {
		last = last && e != epoch
	}
	r.wmu.Unlock()
	if !last {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.admin != nil {
		if err := r.admin.ReleaseWelcome(r.ctx, epoch); err != nil && r.ctx.Err() == nil {
			r.emit(Event{Kind: MembersChanged, Err: fmt.Errorf("room: releasing the Welcome of epoch %d: %w", epoch, err)})
		}
	}
}

// rolesChange reports whether extensions change the room's roles, which the admin checks against the braid; a change of the
// policy alone is committed like any other Commit, and checked for its size.
func (r *Room) rolesChange(exts []kolomls.Extension) bool {
	next, err := settle.RolesOf(exts, r.m.Limits())
	return err != nil || !bytes.Equal(next.Encode(), r.view.Load().roles.Encode())
}

// settledBound is kolo-mls's bound on a SettledCommit of n settlers.
func settledBound(n int) int { return 512 + 96*n }

// Add adds the owners of the key packages and returns the welcome for them: give the same welcome to each, who joins
// with Node.Join. Only an admin device adds, and it returns once the new epoch is settled and applied here.
func (r *Room) Add(ctx context.Context, keyPackages ...[]byte) (welcome []byte, err error) {
	if len(keyPackages) == 0 {
		return nil, errInvalid("no key package")
	}
	w, settled, epoch, err := r.commit(ctx, kolomls.CommitOptions{Add: keyPackages})
	if err != nil {
		return nil, err
	}
	tree, err := r.pins.Get(ctx, settle.TreeKey(r.gid[:], epoch+1), r.n.limits.MaxTreeBytes+64)
	if errors.Is(err, settle.ErrMissing) {
		// The epoch was applied from a Commit another admin settled, so no tree was sealed for it here.
		r.mu.Lock()
		if r.m.Epoch() == epoch+1 {
			tree, err = settle.SealTree(r.m)
		}
		r.mu.Unlock()
	}
	if err != nil {
		return nil, err
	}
	header, entries, err := r.writeTree(epoch+1, tree)
	if err != nil {
		return nil, err
	}
	return (&invite{Welcome: w, Settled: settled, TreeHeader: header, TreeEntries: entries, TreeHash: sha256.Sum256(tree), Secret: r.secret, Name: r.name, Logs: r.controlHeaders()}).encode(), nil
}

// controlHeaders returns the signed headers of the control logs of the room's admins that this device holds.
func (r *Room) controlHeaders() [][]byte {
	r.logMu.Lock()
	defer r.logMu.Unlock()
	var out [][]byte
	for _, l := range r.logs {
		if l.ctl {
			out = append(out, l.header)
		}
	}
	return out
}

// Remove removes every device of a person from the room. Only an admin device removes, and the person must have no
// admin device.
func (r *Room) Remove(ctx context.Context, person ed25519.PublicKey) error {
	if r.reader {
		return ErrNotAdmin
	}
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
	_, _, _, err := r.commit(ctx, kolomls.CommitOptions{Remove: leaves})
	return err
}

// settledFor returns the SettledCommit of epoch that names the Commit with the given hash. Each admin's control log may
// hold one for the epoch, and another admin's may not have reached this device yet.
func (r *Room) settledFor(ctx context.Context, epoch uint64, hash [32]byte) ([]byte, error) {
	key := settle.SettledKey(r.gid[:], epoch)
	for {
		for _, ref := range r.idx.all(key) {
			b, err := r.readObject(ref, key)
			if err != nil {
				continue
			}
			if st, err := settle.DecodeSettled(b, 0); err == nil && st.TBS.Commit == hash {
				return b, nil
			}
		}
		select {
		case <-time.After(10 * time.Millisecond):
		case <-ctx.Done():
			return nil, fmt.Errorf("%w: the SettledCommit of epoch %d", settle.ErrMissing, epoch)
		}
	}
}
