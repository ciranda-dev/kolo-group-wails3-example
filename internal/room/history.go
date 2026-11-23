package room

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
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
	owner  [32]byte
	header []byte
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

// prevLog is a control log that came before the log of epoch, named by that log's first record, which is in the log from.
type prevLog struct {
	epoch  uint64
	owner  [32]byte
	header []byte
	from   logKey
}

func (p prevLog) key() logKey {
	hd, err := kololog.DecodeLogHeader(p.header)
	if err != nil {
		return logKey{}
	}
	return keyOf(p.owner, hd.LogID)
}

// notePrev remembers, once, the control log before l when History may need it: a log of an epoch before the join that the
// policy lets this device read. It is followed at once when History is loading an epoch that needs it.
func (r *Room) notePrev(l *roomLog, rec *ctlRecord) {
	if rec.Epoch > r.joinEpoch || rec.Epoch <= r.joinHist().floor(r.joinEpoch) {
		return
	}
	p := prevLog{epoch: rec.Epoch, owner: l.owner, header: bytes.Clone(rec.Header), from: l.key()}
	r.hmu.Lock()
	dup := slices.ContainsFunc(r.prevs, func(x prevLog) bool { return x.key() == p.key() })
	if !dup {
		r.prevs = append(r.prevs, p)
	}
	r.hmu.Unlock()
	if need := r.needPast.Load(); !dup && need != 0 && p.epoch >= need {
		r.followPrev(p)
	}
}

// followPrev follows a control log that History needs; it is of an epoch before the one of the log that named it.
func (r *Room) followPrev(p prevLog) {
	if _, err := r.replicateAs(&roomLog{owner: p.owner, header: p.header, ctl: true, past: true, top: p.epoch - 1}); err != nil && r.ctx.Err() == nil {
		r.emit(Event{Kind: CatchingUp, Err: fmt.Errorf("%w: %w", ErrCatchUp, err)})
	}
}

// pastRead reports whether every control log that History needs for epoch e has been followed and read to its end: each
// followed control log has its first record read, which names the log before it, and each log before one of an epoch
// after e is followed and read to its last record.
func (r *Room) pastRead(e uint64) bool {
	r.hmu.Lock()
	prevs := slices.Clone(r.prevs)
	r.hmu.Unlock()
	r.logMu.Lock()
	defer r.logMu.Unlock()
	for _, l := range r.logs {
		if l.ctl && !l.own && l.applied == 0 && !l.waited {
			return false
		}
	}
	for _, p := range prevs {
		if p.epoch <= e {
			continue
		}
		if l := r.logs[p.key()]; l == nil || !l.done || l.applied < l.view.Len() {
			return false
		}
	}
	return true
}

// giveUp stops waiting for what pastRead(e) still lacks after CatchUpWait: the first record of a control log, and a log
// before one that no peer serves to its end, with the logs it named. Each log given up is reported in the next page.
func (r *Room) giveUp(e uint64) {
	r.hmu.Lock()
	prevs := slices.Clone(r.prevs)
	r.hmu.Unlock()
	var drop []logKey
	r.logMu.Lock()
	for _, l := range r.logs {
		if l.ctl && !l.own && l.applied == 0 {
			l.waited = true
		}
	}
	for _, p := range prevs {
		k := p.key()
		if l := r.logs[k]; p.epoch <= e || l != nil && l.done && l.applied >= l.view.Len() {
			continue
		}
		drop = append(drop, k)
		if l := r.logs[k]; l != nil && l.past {
			r.forgetLocked(l, true)
		}
		r.hreports = append(r.hreports, Event{Kind: Message, Epoch: p.epoch - 1, ID: MessageID(sha256Of(k[:])),
			Err: fmt.Errorf("%w: no peer serves the control log of device %x before epoch %d", ErrCatchUp, p.owner[:4], p.epoch)})
	}
	r.logMu.Unlock()
	r.hmu.Lock()
	defer r.hmu.Unlock()
	for n := -1; n != len(r.prevs); {
		n = len(r.prevs)
		r.prevs = slices.DeleteFunc(r.prevs, func(p prevLog) bool {
			if slices.Contains(drop, p.from) && !slices.Contains(drop, p.key()) {
				drop = append(drop, p.key())
			}
			return slices.Contains(drop, p.key())
		})
	}
}

// pastOpen remembers a segment of an epoch before the join that the policy lets this device read.
func (r *Room) pastOpen(l *roomLog, rec *ctlRecord) {
	if rec.Epoch < r.joinHist().floor(r.joinEpoch) {
		return
	}
	hd, err := kololog.DecodeLogHeader(rec.Header)
	if err != nil || !bytes.Equal(hd.LogKey, l.owner[:]) {
		return
	}
	s := &pastSeg{owner: l.owner, header: bytes.Clone(rec.Header)}
	r.hmu.Lock()
	defer r.hmu.Unlock()
	k := keyOf(l.owner, hd.LogID)
	if r.pastBy[k] == nil {
		if size, ok := r.early[k]; ok {
			s.closed, s.size = true, size
			delete(r.early, k)
		}
		r.pastBy[k] = s
		r.past[rec.Epoch] = append(r.past[rec.Epoch], s)
	}
}

