package room

import (
	"math/rand/v2"
	"slices"
	"testing"
	"time"
)

var t0 = time.Unix(1_700_000_000, 0)

func testLimits() Limits {
	l, _ := Limits{}.withDefaults()
	return l
}

func evID(n int) MessageID { return MessageID{byte(n), byte(n >> 8), 0xee} }

// dag is a random causal history of devices that write in turn: each message names its author's previous message, a
// reply target and a few earlier messages.
func dag(r *rand.Rand, n, devices int) []Event {
	evs := make([]Event, 0, n)
	last := make([]int, devices)
	for i := range last {
		last[i] = -1
	}
	for i := range n {
		d := r.IntN(devices)
		ev := Event{Kind: Message, ID: evID(i + 1), Device: []byte{byte(d)}, Time: time.UnixMilli(int64(r.IntN(n/3 + 1)))}
		if last[d] >= 0 {
			ev.Parents = append(ev.Parents, evs[last[d]].ID)
		}
		if i > 0 {
			ev.ReplyTo = evs[r.IntN(i)].ID
			for range r.IntN(3) {
				ev.Parents = append(ev.Parents, evs[r.IntN(i)].ID)
			}
		}
		last[d] = i
		evs = append(evs, ev)
	}
	return evs
}

func ids(evs []Event) []MessageID {
	out := make([]MessageID, len(evs))
	for i, e := range evs {
		out[i] = e.ID
	}
	return out
}

// isLinearExtension reports the first event shown before a message it names, if any.
func isLinearExtension(evs []Event) (bad *Event) {
	seen := map[MessageID]bool{}
	in := map[MessageID]bool{}
	for _, e := range evs {
		in[e.ID] = true
	}
	for i := range evs {
		for _, ref := range refsOf(&evs[i]) {
			if in[ref] && !seen[ref] {
				return &evs[i]
			}
		}
		seen[evs[i].ID] = true
	}
	return nil
}

// A reply never shows before its parent when the parent is known, and each device's messages keep its order, whatever
// the order the messages arrive in.
func TestOrdererNeverShowsAReplyBeforeItsParent(t *testing.T) {
	r := rand.New(rand.NewPCG(1, 2))
	evs := dag(r, 120, 6)
	for round := range 200 {
		arrival := slices.Clone(evs)
		r.Shuffle(len(arrival), func(i, j int) { arrival[i], arrival[j] = arrival[j], arrival[i] })
		o := newOrderer([]byte{0}, testLimits())
		var out []Event
		for _, ev := range arrival {
			out = append(out, o.add(ev, t0)...)
		}
		if len(out) != len(evs) {
			t.Fatalf("round %d: %d of %d released", round, len(out), len(evs))
		}
		if bad := isLinearExtension(out); bad != nil {
			t.Fatalf("round %d: message %x shown before a message it names", round, bad.ID[:2])
		}
		pos := map[MessageID]int{}
		for i, e := range out {
			pos[e.ID] = i
		}
		last := map[byte]int{}
		for _, e := range evs { // evs is in send order
			if p, ok := last[e.Device[0]]; ok && pos[e.ID] < p {
				t.Fatalf("round %d: device %d shown out of its own order", round, e.Device[0])
			}
			last[e.Device[0]] = pos[e.ID]
		}
	}
}

// Members that hold the same messages show the same order, whatever order they were fed in.
func TestDisplayIsTheSameForEveryOrderOfArrival(t *testing.T) {
	r := rand.New(rand.NewPCG(3, 4))
	evs := dag(r, 150, 8)
	want := Display(slices.Clone(evs))
	if len(want) != len(evs) || isLinearExtension(want) != nil {
		t.Fatal("Display is not a causal order of every message")
	}
	for range 100 {
		in := slices.Clone(evs)
		r.Shuffle(len(in), func(i, j int) { in[i], in[j] = in[j], in[i] })
		if got := Display(in); !slices.Equal(ids(got), ids(want)) {
			t.Fatal("two members show different orders of the same messages")
		}
	}
}

// Without references the display order is author time, then id.
func TestDisplayOrdersByTimeThenID(t *testing.T) {
	in := []Event{
		{ID: evID(3), Time: time.UnixMilli(5)},
		{ID: evID(2), Time: time.UnixMilli(5)},
		{ID: evID(9), Time: time.UnixMilli(1)},
	}
	got := ids(Display(in))
	if !slices.Equal(got, []MessageID{evID(9), evID(2), evID(3)}) {
		t.Fatalf("order %x", got)
	}
}

