package room

import (
	"bytes"
	"errors"
	"testing"

	kololog "github.com/ciranda-dev/kolo-log"
)

func goodSealed() *sealed {
	return &sealed{Epoch: 3, Nonce: [nonceSize]byte{1}, Sig: [sigSize]byte{2}, CT: []byte("ciphertext")}
}

func TestStoredRoundTripAndRefusals(t *testing.T) {
	s := &stored{Device: [32]byte{7}, Seq: 1 << 40, Body: goodSealed().encode()}
	got, err := decodeStored(s.encode())
	if err != nil || got.Device != s.Device || got.Seq != s.Seq || !bytes.Equal(got.Body, s.Body) || got.sealed.Epoch != 3 {
		t.Fatalf("round trip: %+v %v", got, err)
	}
	for _, b := range [][]byte{nil, make([]byte, 31), make([]byte, 32), append(make([]byte, 32), 0x80), append(make([]byte, 33), kindMessage)} {
		if _, err := decodeStored(b); !errors.Is(err, ErrMalformed) {
			t.Errorf("stored %x: %v", b, err)
		}
	}
	// A body that is not a sealed message is refused, so an entry that holds a Commit message is no message.
	if _, err := decodeStored((&stored{Body: (&commitMsg{Settled: []byte{1}, Commit: []byte{2}}).encode()}).encode()); !errors.Is(err, ErrMalformed) {
		t.Errorf("a stored Commit message: %v", err)
	}
}

func TestMessageIDBindsEverythingTheAuthorSigned(t *testing.T) {
	gid, dev, body := []byte("gid"), []byte("device"), []byte("body")
	id := messageID(gid, dev, 5, body)
	for name, other := range map[string]MessageID{
		"room":     messageID([]byte("gix"), dev, 5, body),
		"device":   messageID(gid, []byte("devicf"), 5, body),
		"sequence": messageID(gid, dev, 6, body),
		"body":     messageID(gid, dev, 5, []byte("bodz")),
	} {
		if other == id {
			t.Errorf("another %s gives the same id", name)
		}
	}
	if messageID(gid, dev, 5, body) != id {
		t.Fatal("the id is not stable")
	}
}

func TestSealedSignatureCoversTheCiphertextAndWhoSent(t *testing.T) {
	id := ident(t)
	gid := []byte("gid")
	s := goodSealed()
	if err := signSealed(id, gid, s, 9); err != nil {
		t.Fatal(err)
	}
	if err := verifySealed(id.PublicKey(), gid, s, 9); err != nil {
		t.Fatalf("a good signature: %v", err)
	}
	flip := func(f func(*sealed)) *sealed { c := *s; c.CT = bytes.Clone(s.CT); f(&c); return &c }
	for name, c := range map[string]struct {
		s   *sealed
		gid []byte
		seq uint64
		dev []byte
	}{
		"ciphertext": {flip(func(x *sealed) { x.CT[0] ^= 1 }), gid, 9, id.PublicKey()},
		"epoch":      {flip(func(x *sealed) { x.Epoch++ }), gid, 9, id.PublicKey()},
		"nonce":      {flip(func(x *sealed) { x.Nonce[0] ^= 1 }), gid, 9, id.PublicKey()},
		"sequence":   {s, gid, 10, id.PublicKey()},
		"room":       {s, []byte("gix"), 9, id.PublicKey()},
		"device":     {s, gid, 9, ident(t).PublicKey()},
	} {
		if err := verifySealed(c.dev, c.gid, c.s, c.seq); !errors.Is(err, ErrSignature) {
			t.Errorf("a changed %s: %v", name, err)
		}
	}
}

