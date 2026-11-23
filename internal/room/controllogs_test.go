package room

import (
	"context"
	"errors"
	"testing"
	"time"

	kololog "github.com/ciranda-dev/kolo-log"
	kolostore "github.com/ciranda-dev/kolo-store"
)

func logCount(r *Room) int {
	r.logMu.Lock()
	defer r.logMu.Unlock()
	return len(r.logs)
}

// A second admin follows the first one's control logs, one for each epoch, for more epochs than MaxLogs: the logs of its
// own do not count against MaxLogs.
func TestAdminFollowsMoreEpochsThanMaxLogs(t *testing.T) {
	gr := newGroup(t, 3, Limits{MaxLogs: 20, KeepSegments: 4, CatchUpWait: 3 * time.Second})
	gr.waitMesh(2)
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	a, b, m := gr.rooms[0], gr.rooms[1], gr.rooms[2]
	if err := a.Promote(ctx, gr.nodes[1].key(), Admin); err != nil {
		t.Fatal(err)
	}
	gr.waitEpoch(a.Status().Epoch)
	for range 30 {
		e := a.Status().Epoch
		if _, err := a.Send(ctx, []byte("x"), MessageID{}); err != nil {
			t.Fatal(err)
		}
		if _, err := a.Add(ctx, ghostKeyPackage(t, gr)); err != nil {
			t.Fatal(err)
		}
		dl := time.Now().Add(20 * time.Second)
		for time.Now().Before(dl) && (b.Status().Epoch <= e || m.Status().Epoch <= e) {
			time.Sleep(50 * time.Millisecond)
		}
		if b.Status().Epoch <= e || m.Status().Epoch <= e {
			t.Fatalf("stuck after epoch %d: epochs a %d b %d m %d; logs a %d b %d m %d; too many logs reported: a %v b %v m %v", e,
				a.Status().Epoch, b.Status().Epoch, m.Status().Epoch, logCount(a), logCount(b), logCount(m),
				hasErr(gr.recs[0], ErrTooManyLogs), hasErr(gr.recs[1], ErrTooManyLogs), hasErr(gr.recs[2], ErrTooManyLogs))
		}
	}
	for i := range gr.recs {
		if hasErr(gr.recs[i], ErrTooManyLogs) {
			t.Errorf("device %d reported too many logs", i)
		}
	}
}

// A joiner reads the history of more epochs than its MaxLogs and goes on following the room: History holds the control
// logs of the epochs it loads, not the whole chain, and lets them go once read.
func TestHistoryOfMoreEpochsThanMaxLogs(t *testing.T) {
	gr := newGroup(t, 2, Limits{KeepSegments: 200, CatchUpWait: 3 * time.Second})
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	sent := gr.advance(t, ctx, 30, "old")
	gr.lim = Limits{MaxLogs: 20, KeepSegments: 4, CatchUpWait: 3 * time.Second}
	gr.join(ctx, gr.newNode())
	j := gr.rooms[2]
	most := 0
	done := make(chan struct{})
	sampled := make(chan struct{})
	go func() {
		defer close(sampled)
		for {
			most = max(most, logCount(j))
			select {
			case <-done:
				return
			case <-time.After(5 * time.Millisecond):
			}
		}
	}()
	var got []Event
	var herr error
	var before MessageID
	for {
		evs, err := j.History(ctx, before, 100)
		if err != nil {
			herr = err
			break
		}
		if len(evs) == 0 {
			break
		}
		got = append(evs, got...)
		before = evs[0].ID
	}
	close(done)
	<-sampled
	if herr != nil {
		t.Fatalf("History: %v (held up to %d logs, MaxLogs 20)", herr, most)
	}
	t.Logf("the joiner held up to %d logs while it read 30 epochs", most)
	bodies := bodiesOf(t, got)
	for body, e := range sent {
		if bodies[body] != e {
			t.Errorf("history lacks %s of epoch %d", body, e)
		}
	}
	if most > 10 {
		t.Errorf("the joiner held up to %d logs while it read History", most)
	}
	gr.advance(t, ctx, 3, "new")
	if hasErr(gr.recs[2], ErrTooManyLogs) {
		t.Fatalf("the joiner reported too many logs (held up to %d)", most)
	}
}

