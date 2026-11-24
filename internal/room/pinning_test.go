package room

import (
	"context"
	"testing"
	"time"

	"github.com/ciranda-dev/kolo-pin"
)

// A segment that is being pushed is not forgotten, however few segments the device keeps: the pins that do not answer hold the
// push, and the segments that were closed before it began are still on the device when it ends.
func TestSegmentBeingPushedIsNotForgotten(t *testing.T) {
	ps := []*pinServer{newPin(t), newPin(t)}
	peers := []pin.Peer{ps[0].peer, ps[1].peer}
	lim := Limits{KeepSegments: 1, PinGap: 2 * time.Second, PinWait: 100 * time.Millisecond, CatchUpWait: 10 * time.Second}
	gr := newGroupPins(t, 2, lim, peers, nil)
	gr.waitMesh(1)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	ps[0].down()
	ps[1].down()
	admin := gr.rooms[0]
	gr.advance(t, ctx, 4, "k")
	own := func() (n int) {
		for _, l := range segments(admin) {
			if l.own && l.closed {
				n++
			}
		}
		return n
	}
	if n := own(); n < 3 {
		t.Fatalf("%d closed segments of this admin before the push", n)
	}
	time.Sleep(lim.PinGap + time.Second)
	if n := own(); n < 3 {
		t.Fatalf("%d closed segments of this admin after the push began: the others were forgotten while unpushed", n)
	}
}

// An admin that keeps one closed segment still pushes every log it writes to both pins: a segment is not forgotten while
// it is being pushed.
func TestKeepOneSegmentStillPushesEveryLogToTwoPins(t *testing.T) {
	ps := []*pinServer{newPin(t), newPin(t)}
	peers := []pin.Peer{ps[0].peer, ps[1].peer}
	gr := newGroupPins(t, 2, Limits{KeepSegments: 1, PinGap: 20 * time.Millisecond, CatchUpWait: 10 * time.Second}, peers, nil)
	gr.waitMesh(1)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	admin := gr.rooms[0]
	gr.advance(t, ctx, 8, "k")
	waitFor(t, 30*time.Second, "the pushes", func() bool { return !admin.dirtyAny() && admin.Status().PinError == nil })
	for _, l := range admin.snapshot() {
		pinsHold(t, peers, gr.nodes[0].dev, l.id, l.log.Len())
	}
}
