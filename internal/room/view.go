package room

import (
	"bytes"
	"crypto/ed25519"
	"fmt"
	"time"

	kologossip "github.com/ciranda-dev/kolo-gossip"
	"github.com/ciranda-dev/kolo-mls/cred"
	"github.com/ciranda-dev/kolo-mls/settle"
)

// view is what a room knows of one epoch, read without locks by the validator and the gossip roster.
type view struct {
	epoch   uint64
	key     [keySize]byte
	authors map[[32]byte]*kologossip.Author
	members []Member
	roles   *settle.Roles
	// prev is the epoch before, kept until prevUntil so messages in flight still pass.
	prev      *view
	prevUntil time.Time
}

// newView reads the tree and roles of m's epoch. Authors of the old view are reused for unchanged credentials.
func newView(m *settle.Member, old *view, now time.Time, window time.Duration) (*view, error) {
	roles, err := settle.RolesOf(m.Extensions(), m.Limits())
	if err != nil {
		return nil, err
	}
	key, err := epochKey(m.Export, m.GroupID(), m.Epoch())
	if err != nil {
		return nil, err
	}
	v := &view{epoch: m.Epoch(), key: key, roles: roles, authors: map[[32]byte]*kologossip.Author{}}
	t := m.Tree()
	for _, l := range t.Members() {
		c, err := cred.Decode(t.Leaf(l).Credential)
		if err != nil {
			return nil, err
		}
		dev := [32]byte(c.Device())
		a := old.author(dev)
		if a == nil || !bytes.Equal(a.Cert.Encode(), c.Cert.Encode()) {
			a = &kologossip.Author{Person: c.Person(), Cert: c.Cert}
		}
		v.authors[dev] = a
		role := Role(0)
		switch {
		case roles.Settler(dev[:]):
			role = Settler
		case roles.Admin(dev[:]):
			role = Admin
		}
		v.members = append(v.members, Member{Person: bytes.Clone(c.Person()), Device: bytes.Clone(c.Device()), Role: role})
	}
	if old != nil {
		p := *old
		p.prev = nil
		v.prev, v.prevUntil = &p, now.Add(window)
	}
	return v, nil
}

func (v *view) author(dev [32]byte) *kologossip.Author {
	if v == nil {
		return nil
	}
	return v.authors[dev]
}

// at returns the view that holds epoch at now: this one, or the previous one while its window lasts.
func (v *view) at(epoch uint64, now time.Time) *view {
	switch {
	case epoch == v.epoch:
		return v
	case v.prev != nil && epoch == v.prev.epoch && now.Before(v.prevUntil):
		return v.prev
	}
	return nil
}

// authors answers gossip's roster from the tree of the current epoch, and of the previous one while its window lasts.
type authors struct{ r *Room }

func (a authors) Lookup(_ kologossip.Topic, person, device ed25519.PublicKey) (*kologossip.Author, error) {
	v := a.r.view.Load()
	if v == nil || len(device) != ed25519.PublicKeySize {
		return nil, kologossip.ErrUnknownAuthor
	}
	d := [32]byte(device)
	au := v.author(d)
	if au == nil && v.prev != nil && a.r.n.now().Before(v.prevUntil) {
		au = v.prev.author(d)
	}
	if au == nil || !bytes.Equal(au.Person, person) {
		return nil, fmt.Errorf("%w", kologossip.ErrUnknownAuthor)
	}
	return au, nil
}
