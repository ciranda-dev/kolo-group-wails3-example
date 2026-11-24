package main

import (
	"bytes"
	"errors"
	"log"
	"slices"

	kolorelay "github.com/ciranda-dev/kolo-relay"
	kolosecure "github.com/ciranda-dev/kolo-secure"
	koloswarm "github.com/ciranda-dev/kolo-swarm"
)

// RelayState is one connection relay: its address, its public key in hex, and State: "waiting" until the app holds a
// place at it (it does once it is in a room), "ready", or "failed" with Error saying why: "wrong-key" (it answered with
// another key), "bad-key", "not-allowed", "full" or "unreachable".
type RelayState struct {
	Addr  string `json:"addr"`
	Key   string `json:"key"`
	State string `json:"state"`
	Error string `json:"error"`
}

// RelayStatus is the payload of the "group:relays" event: the connection relays of the settings, and where they came from
// ("-relay", "-relay-file", both, "KOLO_GROUP_RELAYS", or "" without relays).
type RelayStatus struct {
	Seq    uint64       `json:"seq"` // grows with every event; the window keeps the one with the highest
	From   string       `json:"from"`
	Relays []RelayState `json:"relays"`
}

// relayChanged records a RelayChanged event of the swarm, logs a new failure and tells the window.
func (d *device) relayChanged(ev koloswarm.Event) {
	state, why := "ready", ""
	switch {
	case ev.Err == nil:
	case errors.Is(ev.Err, koloswarm.ErrClosed), errors.Is(ev.Err, kolorelay.ErrReservationEnded):
		state = "waiting" // no room is joined, or the place's lifetime ended: kolo-swarm reserves again
	default:
		state, why = "failed", relayReason(ev.Err)
	}
	d.mu.Lock()
	for i, r := range d.set.relays {
		if !bytes.Equal(r.PublicKey, ev.PublicKey) {
			continue
		}
		if why != "" && d.relays[i].Error != why {
			log.Printf("relay %s: %v", r.Addr, ev.Err)
		}
		d.relays[i].State, d.relays[i].Error = state, why
	}
	d.relaySeq++ // events are sent after the lock is released, so they can arrive out of order
	st := RelayStatus{Seq: d.relaySeq, From: d.set.relaysFrom, Relays: slices.Clone(d.relays)}
	d.mu.Unlock()
	d.emit("group:relays", st)
}

// relayReason names why the app holds no place at a relay, for the window to say in its language.
func relayReason(err error) string {
	switch {
	case errors.Is(err, kolosecure.ErrUnexpectedPeer):
		return "wrong-key"
	case errors.Is(err, koloswarm.ErrInvalidConfig):
		return "bad-key"
	case errors.Is(err, kolorelay.ErrNotAllowed):
		return "not-allowed"
	case errors.Is(err, kolorelay.ErrLimit):
		return "full"
	}
	return "unreachable"
}