// A ctlPrev record an admin appends anywhere but at the start of a log, or with an epoch past what the log may name, is
// refused, so History does not wait for the log it names.
func TestForgedPrevRecordDoesNotStallHistory(t *testing.T) {
	gr := newGroup(t, 2, Limits{CatchUpWait: 3 * time.Second})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	sent := gr.advance(t, ctx, 4, "old")
	gr.join(ctx, gr.newNode())
	admin, j := gr.rooms[0], gr.rooms[2]
	fake, err := admin.openLog(true, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer fake.close()
	if err := admin.appendCtl(&ctlRecord{Kind: ctlPrev, Epoch: admin.Status().Epoch, Header: fake.header}, &ctlRecord{Kind: ctlPrev, Epoch: 1 << 62, Header: fake.header}); err != nil {
		t.Fatal(err)
	}
	admin.logMu.Lock()
	ctl, n := admin.ctl.key(), admin.ctl.log.Len()
	admin.logMu.Unlock()
	waitFor(t, 30*time.Second, "the joiner reads the forged record", func() bool {
		j.logMu.Lock()
		defer j.logMu.Unlock()
		l := j.logs[ctl]
		return l != nil && l.applied >= n
	})
	start := time.Now()
	got := bodiesOf(t, readHistory(t, ctx, j, 100))
	if d := time.Since(start); d >= 3*time.Second {
		t.Fatalf("History took %v with a forged ctlPrev (CatchUpWait 3s)", d.Round(time.Millisecond))
	}
	for body, e := range sent {
		if got[body] != e {
			t.Errorf("history lacks %s of epoch %d", body, e)
		}
	}
	waitFor(t, 10*time.Second, "the reports of the forged records", func() bool {
		return hasErr(gr.recs[2], ErrMalformed) && hasErr(gr.recs[2], ErrWrongEpoch)
	})
}

// A control log that no peer serves is waited for once, CatchUpWait, and then History goes on without it.
func TestUnservedPrevLogIsWaitedForOnce(t *testing.T) {
	gr := newGroup(t, 2, Limits{CatchUpWait: 2 * time.Second})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	sent := gr.advance(t, ctx, 4, "old")
	gr.join(ctx, gr.newNode())
	admin, j := gr.rooms[0], gr.rooms[2]
	fake, err := admin.openLog(true, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer fake.close()
	j.hmu.Lock()
	j.prevs = append(j.prevs, prevLog{epoch: j.joinEpoch, owner: [32]byte(admin.self), header: fake.header})
	j.hmu.Unlock()
	start := time.Now()
	evs := readHistory(t, ctx, j, 100)
	d := time.Since(start)
	got, reports := map[string]uint64{}, 0
	for _, ev := range evs {
		if ev.Err != nil {
			reports++
			continue
		}
		got[string(ev.Body)] = ev.Epoch
	}
	if d >= 4*time.Second {
		t.Fatalf("History of 4 epochs took %v with one control log no peer serves (CatchUpWait 2s)", d.Round(time.Millisecond))
	}
	for body, e := range sent {
		if got[body] != e {
			t.Errorf("history lacks %s of epoch %d", body, e)
		}
	}
	if reports != 1 {
		t.Errorf("%d reports, want 1 for the control log no peer serves", reports)
	}
}

// replicaHeader returns the header of a new log of the device of nd, kept in a store of its own.
func replicaHeader(t *testing.T, nd *tnode, r *Room) []byte {
	t.Helper()
	l, err := kolostore.New(kolostore.NewMemory()).Create(nd.dev, kololog.WithDerivedKeys(r.logSec, 0))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	return l.Header().Encode()
}

// ctlNext and ctlPrev are applied only from the control log of a device that is an admin, as ctlOpen and ctlDir are.
func TestNextAndPrevNeedAnAdmin(t *testing.T) {
	gr := newGroup(t, 3, Limits{})
	gr.waitMesh(2)
	m, other := gr.rooms[1], gr.nodes[2]
	e := m.Status().Epoch
	from := &roomLog{owner: [32]byte(other.key()), ctl: true, epoch: e, view: emptyView{}}
	next := replicaHeader(t, other, m)
	hd, err := kololog.DecodeLogHeader(next)
	if err != nil {
		t.Fatal(err)
	}
	added := m.applyCtl(from, 3, &ctlRecord{Kind: ctlNext, Epoch: e + 1, Header: next})
	m.logMu.Lock()
	followed := m.logs[keyOf(from.owner, hd.LogID)] != nil
	m.logMu.Unlock()
	if added || followed {
		t.Fatal("a member followed the next log of a device that is no admin")
	}
	from = &roomLog{owner: [32]byte(other.key()), ctl: true, epoch: e, view: emptyView{}}
	m.applyCtl(from, 0, &ctlRecord{Kind: ctlPrev, Epoch: e, Header: replicaHeader(t, other, m)})
	m.hmu.Lock()
	defer m.hmu.Unlock()
	for _, p := range m.prevs {
		if p.owner == from.owner {
			t.Fatal("a member kept the previous log of a device that is no admin")
		}
	}
}

type emptyView struct{}

func (emptyView) Len() uint64                { return 0 }
func (emptyView) Get(uint64) ([]byte, error) { return nil, errors.New("empty") }

// A member keeps KeepSegments closed segments, and the control logs it has left apart from them.
func TestControlLogsDoNotTakeTheRoomOfSegments(t *testing.T) {
	gr := newGroup(t, 2, Limits{KeepSegments: 4})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	gr.advance(t, ctx, 12, "e")
	m := gr.rooms[1]
	count := func() (segs, ctls int) {
		m.logMu.Lock()
		defer m.logMu.Unlock()
		for _, l := range m.logs {
			switch {
			case l.own || l.tree:
			case l.ctl && l.done:
				ctls++
			case !l.ctl && l.closed:
				segs++
			}
		}
		return
	}
	dl := time.Now().Add(20 * time.Second)
	for time.Now().Before(dl) {
		if s, c := count(); s == 4 && c <= 4 {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	s, c := count()
	t.Fatalf("the member keeps %d closed segments and %d control logs it has left, want 4 segments (KeepSegments 4)", s, c)
}

// A member keeps no record of the control logs before the ones it follows when it reads no History.
func TestPrevsDoNotGrowWithEpochs(t *testing.T) {
	gr := newGroup(t, 2, Limits{})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	gr.advance(t, ctx, 8, "e")
	m := gr.rooms[1]
	time.Sleep(time.Second)
	m.hmu.Lock()
	n := len(m.prevs)
	m.hmu.Unlock()
	if n > 1 {
		t.Fatalf("the member keeps %d records of earlier control logs after 8 epochs", n)
	}
}

// A ctlOpen that names an epoch past what its control log may name is refused and reported, and the device does not wait
// for the segment.
func TestFarEpochSegmentIsRefused(t *testing.T) {
	gr := newGroup(t, 2, Limits{CatchUpWait: 2 * time.Second, PinWait: 500 * time.Millisecond})
	gr.waitMesh(1)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	admin, m := gr.rooms[0], gr.rooms[1]
	if _, err := admin.Send(ctx, []byte("first"), MessageID{}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 20*time.Second, "first", func() bool { return gr.recs[1].has("first") })
	far := uint64(1) << 40
	l, err := admin.openLog(false, far, nil)
	if err != nil {
		t.Fatal(err)
	}
	admin.logMu.Lock()
	err = admin.trackLocked(l)
	admin.logMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	bad := &sealed{Epoch: admin.Status().Epoch + 1, CT: []byte{1, 2, 3}}
	if _, err := l.log.Append((&stored{Device: [32]byte(admin.self), Seq: 1, Body: bad.encode()}).encode()); err != nil {
		t.Fatal(err)
	}
	if err := admin.appendCtl(&ctlRecord{Kind: ctlOpen, Epoch: far, Header: l.header}); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Send(ctx, []byte("after"), MessageID{}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 20*time.Second, "after", func() bool { return gr.recs[1].has("after") })
	time.Sleep(time.Second)
	m.logMu.Lock()
	followed := m.logs[l.key()] != nil
	m.logMu.Unlock()
	if followed || !hasErr(gr.recs[1], ErrWrongEpoch) || m.Status().CatchingUp {
		t.Fatalf("a segment of epoch 2^40: followed %v, reported %v, catching up %v", followed, hasErr(gr.recs[1], ErrWrongEpoch), m.Status().CatchingUp)
	}
}
