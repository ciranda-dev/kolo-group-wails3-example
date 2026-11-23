package room

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"

	kologossip "github.com/ciranda-dev/kolo-gossip"
	kololog "github.com/ciranda-dev/kolo-log"
	"github.com/ciranda-dev/kolo-pin"
	kolosecure "github.com/ciranda-dev/kolo-secure"
	kolosync "github.com/ciranda-dev/kolo-sync"

	"github.com/ciranda-dev/kolo-mls/settle"
)

// idSet remembers the newest ids it was given, up to max.
type idSet struct {
	mu   sync.Mutex
	max  int
	m    map[MessageID]struct{}
	ring []MessageID
}

func newIDSet(max int) *idSet { return &idSet{max: max, m: map[MessageID]struct{}{}} }

func (s *idSet) add(id MessageID) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.m[id]; ok {
		return false
	}
	s.m[id] = struct{}{}
	s.ring = append(s.ring, id)
	for len(s.ring) > s.max {
		delete(s.m, s.ring[0])
		s.ring = s.ring[1:]
	}
	return true
}

func (s *idSet) has(id MessageID) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.m[id]
	return ok
}

// moved records that something grew or was read, so a device that is catching up is not taken for one that is stuck.
func (r *Room) moved() { r.progress.Store(r.n.now().UnixNano()) }

func (r *Room) kick() {
	select {
	case r.syncKick <- struct{}{}:
	default:
	}
}

// followEvents turns what the follower reports into work for the sync loop and reports of divergence.
func (r *Room) followEvents() {
	for {
		ev, err := r.fol.Next(r.ctx)
		if err != nil {
			return
		}
		switch ev.Kind {
		case kolosync.FollowGrew:
			r.moved()
			r.kick()
		case kolosync.FollowDiverged:
			r.emit(Event{Kind: CatchingUp, Err: fmt.Errorf("%w: a log of device %x holds two histories: %w", ErrCatchUp, ev.Owner[:4], ev.Err)})
		}
	}
}

// syncLoop reads what the followed logs hold: it applies the Commits of the control logs, reads the messages of the
// segments, and asks the pins when the peers cannot bring the device up to date.
func (r *Room) syncLoop() {
	l := r.n.limits
	t := time.NewTicker(max(10*time.Millisecond, min(l.PinWait, l.CatchUpWait, l.RepublishAfter)/2))
	defer t.Stop()
	for {
		r.syncOnce()
		select {
		case <-r.syncKick:
		case <-t.C:
		case <-r.ctx.Done():
			return
		}
	}
}

func (r *Room) syncOnce() {
	r.retryDirs()
	for r.scanControl() {
	}
	r.advance()
	for _, l := range r.snapshot() {
		if !l.ctl && !r.isPast(l) {
			r.scanSegment(l)
		}
	}
	r.reportGaps()
	r.republish()
	r.logMu.Lock()
	r.trimLocked()
	r.logMu.Unlock()
	r.checkPins()
}

func (r *Room) snapshot() []*roomLog {
	r.logMu.Lock()
	defer r.logMu.Unlock()
	out := make([]*roomLog, 0, len(r.logs))
	for _, l := range r.logs {
		out = append(out, l)
	}
	return out
}

// scanControl derives keys and applies the new entries of every control log; it reports whether it added logs.
func (r *Room) scanControl() (grew bool) {
	for _, l := range r.snapshot() {
		r.logMu.Lock()
		err := l.deriveKeys()
		r.logMu.Unlock()
		if err != nil && r.ctx.Err() == nil {
			r.reportOnce(l, err)
		}
		if !l.ctl || l.own {
			continue
		}
		for {
			r.logMu.Lock()
			i, n := l.applied, l.view.Len()
			r.logMu.Unlock()
			if i >= n {
				break
			}
			b, err := l.view.Get(i)
			if errors.Is(err, kololog.ErrKeyUnavailable) {
				break
			}
			r.logMu.Lock()
			l.applied = i + 1
			r.logMu.Unlock()
			if err != nil {
				r.reportOnce(l, err)
				continue
			}
			rec, err := decodeCtl(b, r.n.limits.MaxMessageSize)
			if err != nil {
				r.emit(Event{Kind: CatchingUp, Err: fmt.Errorf("%w: entry %d of a control log: %w", ErrCatchUp, i, err)})
				continue
			}
			r.moved()
			grew = r.applyCtl(l, i, rec) || grew
		}
	}
	return grew
}