func TestControlRecordRoundTripAndRefusals(t *testing.T) {
	for _, c := range []*ctlRecord{
		{Kind: ctlPut, Key: "kolo-mls/settled/x/1", Value: []byte("value")},
		{Kind: ctlOpen, Epoch: 12, Header: []byte("header")},
		{Kind: ctlClose, ID: [32]byte{5}, Size: 99},
		{Kind: ctlDir, Header: []byte("header")},
	} {
		got, err := decodeCtl(c.encode(), 1<<10)
		if err != nil || got.Kind != c.Kind || got.Key != c.Key || !bytes.Equal(got.Value, c.Value) || got.Epoch != c.Epoch || !bytes.Equal(got.Header, c.Header) || got.ID != c.ID || got.Size != c.Size {
			t.Errorf("kind %d: %+v %v", c.Kind, got, err)
		}
		if _, err := decodeCtl(append(c.encode(), 0), 1<<10); !errors.Is(err, ErrMalformed) {
			t.Errorf("kind %d with trailing bytes: %v", c.Kind, err)
		}
		for i := range c.encode() {
			if _, err := decodeCtl(c.encode()[:i], 1<<10); err == nil {
				t.Errorf("kind %d cut at %d bytes decoded", c.Kind, i)
			}
		}
	}
	if _, err := decodeCtl([]byte{9}, 10); !errors.Is(err, ErrMalformed) {
		t.Errorf("an unknown kind: %v", err)
	}
	if _, err := decodeCtl((&ctlRecord{Kind: ctlPut, Key: "k", Value: make([]byte, 11)}).encode(), 10); !errors.Is(err, ErrMalformed) {
		t.Errorf("a value past the bound: %v", err)
	}
	if _, err := decodeCtl((&ctlRecord{Kind: ctlOpen, Header: make([]byte, maxHeader+1)}).encode(), 10); !errors.Is(err, ErrMalformed) {
		t.Errorf("a header past the bound: %v", err)
	}
}

func TestAdminMessagesRoundTripAndRefusals(t *testing.T) {
	req := &adminRequest{Request: []byte("request"), Header: []byte("header")}
	got, err := decodeAdminRequest(req.encode())
	if err != nil || !bytes.Equal(got.Request, req.Request) || !bytes.Equal(got.Header, req.Header) {
		t.Fatalf("request: %+v %v", got, err)
	}
	for _, b := range [][]byte{nil, {kindAdminRequest}, (&adminRequest{Header: []byte("h")}).encode(), (&adminRequest{Request: []byte("r")}).encode(), append(req.encode(), 1), (&adminRequest{Request: make([]byte, maxRequest+1), Header: []byte("h")}).encode()} {
		if _, err := decodeAdminRequest(b); !errors.Is(err, ErrMalformed) {
			t.Errorf("request %x: %v", b, err)
		}
	}
	for _, o := range []*adminOffer{{Kind: kindPromote, Device: [32]byte{1}}, {Kind: kindAdminWelcome, Device: [32]byte{2}, Welcome: []byte("w")}} {
		got, err := decodeAdminOffer(o.encode())
		if err != nil || got.Kind != o.Kind || got.Device != o.Device || !bytes.Equal(got.Welcome, o.Welcome) {
			t.Errorf("offer %d: %+v %v", o.Kind, got, err)
		}
		if _, err := decodeAdminOffer(append(o.encode(), 1)); !errors.Is(err, ErrMalformed) {
			t.Errorf("offer %d with trailing bytes: %v", o.Kind, err)
		}
	}
	for _, b := range [][]byte{nil, {kindCommit}, {kindPromote}, append([]byte{kindAdminWelcome}, make([]byte, 32)...), append(append([]byte{kindAdminWelcome}, make([]byte, 32)...), 0)} {
		if _, err := decodeAdminOffer(b); !errors.Is(err, ErrMalformed) {
			t.Errorf("offer %x: %v", b, err)
		}
	}
}

func TestMetaAndLogRecordsRoundTrip(t *testing.T) {
	m := &meta{Name: "room", Secret: [keySize]byte{3}, Prefix: "room/ab/", JoinEpoch: 7, Ctl: kololog.LogID{1}, HasCtl: true, Seg: kololog.LogID{2}, Braid: true}
	got, err := decodeMeta(m.encode())
	if err != nil || *got != *m {
		t.Fatalf("meta: %+v %v", got, err)
	}
	for i := range m.encode() {
		if _, err := decodeMeta(m.encode()[:i]); err == nil {
			t.Errorf("meta cut at %d bytes decoded", i)
		}
	}
	l := &logRec{Ctl: true, Own: true, Epoch: 4, Closed: true, Size: 10, Cursor: 8, Header: []byte("header")}
	gl, err := decodeLogRec(l.encode())
	if err != nil || gl.Ctl != l.Ctl || gl.Own != l.Own || gl.Epoch != 4 || !gl.Closed || gl.Size != 10 || gl.Cursor != 8 || !bytes.Equal(gl.Header, l.Header) {
		t.Fatalf("log record: %+v %v", gl, err)
	}
	for i := range l.encode() {
		if _, err := decodeLogRec(l.encode()[:i]); err == nil {
			t.Errorf("log record cut at %d bytes decoded", i)
		}
	}
	// A flag that is not defined makes a record that would not encode back to its bytes.
	lb := l.encode()
	lb[0] |= 0x80
	if _, err := decodeLogRec(lb); !errors.Is(err, ErrMalformed) {
		t.Errorf("a log record with an unknown flag: %v", err)
	}
	mb := m.encode()
	mb[len(mb)-1] |= 0x80
	if _, err := decodeMeta(mb); !errors.Is(err, ErrMalformed) {
		t.Errorf("a meta record with an unknown flag: %v", err)
	}
	if _, err := decodeAuthor([]byte("short")); !errors.Is(err, ErrMalformed) {
		t.Errorf("a short author record: %v", err)
	}
	if _, err := decodeAuthor(make([]byte, 40)); !errors.Is(err, ErrMalformed) {
		t.Errorf("an author record with no certificate: %v", err)
	}
}

