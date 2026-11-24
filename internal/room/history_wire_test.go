package room

import (
	"bytes"
	"errors"
	"testing"
)

func TestHistoryRuleDecidesWhichLinksExist(t *testing.T) {
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
		{histPolicy{Rule: AllHistory, Since: 5}, 5, 9, false, 5},
		{histPolicy{Rule: AllHistory, Since: 5}, 6, 9, true, 5},
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

// A rule set by a Commit holds from the next epoch, and history that was cut is never joined again.
func TestHistoryRuleNext(t *testing.T) {
	all, none := histPolicy{}, histPolicy{Rule: NoHistory}
	for _, c := range []struct {
		name      string
		old, want histPolicy
		epoch     uint64
		got       histPolicy
	}{
		{"all stays all", all, all, 9, all},
		{"all to none", all, none, 9, none},
		{"none stays none", none, none, 9, none},
		{"none to all reaches back to the cut", none, all, 9, histPolicy{Rule: AllHistory, Since: 9}},
		{"none to since after the cut", none, histPolicy{Rule: SinceEpoch, Since: 20}, 9, histPolicy{Rule: SinceEpoch, Since: 20}},
		{"none to since before the cut", none, histPolicy{Rule: SinceEpoch, Since: 3}, 9, histPolicy{Rule: SinceEpoch, Since: 9}},
		{"since to all keeps the cut", histPolicy{Rule: SinceEpoch, Since: 5}, all, 9, histPolicy{Rule: AllHistory, Since: 5}},
		{"since to an earlier since keeps the cut", histPolicy{Rule: SinceEpoch, Since: 5}, histPolicy{Rule: SinceEpoch, Since: 2}, 9, histPolicy{Rule: SinceEpoch, Since: 5}},
		{"all to a later since", all, histPolicy{Rule: SinceEpoch, Since: 30}, 9, histPolicy{Rule: SinceEpoch, Since: 30}},
	} {
		if got := c.old.next(c.want, c.epoch); got != c.got {
			t.Errorf("%s: %+v", c.name, got)
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
