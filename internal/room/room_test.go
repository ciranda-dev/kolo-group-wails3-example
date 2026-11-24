package room

import (
	"context"
	"errors"
	"net/netip"
	"strings"
	"testing"
)

// Every method that is not built returns ErrNotImplemented, which carries the package prefix.
func TestUnbuiltMethodsReturnErrNotImplemented(t *testing.T) {
	ctx := context.Background()
	if !strings.HasPrefix(ErrNotImplemented.Error(), "room:") {
		t.Fatalf("error %q lacks the room: prefix", ErrNotImplemented)
	}
	var n *Node
	var r *Room
	_, openErr := Open(ctx, &Config{})
	_, createErr := n.Create(ctx, "name", Policy{})
	_, welcomeErr := r.Add(ctx)
	_, sendErr := r.Send(ctx, nil, MessageID{})
	_, nextErr := r.Next(ctx)
	_, historyErr := r.History(ctx, MessageID{}, 1)
	_, keyErr := n.KeyPackage()
	for name, err := range map[string]error{
		"Open":       openErr,
		"Create":     createErr,
		"Add":        welcomeErr,
		"Send":       sendErr,
		"Next":       nextErr,
		"History":    historyErr,
		"KeyPackage": keyErr,
		"Close":      n.Close(),
		"Leave":      r.Leave(ctx),
		"SetPolicy":  r.SetPolicy(ctx, Policy{}),
	} {
		if !errors.Is(err, ErrNotImplemented) {
			t.Errorf("%s returned %v, want ErrNotImplemented", name, err)
		}
	}
	if n.Allow(nil, netip.AddrPort{}) || n.Rooms() != nil || r.Members() != nil {
		t.Error("an unbuilt node admits a peer or lists a room")
	}
}
