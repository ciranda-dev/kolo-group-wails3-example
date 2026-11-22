package room

import (
	"bytes"
	"errors"
	"testing"
)

func TestSealedRoundTrip(t *testing.T) {
	s := &sealed{Epoch: 300, Nonce: [nonceSize]byte{1, 2, 3}, CT: []byte("ciphertext")}
	got, err := decodeSealed(s.encode())
	if err != nil || got.Epoch != 300 || got.Nonce != s.Nonce || !bytes.Equal(got.CT, s.CT) {
		t.Fatalf("round trip: %+v %v", got, err)
	}
}

func TestContentRoundTripAndPadding(t *testing.T) {
	c := &content{ReplyTo: evID(1), Parents: []MessageID{evID(2), evID(3)}, Time: -5, Body: bytes.Repeat([]byte("x"), 1000)}
	b := c.encode()
	if len(b) != padme(len(b)) || len(b) < 1000 {
		t.Fatalf("encoded length %d is not a Padme length", len(b))
	}
	got, err := decodeContent(b)
	if err != nil || got.ReplyTo != c.ReplyTo || len(got.Parents) != 2 || got.Parents[1] != evID(3) || got.Time != -5 || !bytes.Equal(got.Body, c.Body) {
		t.Fatalf("round trip: %+v %v", got, err)
	}
	b[len(b)-1] = 1
	if _, err := decodeContent(b); !errors.Is(err, ErrMalformed) {
		t.Fatalf("padding that is not zero: %v", err)
	}
}

func TestPadme(t *testing.T) {
	for n, want := range map[int]int{0: 0, 1: 1, 2: 2, 9: 10, 100: 104, 1000: 1024, 1 << 20: 1 << 20, 1<<20 + 1: 1<<20 + 1<<15} {
		if got := padme(n); got != want {
			t.Errorf("padme(%d) = %d, want %d", n, got, want)
		}
	}
	for n := 1; n < 5000; n++ {
		if p := padme(n); p < n || float64(p) > 1.12*float64(n)+1 {
			t.Fatalf("padme(%d) = %d", n, p)
		}
	}
}

func TestDecodersRefuseTruncatedAndHostileInput(t *testing.T) {
	good := (&content{Parents: []MessageID{evID(1)}, Body: []byte("hi")}).encode()
	for i := range good {
		if _, err := decodeContent(good[:i]); err == nil && i < len(good)-10 {
			t.Fatalf("a content cut at %d bytes decoded", i)
		}
	}
	// A parent count past the bytes that remain is refused before anything is allocated.
	huge := append(make([]byte, idSize), 0xff, 0xff, 0xff, 0xff, 0x0f)
	if _, err := decodeContent(huge); !errors.Is(err, ErrMalformed) {
		t.Fatalf("a huge parent count: %v", err)
	}
	for _, b := range [][]byte{nil, {0}, {kindMessage}, {kindMessage, 1}, {kindCommit}, bytes.Repeat([]byte{0xff}, 64)} {
		if _, err := decodeSealed(b); !errors.Is(err, ErrMalformed) {
			t.Fatalf("sealed %x: %v", b, err)
		}
	}
	if _, err := decodeSealed((&sealed{Nonce: [nonceSize]byte{}}).encode()); !errors.Is(err, ErrMalformed) {
		t.Fatalf("an empty ciphertext: %v", err)
	}
	for _, b := range [][]byte{nil, {kindCommit}, {kindCommit, 0, 0}, {kindCommit, 5, 1}, {kindMessage, 1, 1, 1, 1}} {
		if _, err := decodeCommit(b); !errors.Is(err, ErrMalformed) {
			t.Fatalf("commit %x: %v", b, err)
		}
	}
	if _, err := decodeCommit(append((&commitMsg{Settled: []byte{1}, Commit: []byte{2}}).encode(), 9)); !errors.Is(err, ErrMalformed) {
		t.Fatalf("a commit message with trailing bytes: %v", err)
	}
}