func TestOrdererHoldsAMissingParentThenShowsTheMessage(t *testing.T) {
	l := testLimits()
	o := newOrderer(nil, l)
	parent := Event{ID: evID(1), Time: time.UnixMilli(1)}
	child := Event{ID: evID(2), Time: time.UnixMilli(2), ReplyTo: parent.ID}
	if out := o.add(child, t0); len(out) != 0 {
		t.Fatal("a reply shown before its parent arrived")
	}
	if out := o.expire(t0.Add(l.ParentHold - time.Nanosecond)); len(out) != 0 {
		t.Fatal("a reply shown before its hold ended")
	}
	if d := o.nextDeadline(); !d.Equal(t0.Add(l.ParentHold)) {
		t.Fatalf("deadline %v", d)
	}
	if out := o.expire(t0.Add(l.ParentHold)); len(out) != 1 || out[0].ID != child.ID {
		t.Fatalf("the reply is not shown when its hold ends: %d events", len(out))
	}
	if out := o.add(parent, t0.Add(time.Minute)); len(out) != 1 {
		t.Fatal("a late parent is not shown at once")
	}
	if !o.nextDeadline().IsZero() {
		t.Fatal("a deadline remains after every message showed")
	}
}

func TestOrdererShowsAParentBeforeItsHeldChild(t *testing.T) {
	o := newOrderer(nil, testLimits())
	parent := Event{ID: evID(1), Time: time.UnixMilli(9)}
	child := Event{ID: evID(2), Time: time.UnixMilli(1), Parents: []MessageID{parent.ID}}
	o.add(child, t0)
	out := o.add(parent, t0)
	if len(out) != 2 || out[0].ID != parent.ID || out[1].ID != child.ID {
		t.Fatalf("released %x", ids(out))
	}
}

func TestOrdererBoundsHeldMessages(t *testing.T) {
	l := testLimits()
	l.MaxHeld = 2
	o := newOrderer(nil, l)
	var shown []Event
	for i := range 5 {
		shown = append(shown, o.add(Event{ID: evID(i + 1), Time: time.UnixMilli(int64(i)), ReplyTo: evID(100 + i)}, t0)...)
	}
	if len(shown) != 3 || shown[0].ID != evID(1) {
		t.Fatalf("%d shown early, want the 3 oldest of 5 with room for 2", len(shown))
	}
	if o.heldN != 2 {
		t.Fatalf("%d held", o.heldN)
	}
}

func TestOrdererForgetsPastMaxKnown(t *testing.T) {
	l := testLimits()
	l.MaxKnown = 3
	o := newOrderer(nil, l)
	for i := range 10 {
		o.add(Event{ID: evID(i + 1), Time: time.UnixMilli(int64(i))}, t0)
	}
	if len(o.known) != 3 || len(o.tips) > 3 {
		t.Fatalf("%d ids known, %d tips", len(o.known), len(o.tips))
	}
}

func TestOrdererParentsAreOwnPreviousThenNewestTips(t *testing.T) {
	l := testLimits()
	l.MaxParents = 3
	self := []byte{7}
	o := newOrderer(self, l)
	for i, d := range [][]byte{{1}, {2}, self, {3}, {4}} {
		o.add(Event{ID: evID(i + 1), Device: d, Time: time.UnixMilli(int64(i + 1))}, t0)
	}
	got := o.parents()
	want := []MessageID{evID(3), evID(5), evID(4)}
	if !slices.Equal(got, want) {
		t.Fatalf("parents %x, want %x", got, want)
	}
	o.addOwn(Event{ID: evID(6), Device: self, Time: time.UnixMilli(6), Parents: got})
	// The three tips it named are covered; the two older ones it left out remain tips.
	if got := o.parents(); !slices.Equal(got, []MessageID{evID(6), evID(2), evID(1)}) {
		t.Fatalf("after a message that names three tips: %x", got)
	}
}

func TestOrdererIgnoresARepeatedMessage(t *testing.T) {
	o := newOrderer(nil, testLimits())
	ev := Event{ID: evID(1)}
	if len(o.add(ev, t0)) != 1 || len(o.add(ev, t0)) != 0 || len(o.addOwn(ev)) != 0 {
		t.Fatal("a message was shown twice")
	}
}
