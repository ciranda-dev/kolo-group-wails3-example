package room

import (
	"crypto/ed25519"
	"crypto/sha256"
	"os"
	"strconv"
	"testing"
	"time"

	koloid "github.com/ciranda-dev/kolo-id"

	kolomls "github.com/ciranda-dev/kolo-mls"
	"github.com/ciranda-dev/kolo-mls/cred"
	"github.com/ciranda-dev/kolo-mls/settle"
)

// largeLeaves is the size of the groups the large tests build: 1,024 leaves, or what KOLO_GROUP_LEAVES says (30000 runs the
// measurements of the large-group plan, at about 2.5 MB for a Commit and 14.5 MB for a sealed tree).
func largeLeaves(t testing.TB) int {
	if s := os.Getenv("KOLO_GROUP_LEAVES"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 2 {
			t.Fatalf("KOLO_GROUP_LEAVES=%q", s)
		}
		return n
	}
	return 1024
}

type acceptAll struct{}

func (acceptAll) CheckProposal(kolomls.ProposalView) error { return nil }
func (acceptAll) CheckCommit(kolomls.CommitView) error     { return nil }
func (acceptAll) CheckJoin(kolomls.JoinView) error         { return nil }

// largeGroup is a group of leaves members built in this process, as kolo-mls's own measurement builds one: the creator adds
// every other leaf in one Commit. It returns the Commit of a key update that the creator makes next, and the sealed tree.
type largeGroup struct {
	commit, sealed []byte
	build          time.Duration
}

func buildLargeGroup(t testing.TB, leaves int) *largeGroup {
	t.Helper()
	start := time.Now()
	person := ident(t)
	st := kolomls.NewMemStore()
	cfg := kolomls.Config{Authenticator: &cred.Authenticator{Now: time.Now}, Policy: acceptAll{}}
	var self *koloid.Identity
	keyPackage := func(i int) []byte {
		dev := ident(t)
		offer, err := person.OfferDeviceCert(dev.PublicKey(), &koloid.IssueOptions{Serial: uint64(i + 1)})
		if err != nil {
			t.Fatal(err)
		}
		cert, err := dev.AcceptDeviceCert(offer, person.PublicKey())
		if err != nil {
			t.Fatal(err)
		}
		_, sig, err := ed25519.GenerateKey(nil)
		if err != nil {
			t.Fatal(err)
		}
		c, err := cred.New(dev, cert, sig.Public().(ed25519.PublicKey))
		if err != nil {
			t.Fatal(err)
		}
		kp, err := kolomls.NewKeyPackage(st, kolomls.KeyPackageOptions{Credential: c, SignatureKey: sig})
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			self = dev
		}
		return kp
	}
	g, err := kolomls.Create(st, kolomls.CreateOptions{Config: cfg, KeyPackage: keyPackage(0)})
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	kps := make([][]byte, leaves-1)
	for i := range kps {
		kps[i] = keyPackage(i + 1)
	}
	if _, err := g.Commit(kolomls.CommitOptions{Add: kps}); err != nil {
		t.Fatal(err)
	}
	if err := g.ApplyPending(); err != nil {
		t.Fatal(err)
	}
	tree, err := g.RatchetTree()
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := settle.Seal(g.Export, g.GroupID(), g.Epoch(), [32]byte(self.PublicKey()), tree)
	if err != nil {
		t.Fatal(err)
	}
	pc, err := g.Commit(kolomls.CommitOptions{})
	if err != nil {
		t.Fatal(err)
	}
	commit := pc.Commit()
	if err := g.DiscardPending(); err != nil {
		t.Fatal(err)
	}
	if n := len(g.Tree().Members()); n != leaves {
		t.Fatalf("%d leaves built, want %d", n, leaves)
	}
	return &largeGroup{commit: commit, sealed: sealed, build: time.Since(start)}
}

func TestBuildLargeGroup(t *testing.T) {
	n := largeLeaves(t)
	g := buildLargeGroup(t, n)
	t.Logf("%d leaves: Commit %d B, sealed tree %d B, built in %v, hash %x", n, len(g.commit), len(g.sealed), g.build.Round(time.Millisecond), sha256.Sum256(g.commit))
	if len(g.commit) == 0 || len(g.sealed) == 0 {
		t.Fatal("empty")
	}
}
