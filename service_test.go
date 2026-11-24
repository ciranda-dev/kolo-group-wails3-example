package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	koloid "github.com/ciranda-dev/kolo-id"

	"github.com/ciranda-dev/kolo-group-wails3-example/internal/room"
)

const wait = time.Minute

func mustRooms(t testing.TB, a *testApp) []RoomInfo {
	t.Helper()
	rs, err := a.svc.Rooms()
	if err != nil {
		t.Fatal(err)
	}
	return rs
}

func roomOf(t testing.TB, a *testApp, id string) RoomInfo {
	t.Helper()
	for _, r := range mustRooms(t, a) {
		if r.ID == id {
			return r
		}
	}
	t.Fatalf("no room %s", id)
	return RoomInfo{}
}

func members(t testing.TB, a *testApp, id string) MemberPage {
	t.Helper()
	p, err := a.svc.Members(id, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestTwoDevicesTalkAndTheWindowKeepsWhatItWasTold(t *testing.T) {
	server := newServer(t)
	a, b := startApp(t, server, "", nil), startApp(t, server, "", nil)
	id := pair(t, a, b, "Family", PolicyInfo{})
	first, err := a.svc.Send(id, "hello", "")
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, wait, "hello at b", func() bool { return b.has(id, "hello") })
	if _, err := b.svc.Send(id, "hi back", first); err != nil {
		t.Fatal(err)
	}
	waitFor(t, wait, "the reply at a", func() bool { return a.has(id, "hi back") })
	for _, m := range a.messages(id) {
		if m.Body == "hi back" && (m.ReplyTo != first || m.Mine) {
			t.Fatalf("the reply is %+v, want a reply to %s from another person", m, first)
		}
	}
	waitFor(t, wait, "b's message stored", func() bool {
		got, _ := b.svc.Messages(id, 0, 10)
		return len(got) == 2 && got[1].Stored
	})

	a.restart()
	got, err := a.svc.Messages(id, 0, 50)
	if err != nil || len(got) != 2 || got[0].Body != "hello" || got[1].Body != "hi back" || got[1].ReplyTo != first {
		t.Fatalf("after a restart the window reads %+v, %v", got, err)
	}
	if rs := mustRooms(t, a); len(rs) != 1 || rs[0].Name != "Family" || !rs[0].Admin || rs[0].Members != 2 {
		t.Fatalf("rooms after a restart: %+v", rs)
	}
	older, err := a.svc.Messages(id, got[1].Seq, 50)
	if err != nil || len(older) != 1 || older[0].Body != "hello" {
		t.Fatalf("the page before the second message: %+v, %v", older, err)
	}
}

func TestPostersAndPolicyChanges(t *testing.T) {
	server := newServer(t)
	a, b := startApp(t, server, "", nil), startApp(t, server, "", nil)
	id := pair(t, a, b, "News", PolicyInfo{History: "none", Posters: "admins"})
	if r := roomOf(t, b, id); r.CanPost || r.Admin || r.Policy.Posters != "admins" || r.Policy.History != "none" {
		t.Fatalf("b's room: %+v", r)
	}
	if _, err := b.svc.Send(id, "may I?", ""); !errors.Is(err, room.ErrNotPoster) {
		t.Fatalf("a read-only member sent: %v", err)
	}
	if _, err := a.svc.Send(id, "news", ""); err != nil {
		t.Fatal(err)
	}
	waitFor(t, wait, "the news at b", func() bool { return b.has(id, "news") })
	if err := a.svc.SetPolicy(id, PolicyInfo{History: "all", Posters: "all"}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, wait, "b may post", func() bool { return roomOf(t, b, id).CanPost })
	if _, err := b.svc.Send(id, "now I may", ""); err != nil {
		t.Fatal(err)
	}
	waitFor(t, wait, "b's message at a", func() bool { return a.has(id, "now I may") })

	// Chosen persons: the person of b only.
	bp := members(t, b, id).Items
	if len(bp) != 1 || !bp[0].Mine {
		t.Fatalf("members seen by b: %+v", members(t, b, id))
	}
	if err := a.svc.SetPolicy(id, PolicyInfo{History: "since", Posters: "chosen", Persons: []string{bp[0].Person}}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, wait, "the chosen poster", func() bool {
		r := roomOf(t, a, id)
		return r.Policy.Posters == "chosen" && r.Policy.History == "since" && r.Policy.Since > 1 && !r.CanPost
	})
	if r := roomOf(t, b, id); !r.CanPost {
		t.Fatalf("the chosen person cannot post: %+v", r)
	}
	if m := members(t, a, id); !m.Items[0].Poster || m.Admins[0].Poster {
		t.Fatalf("posters in the member list: %+v", m)
	}
}

func TestHistoryScrollsBack(t *testing.T) {
	server := newServer(t)
	a, b, c := startApp(t, server, "", nil), startApp(t, server, "", nil), startApp(t, server, "", nil)
	id := pair(t, a, b, "Old", PolicyInfo{})
	for _, body := range []string{"m0", "m1", "m2", "m3", "m4"} {
		if _, err := a.svc.Send(id, body, ""); err != nil {
			t.Fatal(err)
		}
	}
	waitFor(t, wait, "m4 at b", func() bool { return b.has(id, "m4") })
	invite(t, a, c, id)

	page, err := c.svc.History(id, "", 3)
	if err != nil || len(page.Messages) != 3 || page.Done {
		t.Fatalf("first page: %+v, %v", page, err)
	}
	var got []string
	for _, m := range page.Messages {
		got = append(got, m.Body)
		if m.Seq != 0 || m.Error != "" {
			t.Fatalf("a message of the history: %+v", m)
		}
	}
	rest, err := c.svc.History(id, page.Messages[0].ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	var older []string
	for _, m := range rest.Messages {
		older = append(older, m.Body)
	}
	got = append(older, got...)
	if strings.Join(got, ",") != "m0,m1,m2,m3,m4" {
		t.Fatalf("history: %v", got)
	}
	end, err := c.svc.History(id, rest.Messages[0].ID, 10)
	if err != nil || !end.Done || len(end.Messages) != 0 {
		t.Fatalf("past the oldest: %+v, %v", end, err)
	}
	if live, _ := c.svc.Messages(id, 0, 10); len(live) != 0 {
		t.Fatalf("a message before the join shows as live: %+v", live)
	}
}

func TestPromoteRemoveAndLeave(t *testing.T) {
	server := newServer(t)
	a, b, c := startApp(t, server, "", nil), startApp(t, server, "", nil), startApp(t, server, "", nil)
	id := pair(t, a, b, "Team", PolicyInfo{})
	invite(t, a, c, id)
	waitFor(t, wait, "three devices at a", func() bool { return members(t, a, id).Total == 3 })

	promote(t, a, id, b.info.Device, func() bool { return roomOf(t, b, id).Admin })
	if got := members(t, a, id); len(got.Admins) != 2 || len(got.Items) != 1 {
		t.Fatalf("members after the promotion: %+v", got)
	}

	if err := a.svc.Remove(id, c.info.Person); err != nil {
		t.Fatal(err)
	}
	waitFor(t, wait, "c learns it was removed", func() bool { return len(mustRooms(t, c)) == 0 })
	waitFor(t, wait, "a drops c", func() bool { return members(t, a, id).Total == 2 })
	if _, err := c.svc.Messages(id, 0, 10); err == nil {
		t.Fatal("a removed room still answers")
	}

	if err := b.svc.Leave(id); err == nil {
		t.Fatal("an admin left")
	}
	if err := a.svc.Remove(id, "xyz"); err == nil {
		t.Fatal("a bad key was accepted")
	}
}

func TestMemberLeaves(t *testing.T) {
	server := newServer(t)
	a, b := startApp(t, server, "", nil), startApp(t, server, "", nil)
	id := pair(t, a, b, "Bye", PolicyInfo{})
	if _, err := b.svc.Send(id, "last words", ""); err != nil {
		t.Fatal(err)
	}
	waitFor(t, wait, "the words at b", func() bool { return b.has(id, "last words") })
	ctx := time.AfterFunc(2*time.Minute, b.svc.CancelAction)
	defer ctx.Stop()
	if err := b.svc.Leave(id); err != nil {
		t.Fatal(err)
	}
	if rs := mustRooms(t, b); len(rs) != 0 {
		t.Fatalf("b still has %+v", rs)
	}
	waitFor(t, wait, "a drops b", func() bool { return members(t, a, id).Total == 1 })
	if got, err := b.svc.Messages(id, 0, 10); err == nil {
		t.Fatalf("the messages of a room left are kept: %+v", got)
	}
}

func TestReadLinkRoom(t *testing.T) {
	server := newServer(t)
	a, r := startApp(t, server, "", nil), startApp(t, server, "", nil)
	created, err := a.svc.CreateRoom("Broadcast", PolicyInfo{Posters: "admins", ReadLink: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.svc.CreateRoom("Bad", PolicyInfo{ReadLink: true}); err == nil {
		t.Fatal("a read link with every member posting was accepted")
	}
	link, err := a.svc.RoomLink(created.ID)
	if err != nil || !strings.HasPrefix(link, prefixLink) {
		t.Fatalf("link %q: %v", link, err)
	}
	joined, err := r.svc.JoinLink(link)
	if err != nil {
		t.Fatal(err)
	}
	if !joined.Reader || joined.CanPost || joined.Name != "Broadcast" || joined.ID != created.ID {
		t.Fatalf("the reader's room: %+v", joined)
	}
	if _, err := a.svc.Send(created.ID, "to all readers", ""); err != nil {
		t.Fatal(err)
	}
	waitFor(t, wait, "the broadcast at the reader", func() bool { return r.has(created.ID, "to all readers") })
	if _, err := r.svc.Send(created.ID, "hello?", ""); !errors.Is(err, room.ErrNotPoster) {
		t.Fatalf("a reader sent: %v", err)
	}
	if kept, _ := r.svc.Messages(created.ID, 0, 10); len(kept) != 0 {
		t.Fatalf("a reader keeps messages across a restart: %+v", kept)
	}
	r.restart()
	if rs := mustRooms(t, r); len(rs) != 0 {
		t.Fatalf("a reader room survived a restart: %+v", rs)
	}
	if _, err := r.svc.JoinLink(prefixWelcome + "AAAA"); err == nil || !strings.HasPrefix(err.Error(), "kolo-group:") {
		t.Fatalf("a welcome as a link: %v", err)
	}
	if _, err := a.svc.RoomLink(created.ID); err != nil {
		t.Fatal(err)
	}
	plain, err := a.svc.CreateRoom("Plain", PolicyInfo{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.svc.RoomLink(plain.ID); !errors.Is(err, room.ErrNoLink) {
		t.Fatalf("the link of a room without one: %v", err)
	}
}

func TestStatusAndNetwork(t *testing.T) {
	server := newServer(t)
	a, b := startApp(t, server, "", nil), startApp(t, server, "", nil)
	id := pair(t, a, b, "Status", PolicyInfo{})
	waitFor(t, wait, "b sees an admin online", func() bool {
		st, err := b.svc.RoomStatus(id)
		return err == nil && st.Online && st.Peers >= 1 && st.AdminsOnline >= 1 && st.Epoch >= 1 && !st.CatchingUp && st.PinError == ""
	})
	if a.info.Network.Mode != "own" || len(a.info.Network.Nodes) != 1 || len(a.info.Pins) != 0 || len(a.info.Relays) != 0 || a.info.Person == a.info.Device {
		t.Fatalf("app info: %+v", a.info)
	}
	if _, err := a.svc.RoomStatus("zz"); err == nil {
		t.Fatal("a bad id was accepted")
	}
}

// The devices connected that a room's status counts are the room's own, not every connection of the device.
func TestStatusCountsTheDevicesOfTheRoom(t *testing.T) {
	server := newServer(t)
	a, b, c := startApp(t, server, "", nil), startApp(t, server, "", nil), startApp(t, server, "", nil)
	one := pair(t, a, b, "One", PolicyInfo{})
	two := pair(t, a, c, "Two", PolicyInfo{})
	peers := func(id string) int {
		st, err := a.svc.RoomStatus(id)
		if err != nil {
			t.Fatal(err)
		}
		return st.Peers
	}
	waitFor(t, wait, "a connected to b and c", func() bool {
		st, err := b.svc.RoomStatus(one)
		st2, err2 := c.svc.RoomStatus(two)
		return err == nil && err2 == nil && st.Peers == 1 && st2.Peers == 1
	})
	time.Sleep(time.Second)
	if p1, p2 := peers(one), peers(two); p1 != 1 || p2 != 1 {
		t.Fatalf("devices connected: %d in One and %d in Two, want 1 and 1", p1, p2)
	}
}

// A device that is offline when an admin promotes it gets the offer once it is back.
func TestPromoteADeviceThatWasOffline(t *testing.T) {
	server := newServer(t)
	a, b := startApp(t, server, "", nil), startApp(t, server, "", nil)
	id := pair(t, a, b, "Later", PolicyInfo{})
	b.stop()
	done := make(chan error, 1)
	go func() { done <- a.svc.Promote(id, b.info.Device) }()
	time.Sleep(3 * time.Second)
	b.start()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(wait):
		a.svc.CancelAction()
		<-done
		t.Fatal("Promote did not reach the device once it was back")
	}
	waitFor(t, wait, "b to be an admin", func() bool { return roomOf(t, b, id).Admin })
}

// The pins and relays the app was started with are what Settings shows, with keys in hex.
func TestSettingsShowPinsAndRelays(t *testing.T) {
	server := newServer(t)
	ps := newPin(t)
	relay, err := koloid.Generate()
	if err != nil {
		t.Fatal(err)
	}
	a := startApp(t, server, "", func(c *Config) {
		c.Pins = []string{ps.arg()}
		c.Relays = []string{"127.0.0.1:7448@" + hexKey(relay.PublicKey())}
	})
	if len(a.info.Pins) != 1 || a.info.Pins[0].Addr != ps.peer.Addr || a.info.Pins[0].Key != hexKey(ps.peer.ID) {
		t.Fatalf("pins: %+v", a.info.Pins)
	}
	if len(a.info.Relays) != 1 || a.info.Relays[0].State != "waiting" || a.info.Relays[0].Key != hexKey(relay.PublicKey()) || a.info.From != "-relay" {
		t.Fatalf("relays: %+v from %q", a.info.Relays, a.info.From)
	}
}

func TestOneActionAtATimeAndCancel(t *testing.T) {
	server := newServer(t)
	a, b := startApp(t, server, "", nil), startApp(t, server, "", nil)
	id := pair(t, a, b, "Away", PolicyInfo{})
	a.stop() // no admin is left to answer b
	done := make(chan error, 1)
	go func() { done <- b.svc.Leave(id) }()
	waitFor(t, wait, "the leave to wait", func() bool {
		b.svc.mu.Lock()
		defer b.svc.mu.Unlock()
		return b.svc.cancel != nil
	})
	if err := b.svc.Promote(id, b.info.Device); !errors.Is(err, errBusy) {
		t.Fatalf("a second action while the leave waits: %v", err)
	}
	select {
	case err := <-done:
		t.Fatalf("the leave ended by itself: %v", err)
	default:
	}
	b.svc.CancelAction()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("the cancelled leave: %v", err)
		}
	case <-time.After(wait):
		t.Fatal("CancelAction did not end the leave")
	}
	waitFor(t, wait, "the room to stop here", func() bool { return len(mustRooms(t, b)) == 0 })
}

func TestTextsAndArgumentsAreChecked(t *testing.T) {
	server := newServer(t)
	a := startApp(t, server, "", nil)
	if _, err := a.svc.CreateRoom("  ", PolicyInfo{}); err == nil {
		t.Fatal("a room with no name")
	}
	for _, p := range []PolicyInfo{{History: "never"}, {Posters: "nobody"}, {Posters: "chosen"}, {Posters: "chosen", Persons: []string{"12"}}} {
		if _, err := a.svc.CreateRoom("x", p); err == nil || !strings.HasPrefix(err.Error(), "kolo-group:") {
			t.Errorf("policy %+v: %v", p, err)
		}
	}
	r, err := a.svc.CreateRoom("Args", PolicyInfo{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.svc.JoinRoom(prefixKeyPackage + "AAAA"); err == nil || !strings.Contains(err.Error(), "not a welcome text") {
		t.Errorf("a key package as a welcome: %v", err)
	}
	if _, err := a.svc.JoinRoom(prefixWelcome + "!!"); err == nil || !strings.Contains(err.Error(), "damaged") {
		t.Errorf("a damaged welcome: %v", err)
	}
	if _, err := a.svc.AddMembers(r.ID, []string{" "}); err == nil {
		t.Error("an empty list of key packages")
	}
	if _, err := a.svc.AddMembers(r.ID, []string{"nonsense"}); err == nil {
		t.Error("a key package of no kind")
	}
	if _, err := a.svc.Send(r.ID, "  ", ""); err == nil {
		t.Error("an empty message")
	}
	if _, err := a.svc.Send(r.ID, "x", "nope"); err == nil {
		t.Error("a bad reply id")
	}
	if _, err := a.svc.History(r.ID, "nope", 5); err == nil {
		t.Error("a bad history cursor")
	}
	if _, err := a.svc.Send(strings.Repeat("0", 64), "x", ""); !errors.Is(err, room.ErrUnknownRoom) {
		t.Errorf("an unknown room: %v", err)
	}
	if _, err := a.svc.Start(); !errors.Is(err, errStarted) {
		t.Errorf("a second Start: %v", err)
	}
	s := NewGroupService(Config{DataDir: t.TempDir()})
	if _, err := s.Rooms(); !errors.Is(err, errNotYet) {
		t.Errorf("Rooms before Start: %v", err)
	}
}

func TestMembersPage(t *testing.T) {
	server := newServer(t)
	a, b, c := startApp(t, server, "", nil), startApp(t, server, "", nil), startApp(t, server, "", nil)
	id := pair(t, a, b, "Page", PolicyInfo{})
	invite(t, a, c, id)
	waitFor(t, wait, "three devices", func() bool { return members(t, a, id).Total == 3 })
	first, err := a.svc.Members(id, 0, 1)
	if err != nil || first.Total != 3 || len(first.Admins) != 1 || len(first.Items) != 1 || !first.Admins[0].Me || first.Admins[0].Role != "settler" {
		t.Fatalf("first page: %+v, %v", first, err)
	}
	second, _ := a.svc.Members(id, 1, 1)
	third, _ := a.svc.Members(id, 2, 1)
	if len(second.Items) != 1 || len(third.Items) != 0 || second.Items[0].Device == first.Items[0].Device {
		t.Fatalf("pages: %+v %+v", second, third)
	}
}
