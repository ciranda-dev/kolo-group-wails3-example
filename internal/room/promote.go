package room

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"time"

	kolobraid "github.com/ciranda-dev/kolo-braid"
	"github.com/ciranda-dev/kolo-braid/devices"
	kologossip "github.com/ciranda-dev/kolo-gossip"
	kolomls "github.com/ciranda-dev/kolo-mls"
	"github.com/ciranda-dev/kolo-mls/braid"
)

// promotion is the part of a device that joins the admins' braid before the roles name it.
type promotion struct {
	j      *kolobraid.Joining
	prefix string
	idx    *braid.Index
	roster *devices.Store
	b      *kolobraid.Braid
	order  *braid.BraidOrder
}

// isAdmin reports whether this device is an admin of the room now and runs its admin side.
func (r *Room) isAdmin() bool {
	r.logMu.Lock()
	ctl := r.ctl != nil
	r.logMu.Unlock()
	return ctl && r.view.Load().roles.Admin(r.self)
}

// Promote makes the device an admin: it joins the admins' braid, and the roles name it in the next settled Commit, so it
// writes segments and commits as this device does. Only an admin promotes, only to Admin, and the device must be online.
func (r *Room) Promote(ctx context.Context, device ed25519.PublicKey, role Role) error {
	if role != Admin {
		return fmt.Errorf("%w: only the role Admin is built", ErrNotImplemented)
	}
	if len(device) != ed25519.PublicKeySize {
		return errInvalid("a device key of %d bytes", len(device))
	}
	r.mu.Lock()
	admin, b := r.admin, r.b
	r.mu.Unlock()
	if admin == nil {
		return ErrNotAdmin
	}
	v := r.view.Load()
	switch {
	case v.author([32]byte(device)) == nil:
		return fmt.Errorf("%w: no such device", ErrNotMember)
	case v.roles.Admin(device):
		return nil
	}
	dev := [32]byte(device)
	reqs := make(chan *adminRequest, 1)
	r.promoMu.Lock()
	if r.promoting[dev] != nil {
		r.promoMu.Unlock()
		return errInvalid("this device is being promoted")
	}
	r.promoting[dev] = reqs
	r.promoMu.Unlock()
	defer func() {
		r.promoMu.Lock()
		delete(r.promoting, dev)
		r.promoMu.Unlock()
	}()
	if _, err := r.publish(ctx, func(uint64) ([]byte, error) { return (&adminOffer{Kind: kindPromote, Device: dev}).encode(), nil }); err != nil {
		return err
	}
	var req *adminRequest
	select {
	case req = <-reqs:
	case <-ctx.Done():
		return ctx.Err()
	}
	welcome, err := b.Admit(ctx, device, req.Request, &kolobraid.AdmitOptions{Roles: ptr(kolobraid.RoleAdmin)})
	if err != nil {
		return err
	}
	if _, err := r.publish(ctx, func(uint64) ([]byte, error) {
		return (&adminOffer{Kind: kindAdminWelcome, Device: dev, Welcome: welcome}).encode(), nil
	}); err != nil {
		return err
	}
	for {
		admins, _, _, err := r.border.Roster()
		if err == nil && slices.ContainsFunc(admins, func(k [32]byte) bool { return k == dev }) {
			break
		}
		select {
		case <-time.After(10 * time.Millisecond):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	_, _, _, err = r.commit(ctx, func(v *view) (kolomls.CommitOptions, error) {
		switch {
		case v.author(dev) == nil:
			return kolomls.CommitOptions{}, fmt.Errorf("%w: no such device", ErrNotMember)
		case v.roles.Admin(device):
			return kolomls.CommitOptions{}, errNoChange
		}
		next := *v.roles
		next.Admins = append(slices.Clone(v.roles.Admins), dev)
		slices.SortFunc(next.Admins, func(a, b [32]byte) int { return bytes.Compare(a[:], b[:]) })
		exts, err := groupExtensions(&next, v.pol)
		return kolomls.CommitOptions{SetExtensions: true, Extensions: exts}, err
	})
	if errors.Is(err, errNoChange) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := r.appendCtl(&ctlRecord{Kind: ctlDir, Header: req.Header}); err != nil {
		return err
	}
	r.addLog(dev, req.Header, true, 0)
	return nil
}

func ptr[T any](v T) *T { return &v }

// handleOffer answers an admin's offer of the role to this device: it makes its control log and asks to join the braid.
func (r *Room) handleOffer(m *kologossip.Message, o *adminOffer) {
	if o.Device != [32]byte(r.self) || r.view.Load().roles.Admin(r.self) {
		return
	}
	r.mu.Lock()
	p := r.promo
	r.mu.Unlock()
	if p == nil {
		var pre [8]byte
		if _, err := rand.Read(pre[:]); err != nil {
			return
		}
		prefix := "room/" + hex.EncodeToString(pre[:]) + "/"
		opts, idx, roster, err := braidParts(r.n.cfg.Backend, []byte(prefix+"b/"), r.n.cfg.BraidLimits)
		if err != nil {
			return
		}
		j, err := kolobraid.NewJoin(r.ctx, r.n.cfg.Device, opts)
		if err != nil {
			r.emit(Event{Kind: MembersChanged, Err: fmt.Errorf("room: joining the admins' braid: %w", err)})
			return
		}
		p = &promotion{j: j, prefix: prefix, idx: idx, roster: roster}
		r.mu.Lock()
		r.promo = p
		r.mu.Unlock()
	}
	if err := r.ensureCtl(); err != nil {
		return
	}
	req, err := p.j.Request(nil)
	if err != nil {
		return
	}
	r.logMu.Lock()
	header := r.ctl.header
	r.logMu.Unlock()
	r.publish(r.ctx, func(uint64) ([]byte, error) { return (&adminRequest{Request: req, Header: header}).encode(), nil })
}

func (r *Room) handleRequest(m *kologossip.Message, q *adminRequest) {
	r.promoMu.Lock()
	ch := r.promoting[[32]byte(m.Device)]
	r.promoMu.Unlock()
	if ch == nil {
		return
	}
	select {
	case ch <- q:
	default:
	}
}

// handleWelcome completes this device's join of the admins' braid with the welcome an admin published for it.
func (r *Room) handleWelcome(m *kologossip.Message, o *adminOffer) {
	r.mu.Lock()
	p := r.promo
	r.mu.Unlock()
	if o.Device != [32]byte(r.self) || p == nil || p.b != nil {
		return
	}
	b, err := p.j.Complete(r.ctx, o.Welcome)
	if err != nil {
		r.emit(Event{Kind: MembersChanged, Err: fmt.Errorf("room: joining the admins' braid: %w", err)})
		return
	}
	order := braid.NewBraidOrder(b, p.idx, p.roster)
	r.mu.Lock()
	p.b, p.order = b, order
	r.mu.Unlock()
	r.logMu.Lock()
	r.meta.Prefix, r.meta.Braid = p.prefix, true
	err = r.kv.put(r.meta.encode(), recMeta)
	r.logMu.Unlock()
	if err != nil {
		return
	}
	r.bp.Store(b)
	r.n.attachAll()
	r.kick()
	r.maybeAdmin()
}

// maybeAdmin starts the admin side of a device the roles now name, once it has joined the braid.
func (r *Room) maybeAdmin() {
	r.mu.Lock()
	p, running := r.promo, r.admin != nil
	r.mu.Unlock()
	if running || p == nil || p.b == nil || !r.view.Load().roles.Admin(r.self) {
		return
	}
	if err := r.startAdmin(p.b, p.order); err != nil && r.ctx.Err() == nil {
		r.emit(Event{Kind: MembersChanged, Err: fmt.Errorf("room: starting as an admin: %w", err)})
	}
}

// holdBraid keeps the braid a reopened device joined before the roles named it.
func (r *Room) holdBraid(b *kolobraid.Braid, order *braid.BraidOrder, prefix string) {
	r.mu.Lock()
	r.promo = &promotion{prefix: prefix, b: b, order: order}
	r.mu.Unlock()
	r.bp.Store(b)
	r.n.attachAll()
}
