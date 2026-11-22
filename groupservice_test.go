package main

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestServiceReportsVersionAndNoRooms(t *testing.T) {
	s := NewGroupService(Config{DataDir: t.TempDir()})
	if s.Version() != version {
		t.Fatalf("version %q, want %q", s.Version(), version)
	}
	b, err := json.Marshal(s.Rooms())
	if err != nil || string(b) != "[]" {
		t.Fatalf("rooms as JSON: %s, %v (the window needs [] and not null)", b, err)
	}
}

func TestHeadlessIsNotBuilt(t *testing.T) {
	err := runHeadless(context.Background(), Config{DataDir: t.TempDir(), Headless: true})
	if !errors.Is(err, errHeadless) || !strings.HasPrefix(err.Error(), "kolo-group:") {
		t.Fatalf("got %v", err)
	}
}