func TestInviteCarriesTheControlLogs(t *testing.T) {
	v := &invite{Welcome: []byte("w"), Settled: []byte("s"), Tree: []byte("t"), Name: "n", Logs: [][]byte{[]byte("one"), []byte("two")}}
	got, err := decodeInvite(v.encode(), 1<<10)
	if err != nil || len(got.Logs) != 2 || string(got.Logs[1]) != "two" {
		t.Fatalf("round trip: %+v %v", got, err)
	}
	huge := append(v.encode()[:len(v.encode())-len(encodeLogs(v.Logs))], 0xff, 0xff, 0xff, 0x0f)
	if _, err := decodeInvite(huge, 1<<10); !errors.Is(err, ErrMalformed) {
		t.Errorf("a count of logs past the bytes that remain: %v", err)
	}
	if _, err := decodeInvite((&invite{Welcome: []byte("w"), Settled: []byte("s"), Tree: []byte("t"), Logs: [][]byte{make([]byte, maxHeader+1)}}).encode(), 1<<10); !errors.Is(err, ErrMalformed) {
		t.Errorf("a header past the bound: %v", err)
	}
}

func encodeLogs(logs [][]byte) []byte {
	w := (&invite{Logs: logs}).encode()
	base := (&invite{}).encode()
	return w[len(base)-1:]
}

func FuzzDecodeStored(f *testing.F) {
	f.Add((&stored{Device: [32]byte{1}, Seq: 2, Body: goodSealed().encode()}).encode())
	f.Fuzz(func(t *testing.T, b []byte) {
		if s, err := decodeStored(b); err == nil && !bytes.Equal(s.encode(), b) {
			t.Fatal("a stored entry does not encode back to its bytes")
		}
	})
}

func FuzzDecodeCtl(f *testing.F) {
	f.Add((&ctlRecord{Kind: ctlPut, Key: "k", Value: []byte("v")}).encode())
	f.Add((&ctlRecord{Kind: ctlOpen, Epoch: 1, Header: []byte("h")}).encode())
	f.Add((&ctlRecord{Kind: ctlClose, Size: 3}).encode())
	f.Add((&ctlRecord{Kind: ctlDir, Header: []byte("h")}).encode())
	f.Fuzz(func(t *testing.T, b []byte) {
		if c, err := decodeCtl(b, 1<<12); err == nil && !bytes.Equal(c.encode(), b) {
			t.Fatal("a control record does not encode back to its bytes")
		}
	})
}

func FuzzDecodeAdminMessages(f *testing.F) {
	f.Add((&adminRequest{Request: []byte("r"), Header: []byte("h")}).encode())
	f.Add((&adminOffer{Kind: kindPromote}).encode())
	f.Add((&adminOffer{Kind: kindAdminWelcome, Welcome: []byte("w")}).encode())
	f.Fuzz(func(t *testing.T, b []byte) {
		if r, err := decodeAdminRequest(b); err == nil && !bytes.Equal(r.encode(), b) {
			t.Fatal("a request does not encode back to its bytes")
		}
		if o, err := decodeAdminOffer(b); err == nil && !bytes.Equal(o.encode(), b) {
			t.Fatal("an offer does not encode back to its bytes")
		}
	})
}

func FuzzDecodeRecords(f *testing.F) {
	f.Add((&meta{Name: "n", HasCtl: true}).encode(), (&logRec{Header: []byte("h"), Closed: true}).encode())
	f.Fuzz(func(t *testing.T, m, l []byte) {
		if v, err := decodeMeta(m); err == nil && !bytes.Equal(v.encode(), m) {
			t.Fatal("a meta record does not encode back to its bytes")
		}
		if v, err := decodeLogRec(l); err == nil && !bytes.Equal(v.encode(), l) {
			t.Fatal("a log record does not encode back to its bytes")
		}
		decodeAuthor(m)
	})
}