func (r *Room) reportOnce(l *roomLog, err error) {
	r.logMu.Lock()
	again := l.reported
	l.reported = true
	r.logMu.Unlock()
	if !again {
		r.emit(Event{Kind: CatchingUp, Err: fmt.Errorf("%w: %w", ErrCatchUp, err)})
	}
}

// applyCtl applies entry i of the control log l of an admin; it reports whether a log was added.
func (r *Room) applyCtl(l *roomLog, i uint64, rec *ctlRecord) bool {
	v := r.view.Load()
	switch rec.Kind {
	case ctlPut:
		if shared(rec.Key) {
			r.idx.put(rec.Key, objRef{l, i})
			r.poke()
		}
	case ctlOpen:
		r.logMu.Lock()
		l.opened, l.openedTo = true, max(l.openedTo, rec.Epoch)
		r.logMu.Unlock()
		switch {
		case !v.roles.Admin(l.owner[:]):
			return false
		case rec.Epoch < r.joinEpoch:
			r.pastOpen(l, i, rec)
			return false
		}
		return r.addLog(l.owner, rec.Header, false, rec.Epoch)
	case ctlLink:
		r.addLink(l, rec)
	case ctlRead:
		r.addRead(rec)
	case ctlClose:
		r.pastClose(l, rec)
		r.logMu.Lock()
		if s := r.logs[keyOf(l.owner, rec.ID)]; s != nil && !s.ctl && !s.own {
			s.closed, s.size = true, rec.Size
			r.persistLogLocked(s)
		}
		r.logMu.Unlock()
	case ctlDir:
		hd, err := kololog.DecodeLogHeader(rec.Header)
		if err != nil || len(hd.LogKey) != 32 {
			return false
		}
		if !v.roles.Admin(hd.LogKey) {
			// The Commit that names the device may not be applied here yet; the entry is looked at again after each epoch.
			r.logMu.Lock()
			if len(r.dirs) < r.n.limits.MaxLogs {
				r.dirs[[32]byte(hd.LogKey)] = rec.Header
			}
			r.logMu.Unlock()
			return false
		}
		return r.addLog([32]byte(hd.LogKey), rec.Header, true, 0)
	}
	return false
}

// retryDirs follows the control logs that admins named before the roles named their owners.
func (r *Room) retryDirs() {
	roles := r.view.Load().roles
	r.logMu.Lock()
	var due [][]byte
	for owner, h := range r.dirs {
		if roles.Admin(owner[:]) {
			due = append(due, h)
			delete(r.dirs, owner)
		}
	}
	r.logMu.Unlock()
	for _, h := range due {
		if hd, err := kololog.DecodeLogHeader(h); err == nil {
			r.addLog([32]byte(hd.LogKey), h, true, 0)
		}
	}
}

// addLog follows a log an admin named, and reports whether it is new.
func (r *Room) addLog(owner [32]byte, header []byte, ctl bool, epoch uint64) bool {
	if owner == [32]byte(r.self) {
		return false
	}
	hd, err := kololog.DecodeLogHeader(header)
	if err != nil {
		return false
	}
	r.logMu.Lock()
	_, have := r.logs[keyOf(owner, hd.LogID)]
	r.logMu.Unlock()
	if have {
		return false
	}
	if _, err := r.replicate(owner, header, ctl, epoch); err != nil {
		if r.ctx.Err() == nil {
			r.emit(Event{Kind: CatchingUp, Err: fmt.Errorf("%w: %w", ErrCatchUp, err)})
		}
		return false
	}
	return true
}

// advance applies, one epoch at a time, the settled Commits the control logs hold after the current epoch. Each admin's
// control log may hold a SettledCommit for the epoch, so it tries them until one applies; a hostile admin's cannot
// block the epoch, and is reported once.
func (r *Room) advance() {
	for r.ctx.Err() == nil && r.advanceOnce() {
	}
}

