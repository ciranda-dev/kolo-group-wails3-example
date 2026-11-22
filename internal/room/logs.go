package room

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	kololog "github.com/ciranda-dev/kolo-log"
	kolostore "github.com/ciranda-dev/kolo-store"
	kolosync "github.com/ciranda-dev/kolo-sync"

	"github.com/ciranda-dev/kolo-mls/settle"
)

// logKey names a log by its owner's device key and its id.
type logKey [64]byte

func keyOf(owner [32]byte, id kololog.LogID) logKey {
	var k logKey
	copy(k[:32], owner[:])
	copy(k[32:], id[:])
	return k
}

// logView is what the room reads of a log, its own or a replica.
type logView interface {
	Len() uint64
	Get(uint64) ([]byte, error)
}

// roomLog is a log of a room: the control log or a segment, written by this device or held as a replica of an admin's.
// The fields after view are guarded by Room.logMu.
type roomLog struct {
	owner   [32]byte
	id      kololog.LogID
	own     bool
	ctl     bool
	epoch   uint64 // of a segment
	header  []byte
	src     kolosync.Source
	view    logView
	log     *kolostore.Log
	rep     *kolostore.Replica
	deriver *kololog.KeyDeriver

	closed   bool   // a segment whose writer declared its final size
	size     uint64 // that size
	cursor   uint64 // entries of a segment already read
	keyed    uint64 // entries of a replica that have their keys
	applied  uint64 // entries of a control log already applied
	since    time.Time
	reported bool
	opened   bool   // a control log that has opened a segment
	openedTo uint64 // the newest epoch it opened one for
}

func (l *roomLog) key() logKey { return keyOf(l.owner, l.id) }

func (l *roomLog) complete() bool { return l.closed && l.view.Len() >= l.size }

func (l *roomLog) close() {
	if l.deriver != nil {
		l.deriver.Close()
	}
	switch {
	case l.log != nil:
		l.log.Close()
	case l.rep != nil:
		l.rep.Close()
	}
}

func (l *roomLog) record() *logRec {
	return &logRec{Ctl: l.ctl, Own: l.own, Epoch: l.epoch, Closed: l.closed, Size: l.size, Cursor: l.cursor, Header: l.header}
}

// objRef is where a Commit or SettledCommit sits: an entry of a control log.
type objRef struct {
	l *roomLog
	i uint64
}

// objIndex maps the keys of settle.Pins to the control log entries that hold them: one for each log, since an admin may
// write a value for a key that another admin settled, and a reader must try them all.
type objIndex struct {
	mu   sync.RWMutex
	keys []string
	refs map[string][]objRef
}

func (x *objIndex) put(key string, ref objRef) {
	x.mu.Lock()
	defer x.mu.Unlock()
	if x.refs == nil {
		x.refs = map[string][]objRef{}
	}
	rs := x.refs[key]
	if slices.ContainsFunc(rs, func(o objRef) bool { return o.l == ref.l }) {
		return
	}
	if len(rs) == 0 {
		i, _ := slices.BinarySearch(x.keys, key)
		x.keys = slices.Insert(x.keys, i, key)
	}
	x.refs[key] = append(rs, ref)
}

func (x *objIndex) all(key string) []objRef {
	x.mu.RLock()
	defer x.mu.RUnlock()
	return slices.Clone(x.refs[key])
}

// list returns up to max keys with prefix that sort after the given one; max 0 or less is no bound.
func (x *objIndex) list(prefix, after string, max int) []string {
	x.mu.RLock()
	defer x.mu.RUnlock()
	i := sort.SearchStrings(x.keys, max2(prefix, after))
	var out []string
	for ; i < len(x.keys) && strings.HasPrefix(x.keys[i], prefix); i++ {
		if x.keys[i] <= after {
			continue
		}
		if max > 0 && len(out) == max {
			break
		}
		out = append(out, x.keys[i])
	}
	return out
}

func max2(a, b string) string {
	if a > b {
		return a
	}
	return b
}

const (
	objectPrefix  = "kolo-mls/object/"
	settledPrefix = "kolo-mls/settled/"
)

// shared reports whether a key of settle.Pins goes to the control log, for every member to read.
func shared(key string) bool {
	return strings.HasPrefix(key, objectPrefix) || strings.HasPrefix(key, settledPrefix)
}

