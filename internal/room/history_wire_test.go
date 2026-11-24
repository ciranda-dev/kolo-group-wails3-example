package room

import (
	"bytes"
	"crypto/ed25519"
	"errors"
	"testing"

	kolomls "github.com/ciranda-dev/kolo-mls"
)

func TestPolicyRoundTripAndRefusals(t *testing.T) {
	for _, p := range []histPolicy{{}, {Rule: SinceEpoch, Since: 7}, {Rule: NoHistory}} {
		got, err := decodeHistPolicy(p.encode())
		if err != nil || got != p {
			t.Errorf("%+v: %+v %v", p, got, err)
		}
	}
	for name, b := range map[string][]byte{
		"nothing":            nil,
		"another version":    {1, 0, 0},
		"an unknown rule":    {0, 3, 0},
		"an epoch with all":  {0, 0, 5},
		"an epoch with none": {0, 2, 5},
		"no epoch":           {0, 1},
		"trailing bytes":     {0, 0, 0, 0},
	} {
		if _, err := decodeHistPolicy(b); !errors.Is(err, ErrMalformed) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if p, err := policyOf(nil); err != nil || p != (histPolicy{}) {
		t.Errorf("a room with no policy: %+v %v", p, err)
	}
	exts := []kolomls.Extension{{Type: 1}, histPolicy{Rule: SinceEpoch, Since: 3}.extension()}
	if p, err := policyOf(exts); err != nil || p.Since != 3 {
		t.Errorf("policyOf: %+v %v", p, err)
	}
	if _, err := policyOf([]kolomls.Extension{{Type: policyExtension, Data: []byte{9}}}); err == nil {
		t.Error("a bad policy extension")
	}
}

func TestPolicyDecidesWhichLinksExist(t *testing.T) {
	for _, c := range []struct {
		p     histPolicy
		epoch uint64
		join  uint64
		allow bool
		floor uint64
	}{
		{histPolicy{}, 0, 9, false, 0},
		{histPolicy{}, 1, 9, true, 0},
		{histPolicy{Rule: SinceEpoch, Since: 5}, 5, 9, false, 5},
		{histPolicy{Rule: SinceEpoch, Since: 5}, 6, 9, true, 5},
		{histPolicy{Rule: SinceEpoch, Since: 5}, 6, 3, true, 3},
		{histPolicy{Rule: NoHistory}, 6, 9, false, 9},
	} {
		if got := c.p.allows(c.epoch); got != c.allow {
			t.Errorf("%+v allows(%d) = %v", c.p, c.epoch, got)
		}
		if got := c.p.floor(c.join); got != c.floor {
			t.Errorf("%+v floor(%d) = %d", c.p, c.join, got)
		}
	}
}

func TestPolicyValidate(t *testing.T) {
	if p, err := (Policy{History: SinceEpoch, Since: 4}).validate(); err != nil || p.Since != 4 {
		t.Errorf("a good policy: %+v %v", p, err)
	}
	for name, p := range map[string]Policy{"an unknown rule": {History: 3}, "an epoch with all": {Since: 1}, "an epoch with none": {History: NoHistory, Since: 1}} {
		if _, err := p.validate(); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: %v", name, err)
		}
	}
	for _, p := range []Policy{{ReadLink: true}, {Posters: []ed25519.PublicKey{{1}}}} {
		if _, err := p.validate(); !errors.Is(err, ErrNotImplemented) {
			t.Errorf("%+v: %v", p, err)
		}
	}
}

func TestLinkOpensOnlyWithTheNextKeyOfTheSameRoomAndEpoch(t *testing.T) {
	gid := []byte("room")
	next, prev, other := [keySize]byte{1}, [keySize]byte{2}, [keySize]byte{3}
	l, err := sealLink(gid, 7, &next, &prev)
	if err != nil || len(l) != linkSize {
		t.Fatalf("%d bytes: %v", len(l), err)
	}
	if got, err := openLink(gid, 7, &next, l); err != nil || got != prev {
		t.Fatalf("a good link: %x %v", got, err)
	}
	flip := bytes.Clone(l)
	flip[len(flip)-1] ^= 1
	for name, c := range map[string]struct {
		gid  []byte
		e    uint64
		key  *[keySize]byte
		link []byte
	}{
		"another key":   {gid, 7, &other, l},
		"the prev key":  {gid, 7, &prev, l},
		"another epoch": {gid, 8, &next, l},
		"another room":  {[]byte("other"), 7, &next, l},
		"a flipped bit": {gid, 7, &next, flip},
	} {
		if _, err := openLink(c.gid, c.e, c.key, c.link); !errors.Is(err, ErrOpen) {
			t.Errorf("%s: %v", name, err)
		}
	}
	for _, b := range [][]byte{nil, l[:linkSize-1], append(bytes.Clone(l), 0)} {
		if _, err := openLink(gid, 7, &next, b); !errors.Is(err, ErrMalformed) {
			t.Errorf("%d bytes: %v", len(b), err)
		}
	}
}

func FuzzDecodeHistPolicy(f *testing.F) {
	f.Add(histPolicy{}.encode())
	f.Add(histPolicy{Rule: SinceEpoch, Since: 1 << 40}.encode())
	f.Fuzz(func(t *testing.T, b []byte) {
		if p, err := decodeHistPolicy(b); err == nil && !bytes.Equal(p.encode(), b) {
			t.Fatal("a policy does not encode back to its bytes")
		}
	})
}

func FuzzOpenLink(f *testing.F) {
	key, prev := [keySize]byte{1}, [keySize]byte{2}
	l, _ := sealLink([]byte("room"), 3, &key, &prev)
	f.Add(l, uint64(3))
	f.Fuzz(func(t *testing.T, link []byte, epoch uint64) {
		if got, err := openLink([]byte("room"), epoch, &key, link); err == nil && (epoch != 3 || got != prev) {
			t.Fatal("a link that is not the sealed one opened")
		}
	})
}
