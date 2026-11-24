package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ciranda-dev/kolo-pin"

	"github.com/ciranda-dev/kolo-group-wails3-example/internal/room"
)

// headlessConfig is the configuration of a headless device on the entry node.
func headlessConfig(t testing.TB, server string, mod func(*Config)) Config {
	t.Helper()
	cfg := Config{DataDir: filepath.Join(t.TempDir(), "data"), Bootstrap: server, Listen: "127.0.0.1:0", Headless: true}
	if mod != nil {
		mod(&cfg)
	}
	return cfg
}

// keyPackageIn returns the key package a headless device wrote to its output.
func keyPackageIn(t testing.TB, out *bytes.Buffer) string {
	t.Helper()
	for line := range strings.SplitSeq(out.String(), "\n") {
		if strings.HasPrefix(line, prefixKeyPackage) {
			return line
		}
	}
	t.Fatalf("no key package in %q", out.String())
	return ""
}

// pinnedEntries returns how many entries of the logs of the device the pin holds.
func pinnedEntries(t testing.TB, d *device, ps *pinServer) uint64 {
	t.Helper()
	ids, err := d.logs.List()
	if err != nil {
		t.Fatal(err)
	}
	var total uint64
	for _, id := range ids {
		if _, replica, err := d.logs.Owner(id); err != nil || replica {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		info, err := pin.Status(ctx, d.key, ps.peer, id)
		cancel()
		if err == nil {
			total += info.Entries
		}
	}
	return total
}

// A device with no window joins a room from a welcome file, becomes an admin, writes the room's segments and pushes them to
// the pin, as the other admin does. A member that joins later, with both admins down, reads what was said before from the pin.
func TestHeadlessAdminKeepsTheRoomForTheOffline(t *testing.T) {
	server := newServer(t)
	ps := newPin(t)
	pins := func(c *Config) { c.Pins = []string{ps.arg()} }
	a := startApp(t, server, "", pins)
	b := startApp(t, server, "", pins)
	welcomeFile := filepath.Join(t.TempDir(), "welcome.txt")
	var out bytes.Buffer
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h, err := startHeadless(ctx, headlessConfig(t, server.Addr().String(), func(c *Config) {
		c.Pins, c.WelcomeFile = []string{ps.arg()}, welcomeFile
	}), fastTune, &out)
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()

	created, err := a.svc.CreateRoom("Always on", PolicyInfo{})
	if err != nil {
		t.Fatal(err)
	}
	welcome, err := a.svc.AddMembers(created.ID, []string{keyPackageIn(t, &out)})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(welcomeFile, []byte(welcome+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	waitFor(t, wait, "the headless device to join", func() bool { return len(h.rooms()) == 1 })
	promote(t, a, created.ID, hexKey(h.key.PublicKey()), func() bool { return h.rooms()[0].Admin })

	for _, body := range []string{"before you came", "and again"} {
		if _, err := a.svc.Send(created.ID, body, ""); err != nil {
			t.Fatal(err)
		}
	}
	waitFor(t, wait, "the headless device to hold the messages", func() bool {
		got, _ := h.msgs.page(created.ID, 0, 10)
		return len(got) == 2
	})
	kp, err := b.svc.KeyPackage()
	if err != nil {
		t.Fatal(err)
	}
	welcomeB, err := a.svc.AddMembers(created.ID, []string{kp})
	if err != nil {
		t.Fatal(err)
	}
	// The pin holds the logs of both admins when what it holds stops growing: each push waits PinGap.
	var held uint64
	steady := 0
	waitFor(t, wait, "the admins to push their logs to the pin", func() bool {
		time.Sleep(time.Second)
		if n := pinnedEntries(t, h, ps) + pinnedEntries(t, a.svc.d, ps); n > 0 && n == held {
			steady++
		} else {
			held, steady = n, 0
		}
		return steady >= 3
	})
	if pinnedEntries(t, h, ps) == 0 {
		t.Fatal("the headless device pushed nothing to the pin")
	}
	if st := mustStatus(t, h, created.ID); st.PinError != nil {
		t.Fatalf("the pin: %v", st.PinError)
	}
	a.stop()
	h.Close()
	if _, err := b.svc.JoinRoom(welcomeB); err != nil {
		t.Fatal(err)
	}
	waitFor(t, wait, "b to read from the pin what was said before it joined", func() bool {
		page, err := b.svc.History(created.ID, "", 10)
		return err == nil && len(page.Messages) == 2 && page.Messages[0].Body == "before you came" && page.Messages[1].Body == "and again"
	})
}

func mustStatus(t testing.TB, d *device, id string) room.Status {
	t.Helper()
	r, err := d.room(id)
	if err != nil {
		t.Fatal(err)
	}
	return r.Status()
}

// A welcome file is tried once: a damaged text and a welcome that was joined are not tried again, also after a restart.
func TestWelcomeFileIsJoinedOnce(t *testing.T) {
	server := newServer(t)
	a := startApp(t, server, "", nil)
	welcomeFile := filepath.Join(t.TempDir(), "welcome.txt")
	cfg := headlessConfig(t, server.Addr().String(), func(c *Config) { c.WelcomeFile = welcomeFile })
	var out bytes.Buffer
	h, err := startHeadless(context.Background(), cfg, fastTune, &out)
	if err != nil {
		t.Fatal(err)
	}
	created, _ := a.svc.CreateRoom("Once", PolicyInfo{})
	welcome, err := a.svc.AddMembers(created.ID, []string{keyPackageIn(t, &out)})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(welcomeFile, []byte("kolo-group-welcome:not-a-welcome"), 0o600); err != nil {
		t.Fatal(err)
	}
	time.Sleep(welcomePoll + time.Second)
	if len(h.rooms()) != 0 || !h.usedWelcome(sumOf("kolo-group-welcome:not-a-welcome")) {
		t.Fatalf("a damaged welcome joined a room or was left to be tried again: %+v", h.rooms())
	}
	if err := os.WriteFile(welcomeFile, []byte(welcome), 0o600); err != nil {
		t.Fatal(err)
	}
	waitFor(t, wait, "the welcome to be joined", func() bool { return len(h.rooms()) == 1 })
	h.inflight.Wait()
	h.Close()

	out.Reset()
	h, err = startHeadless(context.Background(), cfg, fastTune, &out)
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	if len(h.rooms()) != 1 || strings.Contains(out.String(), prefixKeyPackage) {
		t.Fatalf("after a restart: %+v, output %q", h.rooms(), out.String())
	}
	time.Sleep(welcomePoll + time.Second)
	if len(h.rooms()) != 1 || !h.usedWelcome(sumOf(welcome)) {
		t.Fatalf("the used welcome was joined again or not recorded: %+v", h.rooms())
	}
}

// The window and -headless run one device over one folder: keys, rooms and messages are the same in both.
func TestWindowAndHeadlessShareTheDataFolder(t *testing.T) {
	server := newServer(t)
	a := startApp(t, server, "", nil)
	b := startApp(t, server, "", nil)
	id := pair(t, a, b, "Shared", PolicyInfo{})
	if _, err := a.svc.Send(id, "from the window", ""); err != nil {
		t.Fatal(err)
	}
	waitFor(t, wait, "the message at b", func() bool { return b.has(id, "from the window") })
	a.stop()

	var out bytes.Buffer
	h, err := startHeadless(context.Background(), Config{DataDir: a.dir, Bootstrap: server.Addr().String(), Listen: "127.0.0.1:0", Headless: true}, fastTune, &out)
	if err != nil {
		t.Fatal(err)
	}
	if hexKey(h.key.PublicKey()) != a.info.Device || hexKey(h.person.PublicKey()) != a.info.Person {
		t.Fatal("the headless device has other keys than the window's")
	}
	rs := h.rooms()
	if len(rs) != 1 || rs[0].ID != id || !rs[0].Admin || strings.Contains(out.String(), prefixKeyPackage) {
		t.Fatalf("rooms in headless mode: %+v, output %q", rs, out.String())
	}
	if got, _ := h.msgs.page(id, 0, 10); len(got) != 1 || got[0].Body != "from the window" {
		t.Fatalf("messages in headless mode: %+v", got)
	}
	if _, err := b.svc.Send(id, "to the headless device", ""); err != nil {
		t.Fatal(err)
	}
	waitFor(t, wait, "b's message to reach the headless device", func() bool {
		got, _ := h.msgs.page(id, 0, 10)
		return len(got) == 2
	})
	h.Close()
	a.start()
	if got, _ := a.svc.Messages(id, 0, 10); len(got) != 2 || got[0].Body != "from the window" || got[1].Body != "to the headless device" {
		t.Fatalf("the window after headless mode: %+v", got)
	}
}
