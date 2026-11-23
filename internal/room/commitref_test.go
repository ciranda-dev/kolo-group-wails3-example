package room

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"testing"
	"time"

	"github.com/ciranda-dev/kolo-mls/settle"
)

func randomBytes(t testing.TB, n int) []byte {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return b
}

// twoAdmins is a group of three whose second device is an admin too: two control logs hold what the members pull.
func twoAdmins(t *testing.T) *group {
	gr := newGroup(t, 3, Limits{})
	gr.waitMesh(2)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if err := gr.rooms[0].Promote(ctx, gr.nodes[1].key(), Admin); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 30*time.Second, "the second admin", func() bool { return gr.rooms[1].isAdmin() && gr.rooms[1].Status().Epoch == gr.rooms[0].Status().Epoch })
	return gr
}

// getsObject reports the object a member's pins hold under key, nil while they hold none.
func getsObject(r *Room, key string) []byte {
	b, err := r.pins.Get(context.Background(), key, 0)
	if err != nil {
		return nil
	}
	return b
}

func TestPiecesCarryAnObjectOfAnySize(t *testing.T) {
	for _, n := range []int{1, maxChunk - 1, maxChunk, maxChunk + 1, 5*maxChunk + 7} {
		b := randomBytes(t, n)
		h := sha256.Sum256(b)
		ps := pieces(h, b)
		if want := (n + maxChunk - 1) / maxChunk; len(ps) != want {
			t.Fatalf("%d bytes in %d pieces, want %d", n, len(ps), want)
		}
		v := memView{}
		for _, p := range ps {
			back, err := decodeCtl(p.encode(), 0)
			if err != nil {
				t.Fatalf("%d bytes: %v", n, err)
			}
			v = append(v, back.encode())
		}
		got, err := readPieces(v, 0, h, 0)
		if err != nil || !bytes.Equal(got, b) {
			t.Fatalf("%d bytes: read back %d bytes, %v", n, len(got), err)
		}
		if n > 1 {
			if _, err := readPieces(v, 0, h, n-1); !errors.Is(err, settle.ErrPinTooLarge) {
				t.Fatalf("%d bytes under a bound of %d: %v", n, n-1, err)
			}
		}
	}
}

type memView [][]byte

func (m memView) Len() uint64 { return uint64(len(m)) }
func (m memView) Get(i uint64) ([]byte, error) {
	if i >= uint64(len(m)) {
		return nil, errors.New("no entry")
	}
	return m[i], nil
}

// An admin that serves pieces that do not hash to the key it names cannot give a member a wrong Commit: the member waits
// for a log whose pieces do.
func TestPiecesThatDoNotHashToTheirKeyAreRefused(t *testing.T) {
	gr := twoAdmins(t)
	good := randomBytes(t, 3000)
	h := sha256.Sum256(good)
	key := settle.ObjectKey(h)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if err := gr.rooms[1].appendCtl(pieces(h, randomBytes(t, 3000))...); err != nil {
		t.Fatal(err)
	}
	member := gr.rooms[2]
	waitFor(t, 30*time.Second, "the hostile pieces at the member", func() bool { return len(member.idx.all(key)) == 1 })
	if _, err := member.pins.Get(ctx, key, 0); !errors.Is(err, settle.ErrMissing) {
		t.Fatalf("pieces of another hash: %v", err)
	}
	if err := gr.rooms[0].pins.Put(ctx, key, good); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 30*time.Second, "the good Commit", func() bool { return bytes.Equal(getsObject(member, key), good) })
}

// A member that finds a log cut short reads the Commit from the control log of another admin.
func TestTruncatedPiecesAreReadFromAnotherAdmin(t *testing.T) {
	gr := twoAdmins(t)
	good := randomBytes(t, 2*maxChunk+5)
	h := sha256.Sum256(good)
	key := settle.ObjectKey(h)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if err := gr.rooms[1].appendCtl(pieces(h, good)[:2]...); err != nil {
		t.Fatal(err)
	}
	member := gr.rooms[2]
	waitFor(t, 30*time.Second, "the cut log at the member", func() bool { return len(member.idx.all(key)) == 1 })
	if _, err := member.pins.Get(ctx, key, 0); !errors.Is(err, settle.ErrMissing) {
		t.Fatalf("a Commit cut after 2 of 3 pieces: %v", err)
	}
	if err := gr.rooms[0].pins.Put(ctx, key, good); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 60*time.Second, "the whole Commit", func() bool { return bytes.Equal(getsObject(member, key), good) })
	if len(member.idx.all(key)) != 2 {
		t.Fatalf("the member indexes %d control logs for the Commit", len(member.idx.all(key)))
	}
}

