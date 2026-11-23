package room

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	kolocodec "github.com/ciranda-dev/kolo-codec"
	kologossip "github.com/ciranda-dev/kolo-gossip"
	kololog "github.com/ciranda-dev/kolo-log"
	"golang.org/x/crypto/chacha20poly1305"
)

const (
	linkLabel = "kolo-group/link/v0"
	linkSize  = nonceSize + keySize + chacha20poly1305.Overhead
	pollEvery = 10 * time.Millisecond
)

// histPolicy is the history rule of a room: links are written for the epochs after Since, so Since is also the oldest epoch
// a joiner can reach.
type histPolicy struct {
	Rule  HistoryRule
	Since uint64 // with AllHistory and SinceEpoch
}

// allows reports whether the policy lets a link of epoch e exist: the link that opens the key of e-1. A link is never
// written for an epoch the policy leaves out, so a new policy is not retroactive.
func (p histPolicy) allows(e uint64) bool { return p.Rule != NoHistory && e > p.Since }

// floor is the oldest epoch a device that joined at epoch join can read.
func (p histPolicy) floor(join uint64) uint64 {
	if p.Rule == NoHistory {
		return join
	}
	return min(p.Since, join)
}

// next returns the rule a Commit of epoch e sets when the room wants a rule: links stop or go on from epoch e+1. A chain that
// was cut is not joined again, so a rule that opens history reaches back only to the cut.
func (p histPolicy) next(want histPolicy, e uint64) histPolicy {
	if want.Rule == NoHistory {
		return want
	}
	cut := p.Since
	if p.Rule == NoHistory {
		cut = e
	}
	want.Since = max(want.Since, cut)
	return want
}

func linkAD(gid []byte, epoch uint64) []byte {
	var e kolocodec.Encoder
	e.PutString(linkLabel)
	e.PutBytes(gid)
	e.PutUvarint(epoch)
	return e.Bytes()
}

// sealLink seals the key of epoch-1 under the key of epoch: whoever holds one key opens the next one back.
func sealLink(gid []byte, epoch uint64, key, prev *[keySize]byte) ([]byte, error) {
	nonce, ct, err := seal(key, linkAD(gid, epoch), prev[:])
	if err != nil {
		return nil, err
	}
	return append(nonce[:], ct...), nil
}

// openLink returns the key of epoch-1 that a link holds, or ErrOpen.
func openLink(gid []byte, epoch uint64, key *[keySize]byte, link []byte) ([keySize]byte, error) {
	if len(link) != linkSize {
		return [keySize]byte{}, fmt.Errorf("%w: link", ErrMalformed)
	}
	pt, err := open(key, [nonceSize]byte(link[:nonceSize]), linkAD(gid, epoch), link[nonceSize:])
	if err != nil {
		return [keySize]byte{}, err
	}
	return [keySize]byte(pt), nil
}

// linkRec is a history link and the admin that wrote it.
type linkRec struct {
	owner [32]byte
	link  [linkSize]byte
}

// pastSeg is a segment of an epoch before this device joined, named by an admin's control log.
type pastSeg struct {
	ref    objRef
	id     kololog.LogID
	closed bool
	size   uint64
}

// writeLink writes the link of the epoch this device has just entered to its control log, before any segment of that
// epoch, so a reader can tell a link that came late.
func (r *Room) writeLink(v *view) error {
	r.logMu.Lock()
	admin := r.ctl != nil
	r.logMu.Unlock()
	if !admin || !v.pol.hist.allows(v.epoch) {
		return nil
	}
	r.kmu.RLock()
	prev, ok := r.keys[v.epoch-1]
	r.kmu.RUnlock()
	if !ok {
		return nil
	}
	link, err := sealLink(r.gid[:], v.epoch, &v.key, &prev)
	if err != nil {
		return err
	}
	return r.appendCtl(&ctlRecord{Kind: ctlLink, Epoch: v.epoch, Value: link})
}