func TestInviteRoundTripAndBounds(t *testing.T) {
	v := &invite{Welcome: []byte("w"), Settled: []byte("s"), Tree: []byte("tree"), Secret: [keySize]byte{9}, Name: "room"}
	got, err := decodeInvite(v.encode(), 1<<20)
	if err != nil || string(got.Welcome) != "w" || string(got.Tree) != "tree" || got.Secret != v.Secret || got.Name != "room" {
		t.Fatalf("round trip: %+v %v", got, err)
	}
	if _, err := decodeInvite(v.encode(), 2); !errors.Is(err, ErrMalformed) && len("tree") > 2+64 {
		t.Fatalf("a tree past the bound: %v", err)
	}
	long := *v
	long.Name = string(bytes.Repeat([]byte("n"), maxName+1))
	if _, err := decodeInvite(long.encode(), 1<<20); !errors.Is(err, ErrMalformed) {
		t.Fatalf("a long name: %v", err)
	}
	if _, err := decodeInvite(append(v.encode(), 0), 1<<20); !errors.Is(err, ErrMalformed) {
		t.Fatalf("trailing bytes: %v", err)
	}
	big := *v
	big.Tree = make([]byte, 200)
	if _, err := decodeInvite(big.encode(), 100); !errors.Is(err, ErrMalformed) {
		t.Fatalf("a tree of 200 bytes under a bound of 100: %v", err)
	}
}

func TestSealOpen(t *testing.T) {
	key := [keySize]byte{1}
	ad := aad([]byte("gid"), 3, []byte("dev"), 9)
	nonce, ct, err := seal(&key, ad, []byte("plain"))
	if err != nil {
		t.Fatal(err)
	}
	if pt, err := open(&key, nonce, ad, ct); err != nil || string(pt) != "plain" {
		t.Fatalf("open: %q %v", pt, err)
	}
	other := [keySize]byte{2}
	for name, f := range map[string]func() error{
		"another key":       func() error { _, err := open(&other, nonce, ad, ct); return err },
		"another epoch":     func() error { _, err := open(&key, nonce, aad([]byte("gid"), 4, []byte("dev"), 9), ct); return err },
		"another device":    func() error { _, err := open(&key, nonce, aad([]byte("gid"), 3, []byte("dex"), 9), ct); return err },
		"another sequence":  func() error { _, err := open(&key, nonce, aad([]byte("gid"), 3, []byte("dev"), 8), ct); return err },
		"another room":      func() error { _, err := open(&key, nonce, aad([]byte("gix"), 3, []byte("dev"), 9), ct); return err },
		"a changed nonce":   func() error { n := nonce; n[0]++; _, err := open(&key, n, ad, ct); return err },
		"a changed message": func() error { c := bytes.Clone(ct); c[0] ^= 1; _, err := open(&key, nonce, ad, c); return err },
	} {
		if err := f(); !errors.Is(err, ErrOpen) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func FuzzDecodeSealed(f *testing.F) {
	f.Add((&sealed{Epoch: 1, CT: []byte("x")}).encode())
	f.Add([]byte{kindMessage, 0xff})
	f.Fuzz(func(t *testing.T, b []byte) {
		if s, err := decodeSealed(b); err == nil && !bytes.Equal(s.encode(), b) {
			t.Fatal("a body that decodes does not encode back to itself")
		}
	})
}

func FuzzDecodeContent(f *testing.F) {
	f.Add((&content{Parents: []MessageID{evID(1)}, Body: []byte("hi")}).encode())
	f.Add(make([]byte, idSize+3))
	f.Fuzz(func(t *testing.T, b []byte) {
		c, err := decodeContent(b)
		if err != nil {
			return
		}
		if len(c.Parents)*idSize > len(b) {
			t.Fatal("more parents than bytes")
		}
		if again, err := decodeContent(c.encode()); err != nil || !bytes.Equal(again.Body, c.Body) || again.Time != c.Time {
			t.Fatalf("re-encoding: %v", err)
		}
	})
}

func FuzzDecodeCommit(f *testing.F) {
	f.Add((&commitMsg{Settled: []byte{1}, Commit: []byte{2}}).encode())
	f.Fuzz(func(t *testing.T, b []byte) {
		if c, err := decodeCommit(b); err == nil && !bytes.Equal(c.encode(), b) {
			t.Fatal("a commit message that decodes does not encode back to itself")
		}
	})
}

func FuzzDecodeInvite(f *testing.F) {
	f.Add((&invite{Welcome: []byte("w"), Settled: []byte("s"), Tree: []byte("t"), Name: "n"}).encode())
	f.Fuzz(func(t *testing.T, b []byte) {
		if v, err := decodeInvite(b, 1<<16); err == nil && !bytes.Equal(v.encode(), b) {
			t.Fatal("an invite that decodes does not encode back to itself")
		}
	})
}