// roomPins is settle.Pins over the control logs: a Commit and a SettledCommit go to this admin's control log and are read
// from the control log of any admin; trees and Welcome marks stay on this device.
type roomPins struct{ r *Room }

func (p roomPins) Put(_ context.Context, key string, value []byte) error {
	if !shared(key) {
		return p.r.kv.put(value, recObject, []byte(key))
	}
	for _, ref := range p.r.idx.all(key) {
		if v, err := p.r.readObject(ref, key); ref.l.own && err == nil && bytes.Equal(v, value) {
			return nil
		}
	}
	return p.r.appendCtl(&ctlRecord{Kind: ctlPut, Key: key, Value: value})
}

func (p roomPins) Get(_ context.Context, key string, max int) ([]byte, error) {
	var v []byte
	if shared(key) {
		refs := p.r.idx.all(key)
		if len(refs) == 0 {
			return nil, fmt.Errorf("%w: %s", settle.ErrMissing, key)
		}
		// A Commit is named by its hash, so the first value that matches it is the one; the other keys have one writer's
		// value each, and the caller that can check them goes through the candidates itself.
		for _, ref := range refs {
			val, err := p.r.readObject(ref, key)
			if err == nil && matchesKey(key, val) {
				v = val
				break
			}
		}
		if v == nil {
			return nil, fmt.Errorf("%w: %s", settle.ErrMissing, key)
		}
	} else {
		var err error
		if v, err = p.r.kv.get(recObject, []byte(key)); err != nil {
			return nil, err
		}
		if v == nil {
			return nil, fmt.Errorf("%w: %s", settle.ErrMissing, key)
		}
	}
	if max > 0 && len(v) > max {
		return nil, settle.ErrPinTooLarge
	}
	return v, nil
}

func (r *Room) readObject(ref objRef, key string) ([]byte, error) {
	b, err := ref.l.view.Get(ref.i)
	if err != nil {
		return nil, err
	}
	rec, err := decodeCtl(b, r.n.limits.MaxMessageSize)
	if err != nil || rec.Kind != ctlPut || rec.Key != key {
		return nil, fmt.Errorf("%w: %s", settle.ErrMissing, key)
	}
	return rec.Value, nil
}

// matchesKey reports whether a value is the one its key names: a Commit has the hash in its key.
func matchesKey(key string, v []byte) bool {
	if !strings.HasPrefix(key, objectPrefix) {
		return true
	}
	h := sha256.Sum256(v)
	return key == settle.ObjectKey(h)
}

func (p roomPins) List(_ context.Context, prefix, after string, max int) ([]string, error) {
	if shared(prefix) {
		return p.r.idx.list(prefix, after, max), nil
	}
	if strings.HasPrefix(prefix, "kolo-mls/archive/") {
		return nil, nil
	}
	var out []string
	err := p.r.kv.each(recObject, func(tail, _ []byte) error {
		if k := string(tail); strings.HasPrefix(k, prefix) && k > after && (max <= 0 || len(out) < max) {
			out = append(out, k)
		}
		return nil
	})
	return out, err
}

func (p roomPins) Delete(_ context.Context, key string) error {
	if shared(key) {
		return nil
	}
	return p.r.kv.del(recObject, []byte(key))
}

// openLog creates or opens this device's own log, with the room's derived entry keys.
func (r *Room) openLog(ctl bool, epoch uint64, id *kololog.LogID) (*roomLog, error) {
	var (
		log *kolostore.Log
		err error
	)
	if id == nil {
		log, err = r.n.cfg.Store.Create(r.n.cfg.Device, kololog.WithDerivedKeys(r.logSec, 0))
	} else {
		log, err = r.n.cfg.Store.Open(r.n.cfg.Device, *id, kololog.WithDerivedKeys(r.logSec, 0))
	}
	if err != nil {
		return nil, err
	}
	l := &roomLog{owner: [32]byte(r.self), id: log.ID(), own: true, ctl: ctl, epoch: epoch, header: log.Header().Encode(), src: log, view: log, log: log}
	return l, nil
}