func (r *Room) advanceOnce() bool {
	if r.reader {
		return false
	}
	r.mu.Lock()
	if r.m.Removed() {
		r.mu.Unlock()
		return false
	}
	e, max := r.m.Epoch(), r.m.MaxCommitBytes()
	r.mu.Unlock()
	key := settle.SettledKey(r.gid[:], e)
	shares := len(r.view.Load().roles.Settlers)
	for _, ref := range r.idx.all(key) {
		sb, err := r.readObject(ref, key)
		if err != nil || len(sb) > settledBound(shares) {
			continue
		}
		st, err := settle.DecodeSettled(sb, shares)
		if err != nil {
			r.reportEpoch(e, sha256Of(sb), err)
			continue
		}
		commit, err := settle.GetObject(r.ctx, r.pins, st.TBS.Commit, max)
		if err != nil {
			continue
		}
		r.mu.Lock()
		if r.m.Epoch() != e {
			r.mu.Unlock()
			return true
		}
		_, err = r.m.ApplySettled(sb, commit)
		var evs []Event
		if err == nil {
			evs = r.refreshLocked()
		}
		r.mu.Unlock()
		if err != nil {
			r.reportEpoch(e, st.TBS.Commit, err)
			continue
		}
		r.mu.Lock()
		clear(r.badCommits)
		r.mu.Unlock()
		r.moved()
		r.applied(evs)
		r.emit(Event{Kind: CatchingUp, Epoch: e + 1})
		return true
	}
	return false
}

// reportEpoch reports a Commit that the control logs hold and the room cannot apply, once for each Commit.
func (r *Room) reportEpoch(epoch uint64, hash [32]byte, err error) {
	r.mu.Lock()
	_, again := r.badCommits[hash]
	r.badCommits[hash] = struct{}{}
	r.mu.Unlock()
	if !again && r.ctx.Err() == nil {
		r.emit(Event{Kind: CatchingUp, Epoch: epoch, Err: fmt.Errorf("%w: the Commit of epoch %d: %w", ErrCatchUp, epoch, err)})
	}
}

// applied does what a new epoch asks of the room outside the lock that applied it.
func (r *Room) applied(evs []Event) {
	r.emit(evs...)
	r.finishRemoval()
	r.epochChanged()
	r.maybeAdmin()
	r.kick()
}

// scanSegment reads the new entries of a segment: it shows the messages the room has not seen and, for a message of this
// device, records that it is stored. An entry it cannot read is reported with ErrCatchUp and never skipped.
func (r *Room) scanSegment(l *roomLog) {
	start := l.cursorNow(r)
	i := start
	for r.ctx.Err() == nil {
		r.logMu.Lock()
		n := l.view.Len()
		r.logMu.Unlock()
		if i >= n {
			break
		}
		b, err := l.view.Get(i)
		if errors.Is(err, kololog.ErrKeyUnavailable) {
			break
		}
		if stop := r.readEntry(l, i, b, err); stop {
			break
		}
		i++
	}
	if i != start {
		r.moved()
		r.logMu.Lock()
		l.cursor = i
		r.persistLogLocked(l)
		r.logMu.Unlock()
	}
}

func (l *roomLog) cursorNow(r *Room) uint64 {
	r.logMu.Lock()
	defer r.logMu.Unlock()
	return l.cursor
}

// readEntry reads entry i of a segment; it reports whether the segment must be read again later, once the room holds the
// key of a later epoch.
func (r *Room) readEntry(l *roomLog, i uint64, b []byte, err error) (wait bool) {
	fail := func(id MessageID, device []byte, epoch uint64, cause error) bool {
		r.emit(r.entryError(l, i, id, device, epoch, cause))
		return false
	}
	if err != nil {
		return fail(MessageID{}, nil, l.epoch, err)
	}
	st, err := decodeStored(b)
	if err != nil {
		return fail(MessageID{}, nil, l.epoch, err)
	}
	epoch := st.sealed.Epoch
	id := messageID(r.gid[:], st.Device[:], st.Seq, st.Body)
	if st.Device == [32]byte(r.self) {
		r.markStored(id)
		return false
	}
	if r.seen.has(id) {
		return false
	}
	// A message of an epoch before the join belongs to History, which holds its key.
	if epoch < r.joinEpoch {
		return false
	}
	r.kmu.RLock()
	key, ok := r.keys[epoch]
	r.kmu.RUnlock()
	if !ok {
		if epoch > r.view.Load().epoch {
			return true
		}
		return fail(id, st.Device[:], epoch, ErrWrongEpoch)
	}
	ev, err := r.openStored(st, &key)
	if err != nil {
		return fail(id, st.Device[:], epoch, err)
	}
	if !r.seen.add(id) {
		return false
	}
	r.release(func() []Event { return r.ord.add(ev, r.n.now()) })
	return false
}

