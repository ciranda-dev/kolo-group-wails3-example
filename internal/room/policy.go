package room

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"slices"

	kolocodec "github.com/ciranda-dev/kolo-codec"
	kolomls "github.com/ciranda-dev/kolo-mls"
	"github.com/ciranda-dev/kolo-mls/settle"
)

// policyExtension is the GroupContext extension type of the room policy, from the private-use range of RFC 9420
// section 17.3 next to the roles extension.
const (
	policyExtension uint16 = 0xF003
	policyVersion          = 0
)

// roomPolicy is the policy of a room: its history rule, who may post and its read link. A settled Commit signs it with
// the rest of the GroupContext, and it holds for the epochs from that Commit on.
type roomPolicy struct {
	hist    histPolicy
	persons [][32]byte // persons that may post, in increasing order
	devices [][32]byte // devices that may post, in increasing order
	admins  bool       // the admin devices may post
	link    bool
	secret  [keySize]byte // the read secret, with link
}

// restricted reports whether the policy limits who may post.
func (p *roomPolicy) restricted() bool { return len(p.persons)+len(p.devices) > 0 || p.admins }

// mayPost reports whether a device of person may post under the policy; roles name the admin devices.
func (p *roomPolicy) mayPost(person, device []byte, roles *settle.Roles) bool {
	return !p.restricted() || p.admins && roles.Admin(device) || hasKey(p.persons, person) || hasKey(p.devices, device)
}

func hasKey(keys [][32]byte, k []byte) bool {
	if len(k) != 32 {
		return false
	}
	_, ok := slices.BinarySearchFunc(keys, [32]byte(k), func(a, b [32]byte) int { return bytes.Compare(a[:], b[:]) })
	return ok
}

func (p *roomPolicy) encode() []byte {
	var e kolocodec.Encoder
	e.PutUvarint(policyVersion)
	e.PutUvarint(uint64(p.hist.Rule))
	e.PutUvarint(p.hist.Since)
	flags := uint64(0)
	for i, f := range []bool{p.admins, p.link} {
		if f {
			flags |= 1 << i
		}
	}
	e.PutUvarint(flags)
	for _, keys := range [][][32]byte{p.persons, p.devices} {
		e.PutUvarint(uint64(len(keys)))
		for _, k := range keys {
			e.PutFixed(k[:])
		}
	}
	if p.link {
		e.PutFixed(p.secret[:])
	}
	return e.Bytes()
}

func (p *roomPolicy) extension() kolomls.Extension {
	return kolomls.Extension{Type: policyExtension, Data: p.encode()}
}

func decodeRoomPolicy(b []byte) (*roomPolicy, error) {
	d := kolocodec.NewDecoder(b)
	if v, err := d.Uvarint(); err != nil || v != policyVersion {
		return nil, fmt.Errorf("%w: policy version", ErrMalformed)
	}
	rule, err := d.UvarintMax(uint64(NoHistory))
	if err != nil {
		return nil, fmt.Errorf("%w: policy rule", ErrMalformed)
	}
	p := &roomPolicy{hist: histPolicy{Rule: HistoryRule(rule)}}
	if p.hist.Since, err = d.Uvarint(); err != nil || p.hist.Rule == NoHistory && p.hist.Since != 0 {
		return nil, fmt.Errorf("%w: policy epoch", ErrMalformed)
	}
	flags, err := d.UvarintMax(3)
	if err != nil {
		return nil, fmt.Errorf("%w: policy flags", ErrMalformed)
	}
	p.admins, p.link = flags&1 != 0, flags&2 != 0
	for _, keys := range []*[][32]byte{&p.persons, &p.devices} {
		n, err := d.UvarintMax(uint64(d.Len() / 32))
		if err != nil {
			return nil, fmt.Errorf("%w: posters", ErrMalformed)
		}
		for range n {
			k, err := d.Fixed(32)
			if err != nil || len(*keys) > 0 && bytes.Compare((*keys)[len(*keys)-1][:], k) >= 0 {
				return nil, fmt.Errorf("%w: posters", ErrMalformed)
			}
			*keys = append(*keys, [32]byte(k))
		}
	}
	if p.link {
		s, err := d.Fixed(keySize)
		if err != nil || !p.restricted() {
			return nil, fmt.Errorf("%w: read secret", ErrMalformed)
		}
		copy(p.secret[:], s)
	}
	if err := d.Done(); err != nil {
		return nil, fmt.Errorf("%w: policy trailing bytes", ErrMalformed)
	}
	return p, nil
}

// policyOf reads the policy of a GroupContext; a room without one keeps all its history and lets every member post.
func policyOf(exts []kolomls.Extension) (*roomPolicy, error) {
	for _, e := range exts {
		if e.Type == policyExtension {
			return decodeRoomPolicy(e.Data)
		}
	}
	return &roomPolicy{}, nil
}