// addLink keeps the link of an admin's control log, or reports why it is refused: the policy leaves its epoch out, or
// its admin had already opened a segment of that epoch or a later one. A link of an epoch before the join that the
// policy of the join leaves out is of no use to this device, and no report: the policy of its epoch may have allowed it.
func (r *Room) addLink(l *roomLog, rec *ctlRecord) {
	r.logMu.Lock()
	late := l.opened && l.openedTo >= rec.Epoch
	r.logMu.Unlock()
	pol := r.policyAt(rec.Epoch).hist
	if rec.Epoch < r.joinEpoch {
		pol = r.joinHist()
		if !pol.allows(rec.Epoch) {
			return
		}
	}
	var cause error
	switch {
	case !pol.allows(rec.Epoch):
		cause = errors.New("the room's policy leaves its epoch out")
	case late:
		cause = errors.New("written after a segment of its epoch")
	}
	if cause != nil {
		r.emit(Event{Kind: CatchingUp, Epoch: rec.Epoch, Err: fmt.Errorf("%w: link of epoch %d from device %x: %w", ErrLink, rec.Epoch, l.owner[:4], cause)})
		return
	}
	if rec.Epoch > r.joinEpoch {
		return
	}
	r.hmu.Lock()
	defer r.hmu.Unlock()
	if !slices.ContainsFunc(r.links[rec.Epoch], func(x linkRec) bool { return x.owner == l.owner }) {
		r.links[rec.Epoch] = append(r.links[rec.Epoch], linkRec{owner: l.owner, link: [linkSize]byte(rec.Value)})
	}
}

// pastOpen remembers a segment of an epoch before the join that the policy lets this device read.
func (r *Room) pastOpen(l *roomLog, i uint64, rec *ctlRecord) {
	if rec.Epoch < r.joinHist().floor(r.joinEpoch) {
		return
	}
	hd, err := kololog.DecodeLogHeader(rec.Header)
	if err != nil {
		return
	}
	s := &pastSeg{ref: objRef{l, i}, id: hd.LogID}
	r.hmu.Lock()
	defer r.hmu.Unlock()
	k := keyOf(l.owner, hd.LogID)
	if r.pastBy[k] == nil {
		r.pastBy[k] = s
		r.past[rec.Epoch] = append(r.past[rec.Epoch], s)
	}
}

// pastClose records the final size of a segment that pastOpen knows.
func (r *Room) pastClose(l *roomLog, rec *ctlRecord) {
	r.hmu.Lock()
	defer r.hmu.Unlock()
	if s := r.pastBy[keyOf(l.owner, rec.ID)]; s != nil {
		s.closed, s.size = true, rec.Size
	}
}

func (r *Room) isPast(l *roomLog) bool { return !l.ctl && l.epoch < r.joinEpoch }

// joinHist is the history rule this device joined under; a later rule does not change what it may read of its past.
func (r *Room) joinHist() histPolicy { return r.policyAt(r.joinEpoch).hist }

// waitUntil polls cond until it holds, ctx ends or CatchUpWait passes, and reports whether it holds.
func (r *Room) waitUntil(ctx context.Context, cond func() bool) bool {
	deadline := time.Now().Add(r.n.limits.CatchUpWait)
	for !cond() {
		if ctx.Err() != nil || r.ctx.Err() != nil || time.Now().After(deadline) {
			return false
		}
		select {
		case <-time.After(pollEvery):
		case <-ctx.Done():
		case <-r.ctx.Done():
		}
	}
	return true
}

// deriveBack opens the links down to the key of epoch target and keeps each key it derives, in one write.
func (r *Room) deriveBack(ctx context.Context, target uint64) (err error) {
	r.kmu.RLock()
	lo := r.lo
	r.kmu.RUnlock()
	if lo <= target {
		return nil
	}
	bt := r.kv.b.NewBatch()
	defer func() { err = errors.Join(err, bt.Commit()) }()
	for {
		r.kmu.RLock()
		lo := r.lo
		key := r.keys[lo]
		r.kmu.RUnlock()
		if lo <= target {
			return nil
		}
		var cands []linkRec
		have := r.waitUntil(ctx, func() bool {
			r.hmu.Lock()
			defer r.hmu.Unlock()
			cands = slices.Clone(r.links[lo])
			return len(cands) > 0
		})
		if !have {
			if err := ctx.Err(); err != nil {
				return err
			}
			return fmt.Errorf("%w: no link of epoch %d", ErrHistory, lo)
		}
		var prev [keySize]byte
		var oerr error
		for _, c := range cands {
			if prev, oerr = openLink(r.gid[:], lo, &key, c.link[:]); oerr == nil {
				break
			}
		}
		if oerr != nil {
			return fmt.Errorf("%w: the link of epoch %d does not open: %w", ErrHistory, lo, oerr)
		}
		bt.Put(r.kv.key(recKey, epochKeyName(lo-1)), prev[:])
		r.kmu.Lock()
		r.keys[lo-1], r.lo = prev, lo-1
		r.kmu.Unlock()
	}
}