// pastClose records the final size of a segment that pastOpen knows. The close of the last segment of an epoch is in the
// admin's control log of the next epoch, which a joiner reads before the log that opened the segment, so a close of a
// segment not known yet and not followed is kept for it, up to MaxLogs of them.
func (r *Room) pastClose(l *roomLog, rec *ctlRecord) {
	k := keyOf(l.owner, rec.ID)
	r.logMu.Lock()
	_, followed := r.logs[k]
	r.logMu.Unlock()
	r.hmu.Lock()
	defer r.hmu.Unlock()
	if s := r.pastBy[k]; s != nil {
		s.closed, s.size = true, rec.Size
	} else if !followed && len(r.early) < r.n.limits.MaxLogs {
		r.early[k] = rec.Size
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

// keysOf returns the keys of epoch e: the one in keys, then the other keys that links gave for it.
func (r *Room) keysOf(e uint64) [][keySize]byte {
	r.kmu.RLock()
	defer r.kmu.RUnlock()
	k, ok := r.keys[e]
	if !ok {
		return nil
	}
	return append([][keySize]byte{k}, r.alts[e]...)
}

// derived is a key that links of an epoch give for the epoch before, and the admins whose links give it.
type derived struct {
	key    [keySize]byte
	owners [][32]byte
}

// deriveBack opens the links down to the key of epoch target and keeps each key it derives, in one write. An admin of an
// epoch can write a link that opens to a wrong key, so every key the links give is kept, the one most links agree on first,
// and a message is opened with each; a disagreement is reported in the next page of History.
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
		var got []*derived
		var oerr error
		for _, c := range cands {
			for _, k := range r.keysOf(lo) {
				prev, err := openLink(r.gid[:], lo, &k, c.link[:])
				if err != nil {
					oerr = err
					continue
				}
				i := slices.IndexFunc(got, func(d *derived) bool { return d.key == prev })
				if i < 0 {
					got = append(got, &derived{key: prev})
					i = len(got) - 1
				}
				got[i].owners = append(got[i].owners, c.owner)
				break
			}
		}
		if len(got) == 0 {
			return fmt.Errorf("%w: the link of epoch %d does not open: %w", ErrHistory, lo, oerr)
		}
		slices.SortStableFunc(got, func(a, b *derived) int { return len(b.owners) - len(a.owners) })
		var alts []byte
		r.kmu.Lock()
		r.keys[lo-1], r.lo = got[0].key, lo-1
		for _, d := range got[1:] {
			r.alts[lo-1] = append(r.alts[lo-1], d.key)
			alts = append(alts, d.key[:]...)
		}
		r.kmu.Unlock()
		bt.Put(r.kv.key(recKey, epochKeyName(lo-1)), got[0].key[:])
		if len(got) > 1 {
			bt.Put(r.kv.key(recAlt, epochKeyName(lo-1)), alts)
			var who []string
			for _, d := range got {
				who = append(who, fmt.Sprintf("%x", d.owners[0][:4]))
			}
			r.hreports = append(r.hreports, Event{Kind: Message, Epoch: lo - 1, ID: MessageID(sha256Of(append(epochKeyName(lo), r.gid[:]...))),
				Err: fmt.Errorf("%w: the links of epoch %d give %d keys for epoch %d (devices %s); each is tried", ErrLink, lo, len(got), lo-1, strings.Join(who, ", "))})
		}
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
	r.hbuf = append(r.hreports, r.hbuf...)
	r.hreports = nil
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
	r.needPast.Store(e + 1)
	r.hmu.Lock()
	prevs := slices.Clone(r.prevs)
	r.hmu.Unlock()
	for _, p := range prevs {
		if p.epoch > e {
			r.followPrev(p)
		}
	}
	if !r.waitUntil(ctx, func() bool { return r.pastRead(e) }) && ctx.Err() == nil {
		r.giveUp(e)
	}
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
		l, err := r.replicate(s.owner, s.header, false, e)
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
		for _, key := range r.keysOf(epoch) {
			var ev Event
			if ev, err = r.openStored(st, e, &key); err == nil {
				return ev, nil
			}
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
		behind := l.applied < l.view.Len() || l.past && !l.done
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
		// A peer may offer more than the admin declared, which is read and reported too.
		if st, err := r.fol.Status(l.owner[:], l.id); err != nil || !closed && st.Largest == 0 || st.Largest > st.Len {
			return false
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
