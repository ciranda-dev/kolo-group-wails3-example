package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	kolobraid "github.com/ciranda-dev/kolo-braid"
	kolodht "github.com/ciranda-dev/kolo-dht"
	kologossip "github.com/ciranda-dev/kolo-gossip"
	koloid "github.com/ciranda-dev/kolo-id"
	"github.com/ciranda-dev/kolo-pin"
	kolosecure "github.com/ciranda-dev/kolo-secure"
	koloswarm "github.com/ciranda-dev/kolo-swarm"
	kolotransport "github.com/ciranda-dev/kolo-transport"

	"github.com/ciranda-dev/kolo-group-wails3-example/internal/room"
)

func waitFor(t testing.TB, d time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out after %v waiting for %s", d, what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// newServer starts a kolo-dht entry node on loopback with per-IP limits raised: every test device is 127.0.0.1.
func newServer(t testing.TB) *kolodht.Node {
	t.Helper()
	id, err := koloid.Generate()
	if err != nil {
		t.Fatal(err)
	}
	n, err := kolodht.New(context.Background(), "127.0.0.1:0", id, &kolodht.Config{
		Server: true, RequestTimeout: 300 * time.Millisecond, MaxRequestsPerIP: 5000, MaxRequestsPerPrefix: 10000, MaxRequests: 20000,
		MaxRecordsPerIP: 2048, MaxRecordsPerTopic: 2048, MaxRelays: 256,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { n.Close() })
	return n
}

// fastTune gives a test device the smallest pacing the libraries allow, so rooms settle in milliseconds.
func fastTune(sc *koloswarm.Config, gc *kologossip.Config, rc *room.Config) {
	sc.DHT = &kolodht.Config{RequestTimeout: 300 * time.Millisecond, PunchTimeout: time.Second,
		Secure: &kolosecure.Config{Transport: &kolotransport.Config{MaxConnsPerIP: 256}}}
	sc.MaxConnecting, sc.MaxHandshakes, sc.MaxHandshakesPerIP = 4, 128, 128
	sc.HelloTimeout, sc.RetryMin, sc.RetryMax = 2*time.Second, 100*time.Millisecond, time.Second
	gc.HeartbeatInterval = 100 * time.Millisecond
	rc.BraidLimits = &kolobraid.Limits{MaxTail: 64, PulseGap: 10 * time.Millisecond, PivotWait: 100 * time.Millisecond, ConfirmWait: time.Second, AnnounceGap: 10 * time.Millisecond}
	rc.Limits.PinGap, rc.Limits.PinWait, rc.Limits.RepublishAfter = 50*time.Millisecond, 300*time.Millisecond, time.Second
}

// testApp is a GroupService on loopback that records the events it sends the window.
type testApp struct {
	t    testing.TB
	svc  *GroupService
	dir  string
	cfg  Config
	info AppInfo

	mu     sync.Mutex
	events []EventInfo
}

// startApp starts a service over dir (a new folder when empty) on the entry node.
func startApp(t testing.TB, server *kolodht.Node, dir string, mod func(*Config)) *testApp {
	t.Helper()
	if dir == "" {
		dir = filepath.Join(t.TempDir(), "data")
	}
	cfg := Config{DataDir: dir, Bootstrap: server.Addr().String(), Listen: "127.0.0.1:0"}
	if mod != nil {
		mod(&cfg)
	}
	a := &testApp{t: t, dir: dir, cfg: cfg}
	a.start()
	t.Cleanup(a.stop)
	return a
}

func (a *testApp) start() {
	a.t.Helper()
	a.svc = NewGroupService(a.cfg)
	a.svc.tune = fastTune
	a.svc.emit = func(name string, data any) {
		if ev, ok := data.(EventInfo); ok && name == "group:event" {
			a.mu.Lock()
			a.events = append(a.events, ev)
			a.mu.Unlock()
		}
	}
	info, err := a.svc.Start()
	if err != nil {
		a.t.Fatal(err)
	}
	a.info = info
}

func (a *testApp) stop() { a.svc.ServiceShutdown() }

// restart stops the service and starts it again over the same folder, as a process that quit and ran again.
func (a *testApp) restart() {
	a.stop()
	a.mu.Lock()
	a.events = nil
	a.mu.Unlock()
	a.start()
}

// messages returns the bodies of the messages of a room the window was told of, in the order it was told.
func (a *testApp) messages(roomID string) []MessageInfo {
	a.mu.Lock()
	defer a.mu.Unlock()
	var out []MessageInfo
	for _, ev := range a.events {
		if ev.Room == roomID && ev.Kind == "message" && ev.Message.Error == "" {
			out = append(out, ev.Message)
		}
	}
	return out
}

func (a *testApp) has(roomID, body string) bool {
	for _, m := range a.messages(roomID) {
		if m.Body == body {
			return true
		}
	}
	return false
}

func (a *testApp) kinds(roomID string) []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	var out []string
	for _, ev := range a.events {
		if ev.Room == roomID {
			out = append(out, ev.Kind)
		}
	}
	return out
}

// pair makes a room on a, adds b to it and returns the room's id once b has joined.
func pair(t testing.TB, a, b *testApp, name string, policy PolicyInfo) string {
	t.Helper()
	r, err := a.svc.CreateRoom(name, policy)
	if err != nil {
		t.Fatal(err)
	}
	invite(t, a, b, r.ID)
	return r.ID
}

// invite adds b to the room of a through a key package and a welcome, as the window does.
func invite(t testing.TB, a, b *testApp, roomID string) {
	t.Helper()
	kp, err := b.svc.KeyPackage()
	if err != nil {
		t.Fatal(err)
	}
	welcome, err := a.svc.AddMembers(roomID, []string{kp})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.svc.JoinRoom(welcome); err != nil {
		t.Fatal(err)
	}
}

type pinServer struct {
	peer pin.Peer
	stop context.CancelFunc
	done chan error
}

// newPin runs a kolo-pin on loopback over a temporary folder.
func newPin(t testing.TB) *pinServer {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "pin")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	id, err := pin.LoadIdentity(dir)
	if err != nil {
		t.Fatal(err)
	}
	ln, err := kolosecure.Listen(context.Background(), "127.0.0.1:0", id, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	ps := &pinServer{peer: pin.Peer{ID: id.PublicKey(), Addr: ln.Addr().String()}, stop: cancel, done: make(chan error, 1)}
	go func() { ps.done <- pin.Serve(ctx, ln, pin.Options{Dir: dir}) }()
	t.Cleanup(func() {
		ps.stop()
		select {
		case <-ps.done:
		case <-time.After(10 * time.Second):
		}
	})
	return ps
}

// pinArg is the pin as a -pin flag writes it.
func (ps *pinServer) arg() string { return fmt.Sprintf("%s@%x", ps.peer.Addr, []byte(ps.peer.ID)) }
