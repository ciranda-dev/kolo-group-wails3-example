package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	kolostore "github.com/ciranda-dev/kolo-store"
)

func msg(i int) MessageInfo {
	return MessageInfo{ID: fmt.Sprintf("%064x", i), Body: fmt.Sprint("m", i)}
}

func TestMessageStoreKeepsEachMessageOnceInOrder(t *testing.T) {
	b := kolostore.NewMemory()
	s := newMsgStore(b)
	for i := range 5 {
		m, kept, err := s.add("r1", msg(i))
		if err != nil || !kept || m.Seq != uint64(i+1) {
			t.Fatalf("add %d: %+v %v %v", i, m, kept, err)
		}
	}
	if m, kept, err := s.add("r1", msg(2)); err != nil || kept || m.Seq != 3 || m.Body != "m2" {
		t.Fatalf("the same message again: %+v %v %v", m, kept, err)
	}
	if m, _, _ := s.add("r2", msg(0)); m.Seq != 1 {
		t.Fatalf("each room counts from 1: %+v", m)
	}
	page, err := s.page("r1", 0, 3)
	if err != nil || len(page) != 3 || page[0].Body != "m2" || page[2].Body != "m4" {
		t.Fatalf("the newest three: %+v %v", page, err)
	}
	older, _ := s.page("r1", page[0].Seq, 10)
	if len(older) != 2 || older[0].Body != "m0" || older[1].Body != "m1" {
		t.Fatalf("before the first of the page: %+v", older)
	}
	if none, _ := s.page("r1", 1, 10); len(none) != 0 {
		t.Fatalf("before the first: %+v", none)
	}
	if other, _ := s.page("r", 0, 10); len(other) != 0 {
		t.Fatalf("a room whose id is a prefix of another: %+v", other)
	}

	// A new store over the same backend goes on counting where the old one stopped.
	again := newMsgStore(b)
	if m, kept, _ := again.add("r1", msg(9)); !kept || m.Seq != 6 {
		t.Fatalf("after a restart: %+v", m)
	}

	if err := s.stored("r1", msg(1).ID); err != nil {
		t.Fatal(err)
	}
	if err := s.stored("r1", strings.Repeat("f", 64)); err != nil {
		t.Fatalf("a message not kept: %v", err)
	}
	got, _ := s.page("r1", 0, 10)
	for _, m := range got {
		if m.Stored != (m.Body == "m1") {
			t.Fatalf("stored flags: %+v", got)
		}
	}

	if err := s.forget("r1"); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.page("r1", 0, 10); len(got) != 0 {
		t.Fatalf("after forget: %+v", got)
	}
	if got, _ := s.page("r2", 0, 10); len(got) != 1 {
		t.Fatalf("forget touched another room: %+v", got)
	}
	if m, kept, _ := s.add("r1", msg(0)); !kept || m.Seq != 1 {
		t.Fatalf("a room made again counts from 1: %+v", m)
	}
}

// The window reads lists as arrays: none of what the service returns is null, also for a room with nothing in it.
func TestNoPayloadIsNull(t *testing.T) {
	server := newServer(t)
	a := startApp(t, server, "", nil)
	r, err := a.svc.CreateRoom("Empty", PolicyInfo{})
	if err != nil {
		t.Fatal(err)
	}
	rooms, _ := a.svc.Rooms()
	msgs, _ := a.svc.Messages(r.ID, 0, 10)
	mem, _ := a.svc.Members(r.ID, 5, 10)
	hist, _ := a.svc.History(r.ID, "", 10)
	for name, v := range map[string]any{"start": a.info, "rooms": rooms, "room": r, "messages": msgs, "members": mem, "history": hist} {
		b, err := json.Marshal(v)
		if err != nil || strings.Contains(string(b), "null") {
			t.Errorf("%s: %s, %v", name, b, err)
		}
	}
	ev, _ := json.Marshal(EventInfo{})
	if strings.Contains(string(ev), "null") {
		t.Errorf("event: %s", ev)
	}
}