// build checks the policy a caller gives and returns it as the room keeps it. A room that has a read link keeps its
// secret, so the link stays the same until the link is turned off and on again; prev is the room's policy, nil at Create.
func (p Policy) build(prev *roomPolicy) (*roomPolicy, error) {
	switch {
	case p.History > NoHistory:
		return nil, errInvalid("history rule %d", p.History)
	case p.History != SinceEpoch && p.Since != 0:
		return nil, errInvalid("an epoch with a rule that has none")
	}
	rp := &roomPolicy{hist: histPolicy{Rule: p.History, Since: p.Since}, admins: p.AdminsPost, link: p.ReadLink}
	var err error
	if rp.persons, err = sortedKeys(p.Posters); err != nil {
		return nil, err
	}
	if rp.devices, err = sortedKeys(p.PosterDevices); err != nil {
		return nil, err
	}
	if rp.link {
		if !rp.restricted() {
			return nil, errInvalid("a read link needs a list of posters: its readers cannot tell who is a member")
		}
		if prev != nil && prev.link {
			rp.secret = prev.secret
		} else if _, err := rand.Read(rp.secret[:]); err != nil {
			return nil, err
		}
	}
	return rp, nil
}

func sortedKeys(in []ed25519.PublicKey) ([][32]byte, error) {
	out := make([][32]byte, 0, len(in))
	for _, k := range in {
		if len(k) != ed25519.PublicKeySize {
			return nil, errInvalid("a key of %d bytes", len(k))
		}
		out = append(out, [32]byte(k))
	}
	slices.SortFunc(out, func(a, b [32]byte) int { return bytes.Compare(a[:], b[:]) })
	return slices.Compact(out), nil
}

// groupExtensions are the GroupContext extensions of a room: the roles and the policy.
func groupExtensions(roles *settle.Roles, p *roomPolicy) ([]kolomls.Extension, error) {
	exts, err := settle.GroupExtensions(roles)
	if err != nil {
		return nil, err
	}
	return append(exts, p.extension()), nil
}

// public returns the policy as a caller gave it, without the read secret.
func (p *roomPolicy) public() Policy {
	out := Policy{History: p.hist.Rule, AdminsPost: p.admins, ReadLink: p.link}
	if p.hist.Rule == SinceEpoch {
		out.Since = p.hist.Since
	}
	for _, k := range p.persons {
		out.Posters = append(out.Posters, ed25519.PublicKey(slices.Clone(k[:])))
	}
	for _, k := range p.devices {
		out.PosterDevices = append(out.PosterDevices, ed25519.PublicKey(slices.Clone(k[:])))
	}
	return out
}

// polRec is the policy that holds from an epoch on.
type polRec struct {
	epoch uint64
	p     *roomPolicy
}

// notePolicy remembers the policy of a view, when it differs from the one before, so a message of an older epoch is
// judged by the policy of its own epoch.
func (r *Room) notePolicy(v *view) error {
	r.kmu.Lock()
	defer r.kmu.Unlock()
	if n := len(r.pols); n > 0 && r.pols[n-1].epoch >= v.epoch || n > 0 && bytes.Equal(r.pols[n-1].p.encode(), v.pol.encode()) {
		return nil
	}
	r.pols = append(r.pols, polRec{v.epoch, v.pol})
	return r.kv.put(v.pol.encode(), recPolicy, epochKeyName(v.epoch))
}

// policyAt returns the policy of epoch e; for an epoch before the first one this device holds, that one.
func (r *Room) policyAt(e uint64) *roomPolicy {
	r.kmu.RLock()
	defer r.kmu.RUnlock()
	for i := len(r.pols) - 1; i >= 0; i-- {
		if r.pols[i].epoch <= e {
			return r.pols[i].p
		}
	}
	return r.pols[0].p
}

// mayPostAt reports whether a device of person may post a message of epoch e. A message of an epoch before this device's
// join is judged by History, which holds no policy of those epochs.
func (r *Room) mayPostAt(v *view, e uint64, person, device []byte) bool {
	if e < r.joinEpoch {
		return true
	}
	roles := v.roles
	if ev := v.at(e, r.n.now()); ev != nil {
		roles = ev.roles
	}
	return r.policyAt(e).mayPost(person, device, roles)
}

// Policy returns the room's policy of the current epoch; a read link shows as a flag only.
func (r *Room) Policy() Policy { return r.view.Load().pol.public() }

// SetPolicy changes the room's policy with a Commit that the admins settle. It holds for the epochs from that Commit on,
// never for the ones before: a message sealed earlier is judged by the policy of its own epoch, and a history rule never
// reaches back before the epochs its links already cover. Only an admin sets the policy.
func (r *Room) SetPolicy(ctx context.Context, p Policy) error {
	v := r.view.Load()
	if r.reader {
		return ErrNotAdmin
	}
	np, err := p.build(v.pol)
	if err != nil {
		return err
	}
	np.hist = v.pol.hist.next(np.hist, v.epoch)
	if bytes.Equal(np.encode(), v.pol.encode()) {
		return nil
	}
	exts, err := groupExtensions(v.roles, np)
	if err != nil {
		return err
	}
	_, _, _, err = r.commit(ctx, kolomls.CommitOptions{SetExtensions: true, Extensions: exts})
	return err
}
