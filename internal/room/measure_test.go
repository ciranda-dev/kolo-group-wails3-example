package room

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	kololog "github.com/ciranda-dev/kolo-log"
	kolosecure "github.com/ciranda-dev/kolo-secure"
	kolostore "github.com/ciranda-dev/kolo-store"
	kolosync "github.com/ciranda-dev/kolo-sync"
)

func heapInUse() uint64 {
	runtime.GC()
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return m.HeapAlloc
}

// TestMeasureSegmentSizes measures what a segment of n entries costs on disk and in memory, to set SegmentEntries and
// KeepSegments: appending, reopening the writer, pulling into a replica and deriving its keys, and reading it all.
// It runs with KOLO_GROUP_MEASURE=1.
func TestMeasureSegmentSizes(t *testing.T) {
	if os.Getenv("KOLO_GROUP_MEASURE") == "" {
		t.Skip("measurement: set KOLO_GROUP_MEASURE=1")
	}
	ctx := context.Background()
	var secret kololog.EntrySecret
	secret[0] = 1
	opt := kololog.WithDerivedKeys(&secret, 0)
	// A sealed message of a short text: epoch, nonce, signature and padded ciphertext, with the device and sequence number.
	st := &stored{Device: [32]byte{1}, Seq: 1 << 50, Body: (&sealed{Epoch: 9, CT: make([]byte, 160)}).encode()}
	entry := st.encode()
	for _, n := range []int{1000, 10000, 100000} {
		dir := filepath.Join(t.TempDir(), "w")
		id := ident(t)
		b, err := kolostore.OpenPebble(dir)
		if err != nil {
			t.Fatal(err)
		}
		s := kolostore.New(b)
		l, err := s.Create(id, opt)
		if err != nil {
			t.Fatal(err)
		}
		start := time.Now()
		for range n {
			if _, err := l.Append(entry); err != nil {
				t.Fatal(err)
			}
		}
		appendT := time.Since(start)
		logID := l.ID()
		l.Close()
		s.Close()
		diskBytes := dirSize(dir)
		b, err = kolostore.OpenPebble(dir)
		if err != nil {
			t.Fatal(err)
		}
		s = kolostore.New(b)
		before := heapInUse()
		start = time.Now()
		l, err = s.Open(id, logID, opt)
		if err != nil {
			t.Fatal(err)
		}
		reopenT := time.Since(start)
		held := int64(heapInUse()) - int64(before)
		// A replica of it, pulled over loopback without keys, then keyed and read.
		ln, err := kolosecure.Listen(ctx, "127.0.0.1:0", id, nil)
		if err != nil {
			t.Fatal(err)
		}
		srv := kolosync.NewServer(func(_, _ ed25519.PublicKey, _ kololog.LogID) kolosync.Access { return kolosync.Ciphertext })
		srv.Offer(l)
		go func() {
			if c, err := ln.Accept(ctx); err == nil {
				srv.Serve(ctx, c)
			}
		}()
		rid := ident(t)
		c, err := kolosecure.Dial(ctx, ln.Addr().String(), rid, id.PublicKey(), nil)
		if err != nil {
			t.Fatal(err)
		}
		rs := kolostore.New(kolostore.NewMemory())
		rep, err := rs.OpenReplica(id.PublicKey(), logID, l.Header().Encode())
		if err != nil {
			t.Fatal(err)
		}
		start = time.Now()
		if err := kolosync.PullCiphertext(ctx, c, rep); err != nil {
			t.Fatal(err)
		}
		pullT := time.Since(start)
		d, err := kololog.NewKeyDeriver(&secret, id.PublicKey(), rep.Header())
		if err != nil {
			t.Fatal(err)
		}
		lr := &roomLog{rep: rep, view: rep, deriver: d}
		start = time.Now()
		if err := lr.deriveKeys(); err != nil {
			t.Fatal(err)
		}
		keyT := time.Since(start)
		start = time.Now()
		for i := range uint64(n) {
			if _, err := rep.Get(i); err != nil {
				t.Fatal(err)
			}
		}
		readT := time.Since(start)
		t.Logf("n=%d: append %v/entry; disk %d B/entry; reopen %v, heap %d B/entry; pull %v; keys %v; read %v", n,
			(appendT / time.Duration(n)).Round(time.Microsecond), diskBytes/int64(n), reopenT.Round(time.Millisecond), held/int64(n),
			pullT.Round(time.Millisecond), keyT.Round(time.Millisecond), readT.Round(time.Millisecond))
		c.Close()
		ln.Close()
		l.Close()
		s.Close()
		runtime.KeepAlive(fmt.Sprint())
	}
}