// authorOf returns the author of a device that is, or ever was, a member of the room.
func (r *Room) authorOf(device [32]byte) *kologossip.Author {
	if a := r.view.Load().author(device); a != nil {
		return a
	}
	r.kmu.RLock()
	defer r.kmu.RUnlock()
	return r.ever[device]
}

// reportGaps reports, once, a closed segment that still lacks entries CatchUpWait after the room first saw it so.
func (r *Room) reportGaps() {
	now := r.n.now()
	var gaps []string
	r.logMu.Lock()
	for _, l := range r.logs {
		if l.ctl || r.isPast(l) || !l.closed || l.complete() {
			l.since = time.Time{}
			continue
		}
		if l.since.IsZero() {
			l.since = now
		}
		if !l.reported && now.Sub(l.since) >= r.n.limits.CatchUpWait {
			l.reported = true
			gaps = append(gaps, fmt.Sprintf("a segment of device %x holds %d of %d entries", l.owner[:4], l.view.Len(), l.size))
		}
	}
	r.logMu.Unlock()
	for _, g := range gaps {
		r.emit(Event{Kind: CatchingUp, Err: fmt.Errorf("%w: %s", ErrCatchUp, g)})
	}
}

// receipt is a message this device sent and no segment holds yet.
type receipt struct {
	seq  uint64
	body []byte
	at   time.Time
}

// addReceipt records a message sent until a segment holds it; it fails when the room has MaxReceipts of them.
func (r *Room) addReceipt(id MessageID, seq uint64, body []byte) error {
	r.rmu.Lock()
	defer r.rmu.Unlock()
	if len(r.rcpt) >= r.n.limits.MaxReceipts {
		return ErrTooManyReceipts
	}
	if err := r.kv.put(append(be(seq), body...), recReceipt, id[:]); err != nil {
		return err
	}
	r.rcpt[id] = &receipt{seq: seq, body: body, at: r.n.now()}
	return nil
}

func (r *Room) dropReceipt(id MessageID) bool {
	r.rmu.Lock()
	defer r.rmu.Unlock()
	if _, ok := r.rcpt[id]; !ok {
		return false
	}
	delete(r.rcpt, id)
	r.kv.del(recReceipt, id[:])
	return true
}

func (r *Room) markStored(id MessageID) {
	if r.dropReceipt(id) {
		r.emit(Event{Kind: Stored, ID: id, Epoch: r.view.Load().epoch})
	}
}

// republish publishes again, in a new envelope, the messages whose segments do not hold them after RepublishAfter.
func (r *Room) republish() {
	now := r.n.now()
	type due struct {
		id MessageID
		rc *receipt
	}
	var ds []due
	r.rmu.Lock()
	for id, rc := range r.rcpt {
		if now.Sub(rc.at) >= r.n.limits.RepublishAfter {
			ds = append(ds, due{id, rc})
		}
	}
	slices.SortFunc(ds, func(a, b due) int { return cmp.Compare(a.rc.seq, b.rc.seq) })
	// A message published again takes tokens of the same bucket as a new one, so the peers' limits are never reached.
	for i, d := range ds {
		if !r.out.allow([32]byte(r.self), now) {
			ds = ds[:i]
			break
		}
		d.rc.at = now
	}
	r.rmu.Unlock()
	for _, d := range ds {
		r.sendMu.Lock()
		r.cur.Store(d.rc.seq)
		_, err := r.gr.Publish(r.ctx, d.rc.body)
		r.sendMu.Unlock()
		if err != nil && r.ctx.Err() == nil {
			r.emit(Event{Kind: Stored, ID: d.id, Err: fmt.Errorf("room: publishing a message again: %w", err)})
		}
	}
}

