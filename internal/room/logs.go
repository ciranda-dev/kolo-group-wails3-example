package room

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strconv"
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
	tree    bool   // holds the sealed tree of epoch
	epoch   uint64 // of a segment, a tree or a control log
	header  []byte
	src     kolosync.Source
	view    logView
	log     *kolostore.Log
	rep     *kolostore.Replica
	deriver *kololog.KeyDeriver

	closed      bool   // a segment whose writer declared its final size
	size        uint64 // that size
	cursor      uint64 // entries of a segment already read
	keyed       uint64 // entries of a replica that have their keys
	applied     uint64 // entries of a control log already applied
	since       time.Time
	keyReported bool   // a failure to derive or read its entries was reported
	gapReported bool   // a closed segment that lacks entries was reported
	opened      bool   // a control log that has opened a segment
	openedTo    uint64 // the newest epoch it opened one for
	done        bool   // a control log that its admin went on from in another log
	past        bool   // a control log that History asked for
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
	return &logRec{Ctl: l.ctl, Own: l.own, Tree: l.tree, Epoch: l.epoch, Closed: l.closed, Size: l.size, Cursor: l.cursor, Header: l.header, Done: l.done}
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

// drop forgets the entries of the log l.
func (x *objIndex) drop(l *roomLog) {
	x.mu.Lock()
	defer x.mu.Unlock()
	for k, rs := range x.refs {
		if rs = slices.DeleteFunc(rs, func(o objRef) bool { return o.l == l }); len(rs) > 0 {
			x.refs[k] = rs
			continue
		}
		delete(x.refs, k)
		if i, ok := slices.BinarySearch(x.keys, k); ok {
			x.keys = slices.Delete(x.keys, i, i+1)
		}
	}
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

func isObject(key string) bool  { return strings.HasPrefix(key, objectPrefix) }
func isSettled(key string) bool { return strings.HasPrefix(key, settledPrefix) }

// shared reports whether a key of settle.Pins goes to the control log, for every member to read.
func shared(key string) bool { return isObject(key) || isSettled(key) }

// objectHash returns the hash an object key names.
func objectHash(key string) ([32]byte, bool) {
	b, err := hex.DecodeString(strings.TrimPrefix(key, objectPrefix))
	return [32]byte(b), err == nil && len(b) == 32
}

// roomPins is settle.Pins over the control logs: a Commit and a SettledCommit go to this admin's control log and are read
// from the control log of any admin; trees and Welcome marks stay on this device.
type roomPins struct{ r *Room }

func (p roomPins) Put(_ context.Context, key string, value []byte) error {
	switch {
	case isObject(key):
		return p.r.putObject(key, value)
	case !isSettled(key):
		return p.r.kv.put(value, recObject, []byte(key))
	}
	for _, ref := range p.r.idx.all(key) {
		if v, err := p.r.readObject(ref, key); ref.l.own && err == nil && bytes.Equal(v, value) {
			return nil
		}
	}
	return p.r.appendCtl(&ctlRecord{Kind: ctlPut, Key: key, Value: value})
}

// putObject writes a Commit to this admin's control log in pieces, under the hash its key names.
func (r *Room) putObject(key string, value []byte) error {
	h, ok := objectHash(key)
	if !ok || len(value) == 0 || sha256.Sum256(value) != h {
		return errInvalid("an object that does not match its key")
	}
	for _, ref := range r.idx.all(key) {
		if v, err := readPieces(ref.l.view, ref.i, h, 0); ref.l.own && err == nil && bytes.Equal(v, value) {
			return nil
		}
	}
	return r.appendCtl(pieces(h, value)...)
}

// readPieces reads the object with hash h whose first piece is entry first of v, at most max bytes (0 is no bound). It
// fails until every piece has arrived, and when what the pieces hold does not hash to h.
func readPieces(v logView, first uint64, h [32]byte, max int) ([]byte, error) {
	var out []byte
	for i := uint64(0); ; i++ {
		b, err := v.Get(first + i)
		if err != nil {
			return nil, fmt.Errorf("%w: piece %d: %w", settle.ErrMissing, i, err)
		}
		rec, err := decodeCtl(b, 0)
		if err != nil || rec.Kind != ctlChunk || rec.ID != h || rec.Index != i {
			return nil, fmt.Errorf("%w: piece %d", settle.ErrMissing, i)
		}
		if out = append(out, rec.Value...); max > 0 && len(out) > max {
			return nil, settle.ErrPinTooLarge
		}
		if i+1 == rec.Count {
			break
		}
	}
	if sha256.Sum256(out) != h {
		return nil, fmt.Errorf("%w: pieces do not match their hash", settle.ErrMissing)
	}
	return out, nil
}

func (p roomPins) Get(_ context.Context, key string, max int) ([]byte, error) {
	var v []byte
	switch {
	case isObject(key):
		return p.r.getObject(key, max)
	case isSettled(key):
		refs := p.r.idx.all(key)
		if len(refs) == 0 {
			return nil, fmt.Errorf("%w: %s", settle.ErrMissing, key)
		}
		// Each admin has one value for the key, and the caller that can check them goes through the candidates itself.
		for _, ref := range refs {
			if v, _ = p.r.readObject(ref, key); v != nil {
				break
			}
		}
		if v == nil {
			return nil, fmt.Errorf("%w: %s", settle.ErrMissing, key)
		}
	default:
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

// getObject returns the Commit a key names from the first control log whose pieces are all there and hash right: a log that
// is cut short or serves other bytes is passed over for another admin's.
func (r *Room) getObject(key string, max int) ([]byte, error) {
	h, ok := objectHash(key)
	if !ok {
		return nil, fmt.Errorf("%w: %s", settle.ErrMissing, key)
	}
	tooLarge := false
	for _, ref := range r.idx.all(key) {
		v, err := readPieces(ref.l.view, ref.i, h, max)
		if err == nil {
			return v, nil
		}
		tooLarge = tooLarge || errors.Is(err, settle.ErrPinTooLarge)
	}
	if tooLarge {
		return nil, settle.ErrPinTooLarge
	}
	return nil, fmt.Errorf("%w: %s", settle.ErrMissing, key)
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
	if epoch, ok := treeEpoch(key, p.r.gid); ok {
		p.r.dropTreeLog(epoch)
	}
	return p.r.kv.del(recObject, []byte(key))
}

// treeEpoch returns the epoch a tree key of this room names.
func treeEpoch(key string, gid GroupID) (uint64, bool) {
	prefix := settle.TreeKey(gid[:], 0)
	rest, ok := strings.CutPrefix(key, prefix[:len(prefix)-1])
	if !ok {
		return 0, false
	}
	e, err := strconv.ParseUint(rest, 10, 64)
	return e, err == nil
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
	return r.replicateKind(owner, header, ctl, false, epoch)
}

func (r *Room) replicateKind(owner [32]byte, header []byte, ctl, tree bool, epoch uint64) (*roomLog, error) {
	return r.replicateAs(&roomLog{owner: owner, header: header, ctl: ctl, tree: tree, epoch: epoch})
}

// replicateAs opens the replica of the log that l describes. Only replicas count against MaxLogs.
func (r *Room) replicateAs(l *roomLog) (*roomLog, error) {
	hd, err := kololog.DecodeLogHeader(l.header)
	if err != nil || !bytes.Equal(hd.LogKey, l.owner[:]) {
		return nil, fmt.Errorf("%w: log header", ErrMalformed)
	}
	r.logMu.Lock()
	defer r.logMu.Unlock()
	if have := r.logs[keyOf(l.owner, hd.LogID)]; have != nil {
		return have, nil
	}
	n := 0
	for _, x := range r.logs {
		if !x.own {
			n++
		}
	}
	if n >= r.n.limits.MaxLogs {
		return nil, fmt.Errorf("%w: %d logs", ErrTooManyLogs, n)
	}
	rep, err := r.n.cfg.Store.OpenReplica(l.owner[:], hd.LogID, l.header)
	if err != nil {
		return nil, err
	}
	d, err := kololog.NewKeyDeriver(r.logSec, l.owner[:], rep.Header())
	if err != nil {
		rep.Close()
		return nil, err
	}
	l.id, l.src, l.view, l.rep, l.deriver = hd.LogID, rep, rep, rep, d
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
	if l.ctl {
		r.idx.drop(l)
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

// appendCtl writes records one after the other to this admin's control log, which no other write comes between, and applies
// them to the index.
func (r *Room) appendCtl(recs ...*ctlRecord) error {
	r.ctlMu.Lock()
	defer r.ctlMu.Unlock()
	r.logMu.Lock()
	l := r.ctl
	r.logMu.Unlock()
	if l == nil {
		return ErrNotAdmin
	}
	return r.appendLocked(l, recs...)
}

// appendLocked writes records to the control log l; r.ctlMu is held.
func (r *Room) appendLocked(l *roomLog, recs ...*ctlRecord) error {
	at := make([]uint64, len(recs))
	for k, rec := range recs {
		i, err := l.log.Append(rec.encode())
		if err != nil {
			return err
		}
		at[k] = i
	}
	r.logMu.Lock()
	l.applied = at[len(at)-1] + 1
	r.logMu.Unlock()
	for k, rec := range recs {
		r.indexRecord(l, at[k], rec)
	}
	r.pinSoon(l)
	return nil
}

// rotateCtl starts this admin's control log of epoch, so a joiner pulls only the logs of the epochs it reads: the old log
// ends with a record that names the new one and the new one starts with a record that names the old one, which History
// follows back.
func (r *Room) rotateCtl(epoch uint64) error {
	r.ctlMu.Lock()
	defer r.ctlMu.Unlock()
	r.logMu.Lock()
	old := r.ctl
	r.logMu.Unlock()
	if old == nil || old.epoch >= epoch {
		return nil
	}
	l, err := r.openLog(true, epoch, nil)
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
	if err := r.appendLocked(l, &ctlRecord{Kind: ctlPrev, Epoch: epoch, Header: old.header}); err != nil {
		return err
	}
	if err := r.appendLocked(old, &ctlRecord{Kind: ctlNext, Epoch: epoch, Header: l.header}); err != nil {
		return err
	}
	r.logMu.Lock()
	defer r.logMu.Unlock()
	old.done, r.ctl = true, l
	r.meta.Ctl, r.meta.HasCtl = l.id, true
	return errors.Join(r.persistLogLocked(old), r.kv.put(r.meta.encode(), recMeta))
}

// indexRecord makes entry i of the control log l findable by the key of the object it starts, and reports whether it did.
func (r *Room) indexRecord(l *roomLog, i uint64, rec *ctlRecord) bool {
	switch {
	case rec.Kind == ctlPut && isSettled(rec.Key):
		r.idx.put(rec.Key, objRef{l, i})
	case rec.Kind == ctlChunk && rec.Index == 0:
		r.idx.put(settle.ObjectKey(rec.ID), objRef{l, i})
	default:
		return false
	}
	return true
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

// trimLocked forgets, past KeepSegments and the oldest first, the closed segments that are complete, read
// and pushed, and the replicas of control logs that are read and left. A replica is deleted from the store: the pins and
// the other members hold the log. An admin's own segment stays in the store and is only closed, since it may be the only
// copy. A control log that History asked for goes as soon as it is read: what History needs of it is kept apart.
func (r *Room) trimLocked() {
	r.trimTreesLocked()
	var replicas, own, read []*roomLog
	epoch := r.view.Load().epoch
	for _, l := range r.logs {
		switch {
		// The admin keeps its own control logs: a member that is behind pulls the Commits from them.
		case l.ctl && (l.own || !l.done || l.applied < l.view.Len()) || r.dirtyLog(l):
		case l.ctl && l.past:
			read = append(read, l)
		case l.ctl && l.epoch < epoch:
			replicas = append(replicas, l)
		case l.ctl || r.isPast(l) || !l.complete():
		case l.own:
			own = append(own, l)
		case l.cursor >= l.size:
			replicas = append(replicas, l)
		}
	}
	if len(read) > 0 {
		r.hmu.Lock()
		r.prevs = slices.DeleteFunc(r.prevs, func(p prevLog) bool {
			return slices.ContainsFunc(read, func(l *roomLog) bool { return l.key() == p.key() })
		})
		r.hmu.Unlock()
		for _, l := range read {
			r.forgetLocked(l, true)
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

// writeTree puts the sealed tree of epoch into a log of this admin, in pieces under its hash, and names the log in the
// control log, so the members that hold it can serve it to a joiner. It returns the log's header and entry count.
func (r *Room) writeTree(epoch uint64, sealed []byte) (header []byte, entries uint64, err error) {
	h := sha256.Sum256(sealed)
	ps := pieces(h, sealed)
	r.logMu.Lock()
	for _, l := range r.logs {
		if l.tree && l.own && l.epoch == epoch && l.log.Len() == uint64(len(ps)) {
			r.logMu.Unlock()
			return l.header, l.log.Len(), nil
		}
	}
	l, err := r.openLog(false, epoch, nil)
	if err == nil {
		l.tree = true
		if err = r.trackLocked(l); err != nil {
			l.close()
		}
	}
	r.logMu.Unlock()
	if err != nil {
		return nil, 0, err
	}
	for _, p := range ps {
		if _, err := l.log.Append(p.encode()); err != nil {
			return nil, 0, err
		}
	}
	if err := r.appendCtl(&ctlRecord{Kind: ctlTree, Epoch: epoch, Header: l.header}); err != nil {
		return nil, 0, err
	}
	r.pinSoon(l)
	return l.header, l.log.Len(), nil
}

// dropTreeLog forgets this admin's log of the tree of epoch: no joiner needs it any more.
func (r *Room) dropTreeLog(epoch uint64) {
	r.logMu.Lock()
	defer r.logMu.Unlock()
	for _, l := range r.logs {
		if l.tree && l.own && l.epoch == epoch && !r.dirtyLog(l) {
			r.forgetLocked(l, true)
		}
	}
}

// servesTree reports whether this device keeps the tree logs of joiners: an admin does, a member only by Config.ServeTree.
func (r *Room) servesTree() bool {
	return !r.reader && (r.n.cfg.ServeTree || r.view.Load().roles.Admin(r.self))
}

// followTree replicates the log of the sealed tree that an admin named, if this device serves trees and the room is not
// past its epoch: only a joiner of that epoch reads it.
func (r *Room) followTree(l *roomLog, rec *ctlRecord) {
	if rec.Epoch < r.view.Load().epoch || !r.servesTree() {
		return
	}
	hd, err := kololog.DecodeLogHeader(rec.Header)
	if err != nil || !bytes.Equal(hd.LogKey, l.owner[:]) {
		return
	}
	if _, err := r.replicateKind(l.owner, rec.Header, false, true, rec.Epoch); err != nil && r.ctx.Err() == nil {
		r.emit(Event{Kind: CatchingUp, Err: fmt.Errorf("%w: %w", ErrCatchUp, err)})
	}
}

// trimTreesLocked forgets the replicas of trees that the room is past.
func (r *Room) trimTreesLocked() {
	epoch := r.view.Load().epoch
	for _, l := range r.logs {
		if l.tree && !l.own && l.epoch < epoch {
			r.forgetLocked(l, true)
		}
	}
}