// History returns up to n messages older than before, oldest first, from the epochs this device joined after, as far
// back as the room's policy allows. The zero id starts from the newest of them; any other must be the oldest message of the
// page the last call returned. A message that could not be read is an Event with Err set, never left out. It returns
// ErrHistory when the chain of keys cannot go on.
func (r *Room) History(ctx context.Context, before MessageID, n int) ([]Event, error) {
	switch {
	case n <= 0:
		return nil, errInvalid("%d events", n)
	case r.removed.Load():
		return nil, ErrRemoved
	case r.ctx.Err() != nil:
		return nil, ErrClosed
	}
	r.histMu.Lock()
	defer r.histMu.Unlock()
	switch {
	case before == (MessageID{}):
		r.hbound, r.hbuf, r.hlast, r.hseen = r.joinEpoch, nil, MessageID{}, newIDSet(r.n.limits.MaxKnown)
	case before != r.hlast:
		return nil, errInvalid("before is not the oldest message of the last page")
	}
	floor := r.joinHist().floor(r.joinEpoch)
	if r.hbound > floor {
		r.waitUntil(ctx, r.controlRead)
	}
	var loadErr error
	for len(r.hbuf) < n && r.hbound > floor {
		evs, err := r.loadEpoch(ctx, r.hbound-1)
		if err != nil {
			loadErr = err
			break
		}
		r.hbuf = append(evs, r.hbuf...)
		r.hbound--
	}
	if len(r.hbuf) == 0 {
		return nil, loadErr
	}
	k := max(0, len(r.hbuf)-n)
	page := slices.Clone(r.hbuf[k:])
	r.hbuf = r.hbuf[:k]
	r.hlast = page[0].ID
	return page, nil
}

// loadEpoch reads the messages of the segments that admins wrote in epoch e, which is before this device joined.
func (r *Room) loadEpoch(ctx context.Context, e uint64) ([]Event, error) {
	if err := r.deriveBack(ctx, e); err != nil {
		return nil, err
	}
	r.hmu.Lock()
	segs := slices.Clone(r.past[e])
	closes := make([]pastSeg, len(segs))
	for i, s := range segs {
		closes[i] = *s
	}
	r.hmu.Unlock()
	var logs []*roomLog
	defer func() {
		r.logMu.Lock()
		for _, l := range logs {
			r.forgetLocked(l, true)
		}
		r.logMu.Unlock()
	}()
	var out []Event
	for _, s := range closes {
		b, err := s.ref.l.view.Get(s.ref.i)
		var rec *ctlRecord
		if err == nil {
			rec, err = decodeCtl(b, r.n.limits.MaxMessageSize)
		}
		if err != nil {
			out = append(out, r.entryError(s.ref.l, s.ref.i, MessageID(sha256Of(b)), nil, e, err))
			continue
		}
		l, err := r.replicate(s.ref.l.owner, rec.Header, false, e)
		if err != nil {
			return nil, err
		}
		r.logMu.Lock()
		l.closed, l.size = s.closed, s.size
		r.logMu.Unlock()
		logs = append(logs, l)
	}
	r.waitUntil(ctx, func() bool { return r.allRead(logs) })
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var msgs []Event
	for _, l := range logs {
		r.logMu.Lock()
		gap := l.closed && !l.complete()
		n := l.view.Len()
		r.logMu.Unlock()
		if gap {
			out = append(out, Event{Kind: Message, Epoch: e, ID: MessageID(sha256Of(l.id[:])), Err: fmt.Errorf("%w: a segment of device %x holds %d of %d entries", ErrCatchUp, l.owner[:4], n, l.size)})
		}
		for i := range n {
			ev, err := r.readPast(ctx, l, i, e)
			switch {
			case err != nil:
				out = append(out, ev)
			case r.hseen.add(ev.ID):
				msgs = append(msgs, ev)
			}
		}
	}
	return append(out, Display(msgs)...), nil
}

