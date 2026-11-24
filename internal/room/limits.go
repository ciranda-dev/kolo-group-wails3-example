package room

import (
	"time"

	kolomls "github.com/ciranda-dev/kolo-mls"
)

// Limits holds the configurable protective bounds of a node; a zero field takes its default, and a negative one is refused.
// Each default has its source in docs/limits.md.
type Limits struct {
	MaxMessageSize int           // largest gossip envelope of a room topic, in bytes
	Rate           float64       // messages per second one device may send in a room
	Burst          int           // messages one device may send at once
	MaxParents     int           // parents one message may name, its own previous message included
	ParentHold     time.Duration // how long a message waits for a parent that has not arrived
	MaxHeld        int           // messages waiting for a parent
	MaxKnown       int           // message ids remembered for ordering
	EventQueue     int           // events waiting for Next
	PastEpoch      time.Duration // how long messages of the previous epoch are still accepted
	MaxSkew        time.Duration // how far the time inside a message may differ from its envelope's
	MaxTreeBytes   int           // largest ratchet tree a join accepts
}

const (
	defaultMaxMessageSize = 1 << 20
	defaultRate           = 10
	defaultMaxParents     = 16
	defaultParentHold     = 5 * time.Second
	defaultMaxHeld        = 8333
	defaultMaxKnown       = 200000
	defaultEventQueue     = 1024
	defaultPastEpoch      = 10 * time.Second
	defaultMaxSkew        = time.Minute
)

func (l Limits) withDefaults() (Limits, error) {
	if l.MaxMessageSize < 0 || l.Rate < 0 || l.Burst < 0 || l.MaxParents < 0 || l.ParentHold < 0 || l.MaxHeld < 0 ||
		l.MaxKnown < 0 || l.EventQueue < 0 || l.PastEpoch < 0 || l.MaxSkew < 0 || l.MaxTreeBytes < 0 {
		return l, errInvalid("a negative limit")
	}
	if l.MaxMessageSize == 0 {
		l.MaxMessageSize = defaultMaxMessageSize
	}
	if l.Rate == 0 {
		l.Rate = defaultRate
	}
	if l.Burst == 0 {
		l.Burst = max(1, int(l.Rate))
	}
	if l.MaxParents == 0 {
		l.MaxParents = defaultMaxParents
	}
	if l.ParentHold == 0 {
		l.ParentHold = defaultParentHold
	}
	if l.MaxHeld == 0 {
		l.MaxHeld = defaultMaxHeld
	}
	if l.MaxKnown == 0 {
		l.MaxKnown = defaultMaxKnown
	}
	if l.EventQueue == 0 {
		l.EventQueue = defaultEventQueue
	}
	if l.PastEpoch == 0 {
		l.PastEpoch = defaultPastEpoch
	}
	if l.MaxSkew == 0 {
		l.MaxSkew = defaultMaxSkew
	}
	if l.MaxTreeBytes == 0 {
		l.MaxTreeBytes = kolomls.DefaultMaxTreeBytes
	}
	return l, nil
}