// replicate opens the replica of an admin's log from its signed header and has it followed.
func (r *Room) replicate(owner [32]byte, header []byte, ctl bool, epoch uint64) (*roomLog, error) {
	hd, err := kololog.DecodeLogHeader(header)
	if err != nil || !bytes.Equal(hd.LogKey, owner[:]) {
		return nil, fmt.Errorf("%w: log header", ErrMalformed)
	}
	r.logMu.Lock()
	defer r.logMu.Unlock()
	if l := r.logs[keyOf(owner, hd.LogID)]; l != nil {
		return l, nil
	}
	if len(r.logs) >= r.n.limits.MaxLogs {
		return nil, fmt.Errorf("%w: %d logs", ErrTooManyLogs, len(r.logs))
	}
	rep, err := r.n.cfg.Store.OpenReplica(owner[:], hd.LogID, header)
	if err != nil {
		return nil, err
	}
	d, err := kololog.NewKeyDeriver(r.logSec, owner[:], rep.Header())
	if err != nil {
		rep.Close()
		return nil, err
	}
	l := &roomLog{owner: owner, id: hd.LogID, ctl: ctl, epoch: epoch, header: header, src: rep, view: rep, rep: rep, deriver: d}
	if err := r.trackLocked(l); err != nil {
		l.close()
		return nil, err
	}
	return l, nil
}

// trackLocked registers l: it is offered to the room's peers, a replica is followed, and the record is stored.
func (r *Room) trackLocked(l *roomLog) error {
	if err := r.n.sync.Offer(l.src); err != nil {
		return err
	}
	r.n.logRoom.Store(l.key(), r)
	if l.rep != nil {
		if err := r.fol.Add(l.rep); err != nil {
			r.n.sync.Withdraw(l.owner[:], l.id)
			r.n.logRoom.Delete(l.key())
			return err
		}
	}
	r.logs[l.key()] = l
	return r.kv.put(l.record().encode(), recLog, l.owner[:], l.id[:])
}

func (r *Room) persistLogLocked(l *roomLog) error {
	return r.kv.put(l.record().encode(), recLog, l.owner[:], l.id[:])
}

// forgetLocked drops a log the room no longer keeps: it is withdrawn and closed, and deleted from the store when asked.
func (r *Room) forgetLocked(l *roomLog, deleteData bool) {
	r.n.sync.Withdraw(l.owner[:], l.id)
	r.n.logRoom.Delete(l.key())
	if l.rep != nil {
		r.fol.Remove(l.owner[:], l.id)
	}
	l.close()
	if deleteData {
		r.n.cfg.Store.Delete(l.id)
	}
	r.kv.del(recLog, l.owner[:], l.id[:])
	delete(r.logs, l.key())
}

// ensureCtl gives this device its control log, making it when the room has none yet.
func (r *Room) ensureCtl() error {
	r.logMu.Lock()
	defer r.logMu.Unlock()
	if r.ctl != nil {
		return nil
	}
	var id *kololog.LogID
	if r.meta.HasCtl {
		id = &r.meta.Ctl
	}
	l, err := r.openLog(true, 0, id)
	if err != nil {
		return err
	}
	if err := r.trackLocked(l); err != nil {
		l.close()
		return err
	}
	r.ctl = l
	r.meta.Ctl, r.meta.HasCtl = l.id, true
	if err := r.kv.put(r.meta.encode(), recMeta); err != nil {
		return err
	}
	r.wg.Go(r.pinLoop)
	return nil
}

// appendCtl writes a record to this admin's control log and applies it to the index.
func (r *Room) appendCtl(rec *ctlRecord) error {
	r.logMu.Lock()
	l := r.ctl
	r.logMu.Unlock()
	if l == nil {
		return ErrNotAdmin
	}
	r.ctlMu.Lock()
	defer r.ctlMu.Unlock()
	i, err := l.log.Append(rec.encode())
	if err != nil {
		return err
	}
	r.logMu.Lock()
	l.applied = i + 1
	r.logMu.Unlock()
	if rec.Kind == ctlPut && shared(rec.Key) {
		r.idx.put(rec.Key, objRef{l, i})
	}
	r.pinSoon(l)
	return nil
}