// readPast reads entry i of a segment of epoch e; an Event with Err set is returned with its error.
func (r *Room) readPast(ctx context.Context, l *roomLog, i, e uint64) (Event, error) {
	b, err := l.view.Get(i)
	if err != nil {
		return r.entryError(l, i, MessageID(sha256Of(l.id[:])), nil, e, err), err
	}
	st, err := decodeStored(b)
	if err != nil {
		return r.entryError(l, i, MessageID(sha256Of(b)), nil, e, err), err
	}
	id := messageID(r.gid[:], st.Device[:], st.Seq, st.Body)
	epoch := st.sealed.Epoch
	if epoch > e || epoch < r.joinHist().floor(r.joinEpoch) {
		err = ErrWrongEpoch
	} else if err = r.deriveBack(ctx, epoch); err == nil {
		r.kmu.RLock()
		key := r.keys[epoch]
		r.kmu.RUnlock()
		var ev Event
		if ev, err = r.openStored(st, e, &key); err == nil {
			return ev, nil
		}
	}
	return r.entryError(l, i, id, st.Device[:], epoch, err), err
}

// controlRead reports whether every control log has been read as far as this device holds it and no peer offers more.
func (r *Room) controlRead() bool {
	for _, l := range r.snapshot() {
		if !l.ctl || l.own {
			continue
		}
		r.logMu.Lock()
		behind := l.applied < l.view.Len()
		r.logMu.Unlock()
		if st, err := r.fol.Status(l.owner[:], l.id); behind || err == nil && st.Largest > st.Len {
			return false
		}
	}
	return true
}

// allRead reports whether every log holds all its entries, with their keys: a segment its admin closed has its final
// size, and one it did not close is taken as complete when no peer offers more.
func (r *Room) allRead(logs []*roomLog) bool {
	for _, l := range logs {
		r.logMu.Lock()
		n, keyed, closed, size := l.view.Len(), l.keyed, l.closed, l.size
		r.logMu.Unlock()
		if keyed < n || closed && n < size {
			return false
		}
		if !closed {
			if st, err := r.fol.Status(l.owner[:], l.id); err != nil || st.Largest == 0 || st.Largest > st.Len {
				return false
			}
		}
	}
	return true
}

// entryError is the Event that reports an entry of a segment that could not be read.
func (r *Room) entryError(l *roomLog, i uint64, id MessageID, device []byte, epoch uint64, cause error) Event {
	return Event{Kind: Message, ID: id, Device: bytes.Clone(device), Epoch: epoch, Err: fmt.Errorf("%w: entry %d of a segment of device %x: %w", ErrCatchUp, i, l.owner[:4], cause)}
}

// openStored checks a stored message of a segment of epoch at against its author and opens it with the key of its epoch.
func (r *Room) openStored(st *stored, at uint64, key *[keySize]byte) (Event, error) {
	au := r.authorOf(st.Device)
	if au == nil {
		return Event{}, kologossip.ErrUnknownAuthor
	}
	if err := verifySealed(st.Device[:], r.gid[:], st.sealed, st.Seq); err != nil {
		return Event{}, err
	}
	if !r.mayPostAt(r.view.Load(), st.sealed.Epoch, storedAt(st.sealed.Epoch, at), au.Person, st.Device[:]) {
		return Event{}, ErrNotPoster
	}
	c, err := r.openContent(key, st.sealed, st.Device[:], st.Seq)
	if err != nil {
		return Event{}, err
	}
	return Event{Kind: Message, ID: messageID(r.gid[:], st.Device[:], st.Seq, st.Body), ReplyTo: c.ReplyTo, Parents: c.Parents, Person: bytes.Clone(au.Person),
		Device: bytes.Clone(st.Device[:]), Time: time.UnixMilli(c.Time), Body: c.Body, Epoch: st.sealed.Epoch}, nil
}