func dirSize(dir string) int64 {
	var n int64
	filepath.WalkDir(dir, func(_ string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			if info, err := d.Info(); err == nil {
				n += info.Size()
			}
		}
		return nil
	})
	return n
}

// TestMeasureOpenLogs measures the memory of a log that is open and holds one entry, to set MaxLogs.
func TestMeasureOpenLogs(t *testing.T) {
	if os.Getenv("KOLO_GROUP_MEASURE") == "" {
		t.Skip("measurement: set KOLO_GROUP_MEASURE=1")
	}
	s := kolostore.New(kolostore.NewMemory())
	id := ident(t)
	var secret kololog.EntrySecret
	before := heapInUse()
	const n = 2000
	for range n {
		l, err := s.Create(id, kololog.WithDerivedKeys(&secret, 0))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := l.Append([]byte("x")); err != nil {
			t.Fatal(err)
		}
	}
	t.Logf("%d open logs of one entry: %d B each", n, (int64(heapInUse())-int64(before))/n)
	s.Close()
}

// TestMeasureLinks measures what the history links cost: the bytes of a link on disk, and the time and memory of walking back
// n epochs, at 3 members. It runs with KOLO_GROUP_MEASURE=1.
func TestMeasureLinks(t *testing.T) {
	if os.Getenv("KOLO_GROUP_MEASURE") == "" {
		t.Skip("measurement: set KOLO_GROUP_MEASURE=1")
	}
	ctx := context.Background()
	var secret kololog.EntrySecret
	secret[0] = 1
	rec := (&ctlRecord{Kind: ctlLink, Epoch: 1 << 20, Value: make([]byte, linkSize)}).encode()
	for _, n := range []int{1000, 10000} {
		dir := filepath.Join(t.TempDir(), "w")
		b, err := kolostore.OpenPebble(dir)
		if err != nil {
			t.Fatal(err)
		}
		s := kolostore.New(b)
		l, err := s.Create(ident(t), kololog.WithDerivedKeys(&secret, 0))
		if err != nil {
			t.Fatal(err)
		}
		for range n {
			if _, err := l.Append(rec); err != nil {
				t.Fatal(err)
			}
		}
		l.Close()
		s.Close()
		t.Logf("%d links: a record is %d B, %d B on disk for each", n, len(rec), dirSize(dir)/int64(n))
	}
	gr := newGroupWith(t, 3, Limits{CatchUpWait: time.Second}, func(o *tnodeOpts) { o.dir = t.TempDir() })
	r := gr.rooms[0]
	for _, n := range []uint64{1000, 10000, 100000} {
		keys := make([][keySize]byte, n+1)
		for i := range keys {
			rand.Read(keys[i][:])
		}
		links := map[uint64][]linkRec{}
		before := heapInUse()
		for e := uint64(1); e <= n; e++ {
			l, err := sealLink(r.gid[:], e, &keys[e], &keys[e-1])
			if err != nil {
				t.Fatal(err)
			}
			links[e] = []linkRec{{owner: [32]byte{1}, link: [linkSize]byte(l)}}
		}
		held := int64(heapInUse()) - int64(before)
		r.hmu.Lock()
		r.links = links
		r.hmu.Unlock()
		r.kmu.Lock()
		clear(r.keys)
		r.keys[n], r.lo = keys[n], n
		r.kmu.Unlock()
		start := time.Now()
		if err := r.deriveBack(ctx, 0); err != nil {
			t.Fatal(err)
		}
		d := time.Since(start)
		if r.keys[0] != keys[0] {
			t.Fatal("the walk did not reach the first key")
		}
		t.Logf("walk of %d links on Pebble: %v (%v each), %d B of memory for the links held, %d B each", n, d.Round(time.Millisecond), (d / time.Duration(n)).Round(time.Microsecond), held, held/int64(n))
	}
}