// archive stores a message in this admin's current segment; a device that is no admin keeps none.
func (r *Room) archive(st *stored) error {
	if !r.isAdmin() {
		return nil
	}
	r.segMu.Lock()
	defer r.segMu.Unlock()
	epoch := r.view.Load().epoch
	if r.seg != nil && (r.seg.epoch != epoch || r.seg.log.Len() >= uint64(r.n.limits.SegmentEntries)) {
		if err := r.closeSegLocked(); err != nil {
			return err
		}
	}
	if r.seg == nil {
		if err := r.openSegLocked(epoch); err != nil {
			return err
		}
	}
	if _, err := r.seg.log.Append(st.encode()); err != nil {
		return err
	}
	r.pinSoon(r.seg)
	return nil
}

// openSegLocked starts a segment for epoch and writes its header to the control log; r.segMu is held.
func (r *Room) openSegLocked(epoch uint64) error {
	l, err := r.openLog(false, epoch, nil)
	if err != nil {
		return err
	}
	r.logMu.Lock()
	err = r.trackLocked(l)
	r.logMu.Unlock()
	if err != nil {
		l.close()
		return err
	}
	if err := r.appendCtl(&ctlRecord{Kind: ctlOpen, Epoch: epoch, Header: l.header}); err != nil {
		return err
	}
	r.seg = l
	r.logMu.Lock()
	r.meta.Seg, r.meta.HasSeg = l.id, true
	err = r.kv.put(r.meta.encode(), recMeta)
	r.logMu.Unlock()
	return err
}

// closeSegLocked declares the current segment's final size in the control log; r.segMu is held.
func (r *Room) closeSegLocked() error {
	l := r.seg
	if l == nil {
		return nil
	}
	if err := r.appendCtl(&ctlRecord{Kind: ctlClose, ID: l.id, Size: l.log.Len()}); err != nil {
		return err
	}
	r.logMu.Lock()
	l.closed, l.size = true, l.log.Len()
	r.meta.HasSeg = false
	err := errors.Join(r.persistLogLocked(l), r.kv.put(r.meta.encode(), recMeta))
	r.logMu.Unlock()
	r.seg = nil
	r.pinSoon(l)
	return err
}

func (r *Room) epochChanged() {
	r.segMu.Lock()
	defer r.segMu.Unlock()
	if err := r.closeSegLocked(); err != nil && r.ctx.Err() == nil {
		r.emit(Event{Kind: Stored, Err: fmt.Errorf("room: closing the segment: %w", err)})
	}
}

// trimLocked forgets the closed segments past KeepSegments, the oldest first, that are complete, read and pushed. A
// replica is deleted from the store: the pins and the other members hold the log. An admin's own segment stays in the store
// and is only closed, since it may be the only copy.
func (r *Room) trimLocked() {
	var replicas, own []*roomLog
	for _, l := range r.logs {
		switch {
		case l.ctl || r.isPast(l) || !l.complete() || r.dirtyLog(l):
		case l.own:
			own = append(own, l)
		case l.cursor >= l.size:
			replicas = append(replicas, l)
		}
	}
	for _, ls := range [][]*roomLog{replicas, own} {
		if len(ls) <= r.n.limits.KeepSegments {
			continue
		}
		slices.SortFunc(ls, func(a, b *roomLog) int {
			if a.epoch != b.epoch {
				return int(a.epoch) - int(b.epoch)
			}
			return bytes.Compare(a.id[:], b.id[:])
		})
		for _, l := range ls[:len(ls)-r.n.limits.KeepSegments] {
			r.forgetLocked(l, !l.own)
		}
	}
}

// deriveKeys gives a replica the keys of the entries it has not got, derived from the room's secret.
func (l *roomLog) deriveKeys() error {
	if l.rep == nil {
		return nil
	}
	for n := l.rep.Len(); l.keyed < n; {
		end := min(n, l.keyed+kololog.MaxAddKeys)
		keys := make([]kololog.EntryKey, 0, end-l.keyed)
		for i := l.keyed; i < end; i++ {
			k, err := l.deriver.Key(i)
			if err != nil {
				return err
			}
			keys = append(keys, k)
		}
		if err := l.rep.AddKeys(l.keyed, keys); err != nil {
			return err
		}
		l.keyed = end
	}
	return nil
}