func (r *Room) loadReceipts() error {
	return r.kv.each(recReceipt, func(tail, v []byte) error {
		if len(tail) != idSize || len(v) < 8 {
			return fmt.Errorf("%w: receipt record", ErrMalformed)
		}
		r.rcpt[MessageID(tail)] = &receipt{seq: unbe(v), body: v[8:], at: r.n.now()}
		return nil
	})
}

// catchingUp reports whether a followed log has entries the room has not read or a peer offers more than it holds.
func (r *Room) catchingUp() bool {
	for _, l := range r.snapshot() {
		r.logMu.Lock()
		behind := !l.ctl && !r.isPast(l) && l.cursor < l.view.Len() || l.closed && !l.complete()
		r.logMu.Unlock()
		if behind {
			return true
		}
		if l.rep != nil {
			if st, err := r.fol.Status(l.owner[:], l.id); err == nil && st.Largest > st.Len {
				return true
			}
		}
	}
	return false
}

// adminsOnline counts the admin devices, other than this one, that are connected.
func (r *Room) adminsOnline() int {
	roles := r.view.Load().roles
	n := 0
	r.n.mu.Lock()
	for k := range r.n.conns {
		if roles.Admin(k[:]) && k != [32]byte(r.self) {
			n++
		}
	}
	r.n.mu.Unlock()
	return n
}

// checkPins connects to the pins when no other admin has been connected for PinWait, or the device is behind and nothing
// has grown or been read for PinWait, and lets them go when it is current and an admin is back.
func (r *Room) checkPins() {
	if len(r.n.cfg.Pins) == 0 {
		return
	}
	now := r.n.now()
	noAdmin := r.adminsOnline() == 0
	stalled := r.catchingUp() && now.Sub(time.Unix(0, r.progress.Load())) >= r.n.limits.PinWait
	r.pinMu.Lock()
	defer r.pinMu.Unlock()
	switch {
	case !noAdmin && !stalled:
		r.pinSince = time.Time{}
		for _, c := range r.pinConns {
			c.cancel()
		}
		clear(r.pinConns)
		return
	case stalled:
	case r.pinSince.IsZero():
		r.pinSince = now
		return
	case now.Sub(r.pinSince) < r.n.limits.PinWait:
		return
	}
	for _, p := range r.n.cfg.Pins {
		if r.pinConns[string(p.ID)] == nil {
			ctx, cancel := context.WithCancel(r.ctx)
			c := &pinConn{cancel: cancel}
			r.pinConns[string(p.ID)] = c
			r.wg.Go(func() { r.followPin(ctx, p, c) })
		}
	}
}

type pinConn struct{ cancel context.CancelFunc }

// followPin pulls the room's logs from a pin on a connection until ctx ends, every PinWait: a pin offers a log to the
// first peer that asks for it, so a watch of it that comes first learns nothing (see the library gaps of the roadmap).
func (r *Room) followPin(ctx context.Context, p pin.Peer, c *pinConn) {
	defer func() {
		r.pinMu.Lock()
		if r.pinConns[string(p.ID)] == c {
			delete(r.pinConns, string(p.ID))
		}
		r.pinMu.Unlock()
	}()
	// The connection lives as long as its dial context, so the timeout of the dial is its own.
	dctx, cancel := context.WithCancel(ctx)
	defer cancel()
	timer := time.AfterFunc(r.n.limits.PinWait*2, cancel)
	conn, err := kolosecure.Dial(dctx, p.Addr, r.n.cfg.Device, p.ID, nil)
	if !timer.Stop() || err != nil {
		return
	}
	defer conn.Close()
	for dctx.Err() == nil {
		grew := false
		for _, l := range r.snapshot() {
			if l.rep == nil || l.complete() {
				continue
			}
			before := l.rep.Len()
			// A pin that does not answer must not keep the replica busy, which the follower needs for the other peers.
			pctx, stop := context.WithTimeout(dctx, 2*r.n.limits.PinWait)
			err := kolosync.PullCiphertext(pctx, conn, l.rep)
			stop()
			if err == nil && l.rep.Len() > before {
				r.moved()
				grew = true
			} else if errors.Is(err, kolosync.ErrClosed) || dctx.Err() != nil {
				return
			}
		}
		if grew {
			r.kick()
		}
		select {
		case <-time.After(r.n.limits.PinWait):
		case <-dctx.Done():
		}
	}
}