// A Commit travels as pieces of an admin's control log and each member holds one copy of it, whatever the size of the group:
// the announcement on the topic carries the SettledCommit only.
func TestCommitIsPulledFromTheControlLog(t *testing.T) {
	gr := newGroup(t, 3, Limits{})
	gr.waitMesh(2)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	sent := func() (n uint64) {
		for _, nd := range gr.nodes {
			n += nd.n.sync.Stats().BytesSent
		}
		return n
	}
	before := sent()
	gr.join(ctx, gr.newNode())
	waitFor(t, 30*time.Second, "the Commit at both members", func() bool {
		return len(gr.rooms[1].idx.list(objectPrefix, "", 0)) > 0 && len(gr.rooms[2].idx.list(objectPrefix, "", 0)) > 0
	})
	for _, i := range []int{1, 2} {
		keys := gr.rooms[i].idx.list(objectPrefix, "", 0)
		key := keys[len(keys)-1]
		refs := gr.rooms[i].idx.all(key)
		h, _ := objectHash(key)
		if len(refs) != 1 || refs[0].l.own || !refs[0].l.ctl {
			t.Fatalf("member %d finds the Commit in %d logs, want the replica of an admin's control log", i, len(refs))
		}
		rec, err := decodeCtl(mustGet(t, refs[0].l.view, refs[0].i), 0)
		if err != nil || rec.Count != 1 || rec.ID != h {
			t.Fatalf("member %d: %+v %v", i, rec, err)
		}
	}
	t.Logf("a 4-member epoch: the sync servers sent %d bytes", sent()-before)
	if gr.nodes[0].n.sync.Stats().BytesSent == 0 {
		t.Fatal("the admin served nothing")
	}
	admin := gr.restart(0)
	keys := admin.idx.list(objectPrefix, "", 0)
	if len(keys) == 0 || getsObject(admin, keys[len(keys)-1]) == nil {
		t.Fatal("a restarted admin does not find its Commits")
	}
}

func mustGet(t testing.TB, v logView, i uint64) []byte {
	b, err := v.Get(i)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// A Commit built for a group of many leaves reaches 64 members through the pulls of the admin's control log.
func TestLargeCommitReachesSixtyFourMembers(t *testing.T) {
	if testing.Short() {
		t.Skip("64 devices")
	}
	leaves := largeLeaves(t)
	lg := buildLargeGroup(t, leaves)
	gr := newSixtyFour(t)
	h := sha256.Sum256(lg.commit)
	key := settle.ObjectKey(h)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	var before uint64
	for _, nd := range gr.nodes {
		before += nd.n.sync.Stats().BytesSent
	}
	start := time.Now()
	if err := gr.rooms[0].pins.Put(ctx, key, lg.commit); err != nil {
		t.Fatal(err)
	}
	done := make(chan time.Duration, len(gr.rooms))
	for _, r := range gr.rooms[1:] {
		go func() {
			for ctx.Err() == nil {
				if b, err := r.pins.Get(ctx, key, 0); err == nil && sha256.Sum256(b) == h {
					done <- time.Since(start)
					return
				}
				time.Sleep(20 * time.Millisecond)
			}
			done <- -1
		}()
	}
	var last time.Duration
	for range gr.rooms[1:] {
		d := <-done
		if d < 0 {
			t.Fatal("a member did not get the Commit")
		}
		last = max(last, d)
	}
	var after uint64
	for _, nd := range gr.nodes {
		after += nd.n.sync.Stats().BytesSent
	}
	t.Logf("a %d-leaf group: Commit of %d bytes in %d pieces reached 63 members in %v; the sync servers sent %d bytes in all (%.2f copies); the group took %v to build",
		leaves, len(lg.commit), len(pieces(h, lg.commit)), last.Round(time.Millisecond), after-before, float64(after-before)/float64(len(lg.commit)), lg.build.Round(time.Millisecond))
}

// newSixtyFour is a room of 64 devices on 127.0.0.1, the other 63 added in Adds of 8.
func newSixtyFour(t *testing.T) *group {
	server := newServer(t)
	first := newTNode(t, tnodeOpts{server: server})
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	r, err := first.n.Create(ctx, "sixty-four", Policy{})
	if err != nil {
		t.Fatal(err)
	}
	gr := &group{t: t, server: server, nodes: []*tnode{first}, rooms: []*Room{r}, recs: []*recorder{record(t, r)}, light: true}
	var rest []*tnode
	for range 63 {
		rest = append(rest, gr.newNode())
	}
	gr.joinAll(ctx, rest, 8)
	return gr
}
