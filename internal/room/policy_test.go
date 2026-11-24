package room

import (
	"bytes"
	"crypto/ed25519"
	"errors"
	"testing"

	kolomls "github.com/ciranda-dev/kolo-mls"
	"github.com/ciranda-dev/kolo-mls/settle"
)

func key32(b byte) [32]byte { return [32]byte{b} }

func kb(b byte) []byte { k := key32(b); return k[:] }

func samplePolicy() *roomPolicy {
	return &roomPolicy{hist: histPolicy{Rule: SinceEpoch, Since: 7}, persons: [][32]byte{key32(1), key32(5)}, devices: [][32]byte{key32(2)}, admins: true, link: true, secret: key32(9)}
}

func TestRoomPolicyRoundTripAndRefusals(t *testing.T) {
	for _, p := range []*roomPolicy{{}, samplePolicy(), {hist: histPolicy{Rule: NoHistory}, admins: true}, {hist: histPolicy{Rule: AllHistory, Since: 4}}} {
		got, err := decodeRoomPolicy(p.encode())
		if err != nil || !bytes.Equal(got.encode(), p.encode()) {
			t.Errorf("%+v: %+v %v", p, got, err)
		}
	}
	good := samplePolicy().encode()
	for name, b := range map[string][]byte{
		"nothing":               nil,
		"another version":       {1, 0, 0, 0, 0, 0},
		"an unknown rule":       {0, 3, 0, 0, 0, 0},
		"an epoch with none":    {0, 2, 5, 0, 0, 0},
		"no epoch":              {0, 1},
		"unknown flags":         {0, 0, 0, 4, 0, 0},
		"more keys than data":   {0, 0, 0, 0, 2, 1, 0},
		"trailing bytes":        append(bytes.Clone((&roomPolicy{}).encode()), 0),
		"truncated":             good[:len(good)-1],
		"a link with no list":   append([]byte{0, 0, 0, 2, 0, 0}, make([]byte, 32)...),
		"a link with no secret": (&roomPolicy{persons: [][32]byte{key32(1)}}).encode()[:3],
		"persons out of order":  append(append([]byte{0, 0, 0, 0, 2}, kb(5)...), append(kb(1), 0)...),
		"a person twice":        append(append([]byte{0, 0, 0, 0, 2}, kb(5)...), append(kb(5), 0)...),
	} {
		if _, err := decodeRoomPolicy(b); !errors.Is(err, ErrMalformed) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if p, err := policyOf(nil); err != nil || p.encode() == nil || p.restricted() || p.hist != (histPolicy{}) {
		t.Errorf("a room with no policy: %+v %v", p, err)
	}
	exts := []kolomls.Extension{{Type: 1}, samplePolicy().extension()}
	if p, err := policyOf(exts); err != nil || p.hist.Since != 7 || !p.link {
		t.Errorf("policyOf: %+v %v", p, err)
	}
	if _, err := policyOf([]kolomls.Extension{{Type: policyExtension, Data: []byte{9}}}); err == nil {
		t.Error("a bad policy extension")
	}
}

func TestMayPost(t *testing.T) {
	roles := &settle.Roles{Admins: [][32]byte{key32(7)}, Settlers: [][32]byte{key32(7)}}
	person, device, admin, other := key32(1), key32(2), key32(7), key32(3)
	for _, c := range []struct {
		name string
		p    roomPolicy
		who  [2][32]byte
		ok   bool
	}{
		{"no list", roomPolicy{}, [2][32]byte{other, other}, true},
		{"a listed person", roomPolicy{persons: [][32]byte{person}}, [2][32]byte{person, other}, true},
		{"a person that is not listed", roomPolicy{persons: [][32]byte{person}}, [2][32]byte{other, other}, false},
		{"a listed device", roomPolicy{devices: [][32]byte{device}}, [2][32]byte{other, device}, true},
		{"a device that is not listed", roomPolicy{devices: [][32]byte{device}}, [2][32]byte{person, other}, false},
		{"an admin device", roomPolicy{admins: true}, [2][32]byte{other, admin}, true},
		{"a device that is no admin", roomPolicy{admins: true}, [2][32]byte{person, device}, false},
	} {
		if got := c.p.mayPost(c.who[0][:], c.who[1][:], roles); got != c.ok {
			t.Errorf("%s: %v", c.name, got)
		}
	}
	if (&roomPolicy{persons: [][32]byte{person}}).mayPost(nil, nil, roles) {
		t.Error("a device with no key may post")
	}
}

func TestPolicyBuild(t *testing.T) {
	a, b := ed25519.PublicKey(bytes.Repeat([]byte{1}, 32)), ed25519.PublicKey(bytes.Repeat([]byte{2}, 32))
	p, err := (Policy{History: SinceEpoch, Since: 4, Posters: []ed25519.PublicKey{b, a, b}, PosterDevices: []ed25519.PublicKey{a}, AdminsPost: true, ReadLink: true}).build(nil)
	if err != nil || p.hist.Since != 4 || len(p.persons) != 2 || p.persons[0] != [32]byte(a) || len(p.devices) != 1 || !p.admins || !p.link || p.secret == ([keySize]byte{}) {
		t.Fatalf("a good policy: %+v %v", p, err)
	}
	same, err := (Policy{Posters: []ed25519.PublicKey{a}, ReadLink: true}).build(p)
	if err != nil || same.secret != p.secret {
		t.Errorf("the link of a room that has one changed: %v", err)
	}
	off, err := (Policy{Posters: []ed25519.PublicKey{a}}).build(p)
	if err != nil || off.link {
		t.Errorf("a link that was turned off: %+v %v", off, err)
	}
	again, err := (Policy{Posters: []ed25519.PublicKey{a}, ReadLink: true}).build(off)
	if err != nil || again.secret == p.secret {
		t.Errorf("a link turned on again has the old secret: %v", err)
	}
	if got := p.public(); !got.ReadLink || len(got.Posters) != 2 || got.Since != 4 || !got.AdminsPost {
		t.Errorf("public: %+v", got)
	}
	for name, p := range map[string]Policy{
		"an unknown rule":     {History: 3},
		"an epoch with all":   {Since: 1},
		"an epoch with none":  {History: NoHistory, Since: 1},
		"a short poster key":  {Posters: []ed25519.PublicKey{{1}}},
		"a short device key":  {PosterDevices: []ed25519.PublicKey{nil}},
		"a link with no list": {ReadLink: true},
	} {
		if _, err := p.build(nil); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestReadStateAndLinkRoundTripAndRefusals(t *testing.T) {
	st := &readState{Key: key32(3), Roles: []byte{1, 2}, Policy: []byte{3}, Authors: [][]byte{{4, 5}, {}}}
	got, err := decodeReadState(st.encode())
	if err != nil || got.Key != st.Key || !bytes.Equal(got.Roles, st.Roles) || len(got.Authors) != 2 {
		t.Fatalf("a state: %+v %v", got, err)
	}
	for name, b := range map[string][]byte{"nothing": nil, "no key": st.encode()[:5], "trailing": append(st.encode(), 1), "more authors than data": append(bytes.Clone(kb(1)), 0, 0, 200), "cut": st.encode()[:len(st.encode())-1]} {
		if _, err := decodeReadState(b); !errors.Is(err, ErrMalformed) {
			t.Errorf("a state with %s: %v", name, err)
		}
	}
	rk, other, gid := key32(1), key32(2), []byte("room")
	b, err := sealRead(gid, 7, &rk, st)
	if err != nil {
		t.Fatal(err)
	}
	if s, err := openRead(gid, 7, &rk, b); err != nil || s.Key != st.Key {
		t.Fatalf("a good record: %v", err)
	}
	flip := bytes.Clone(b)
	flip[len(flip)-1] ^= 1
	for name, c := range map[string]struct {
		gid   []byte
		epoch uint64
		key   *[keySize]byte
		b     []byte
	}{"another key": {gid, 7, &other, b}, "another epoch": {gid, 8, &rk, b}, "another room": {[]byte("x"), 7, &rk, b}, "a flipped bit": {gid, 7, &rk, flip}} {
		if _, err := openRead(c.gid, c.epoch, c.key, c.b); !errors.Is(err, ErrOpen) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := openRead(gid, 7, &rk, b[:nonceSize]); !errors.Is(err, ErrMalformed) {
		t.Errorf("a record of a nonce: %v", err)
	}

	l := &readLink{GID: key32(1), Secret: key32(2), ReadSecret: key32(3), Name: "room", Epoch: 9, State: b, Logs: [][]byte{{1}, {2, 3}}}
	back, err := decodeReadLink(l.encode(), len(b))
	if err != nil || !bytes.Equal(back.encode(), l.encode()) {
		t.Fatalf("a link: %+v %v", back, err)
	}
	enc := l.encode()
	for name, c := range map[string]struct {
		b   []byte
		max int
	}{"nothing": {nil, 100}, "a cut link": {enc[:len(enc)-1], 1000}, "trailing": {append(bytes.Clone(enc), 1), 1000}, "a state past its bound": {enc, len(b) - 1}, "another version": {append([]byte{1}, enc[1:]...), 1000}, "a long name": {(&readLink{Name: string(make([]byte, maxName+1))}).encode(), 1000}} {
		if _, err := decodeReadLink(c.b, c.max); !errors.Is(err, ErrMalformed) {
			t.Errorf("a link with %s: %v", name, err)
		}
	}
}

func TestReadRecordRoundTripAndRefusals(t *testing.T) {
	rec := &ctlRecord{Kind: ctlRead, Epoch: 12, Value: []byte{1, 2, 3}}
	got, err := decodeCtl(rec.encode(), 100)
	if err != nil || got.Epoch != 12 || !bytes.Equal(got.Value, rec.Value) {
		t.Fatalf("%+v %v", got, err)
	}
	if _, err := decodeCtl(rec.encode(), 2); !errors.Is(err, ErrMalformed) {
		t.Errorf("a value past its bound: %v", err)
	}
	if _, err := decodeCtl(rec.encode()[:3], 100); !errors.Is(err, ErrMalformed) {
		t.Errorf("a cut record: %v", err)
	}
}

func FuzzDecodeRoomPolicy(f *testing.F) {
	f.Add((&roomPolicy{}).encode())
	f.Add(samplePolicy().encode())
	f.Fuzz(func(t *testing.T, b []byte) {
		if p, err := decodeRoomPolicy(b); err == nil && !bytes.Equal(p.encode(), b) {
			t.Fatal("a policy does not encode back to its bytes")
		}
	})
}

func FuzzDecodeReadState(f *testing.F) {
	f.Add((&readState{Key: key32(1), Roles: []byte{1}, Policy: []byte{2}, Authors: [][]byte{{3}}}).encode())
	f.Fuzz(func(t *testing.T, b []byte) {
		if s, err := decodeReadState(b); err == nil && !bytes.Equal(s.encode(), b) {
			t.Fatal("a state does not encode back to its bytes")
		}
	})
}

func FuzzDecodeReadLink(f *testing.F) {
	f.Add((&readLink{Name: "r", Epoch: 1, State: []byte{1}, Logs: [][]byte{{1}}}).encode())
	f.Fuzz(func(t *testing.T, b []byte) {
		if l, err := decodeReadLink(b, 1<<16); err == nil && !bytes.Equal(l.encode(), b) {
			t.Fatal("a link does not encode back to its bytes")
		}
	})
}

func FuzzOpenRead(f *testing.F) {
	rk := key32(1)
	b, _ := sealRead([]byte("room"), 3, &rk, &readState{Key: key32(2)})
	f.Add(b, uint64(3))
	f.Fuzz(func(t *testing.T, rec []byte, epoch uint64) {
		if s, err := openRead([]byte("room"), epoch, &rk, rec); err == nil && (epoch != 3 || s.Key != key32(2)) {
			t.Fatal("a record that is not the sealed one opened")
		}
	})
}
