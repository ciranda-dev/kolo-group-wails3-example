package room

import (
	"bytes"
	"cmp"
	"container/heap"
	"slices"
	"sync"
	"time"
)

// before is the display order of two messages that nothing else orders: author time, then id.
func before(a, b *Event) int {
	if c := cmp.Compare(a.Time.UnixMilli(), b.Time.UnixMilli()); c != 0 {
		return c
	}
	return bytes.Compare(a.ID[:], b.ID[:])
}

type byTime []*Event

func (h byTime) Len() int           { return len(h) }
func (h byTime) Less(i, j int) bool { return before(h[i], h[j]) < 0 }
func (h byTime) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *byTime) Push(x any)        { *h = append(*h, x.(*Event)) }
func (h *byTime) Pop() any {
	old := *h
	x := old[len(old)-1]
	*h = old[:len(old)-1]
	return x
}

func refsOf(ev *Event) []MessageID {
	var refs []MessageID
	if ev.ReplyTo != (MessageID{}) {
		refs = append(refs, ev.ReplyTo)
	}
	for _, p := range ev.Parents {
		if !slices.Contains(refs, p) {
			refs = append(refs, p)
		}
	}
	return refs
}

// Display returns the message events in display order: a topological order of the causal references (ReplyTo and
// Parents) with ties broken by author time and id, so members that hold the same messages show the same order.
// References to messages not in events are ignored.
func Display(events []Event) []Event {
	byID := make(map[MessageID]*Event, len(events))
	for i := range events {
		byID[events[i].ID] = &events[i]
	}
	waiting := make(map[MessageID]int, len(events))
	children := map[MessageID][]*Event{}
	ready := &byTime{}
	for i := range events {
		ev := &events[i]
		for _, r := range refsOf(ev) {
			if byID[r] != nil && r != ev.ID {
				waiting[ev.ID]++
				children[r] = append(children[r], ev)
			}
		}
		if waiting[ev.ID] == 0 {
			*ready = append(*ready, ev)
		}
	}
	heap.Init(ready)
	out := make([]Event, 0, len(events))
	for ready.Len() > 0 {
		ev := heap.Pop(ready).(*Event)
		out = append(out, *ev)
		for _, c := range children[ev.ID] {
			if waiting[c.ID]--; waiting[c.ID] == 0 {
				heap.Push(ready, c)
			}
		}
	}
	return out
}

// entry is a message the orderer holds or has released.
type entry struct {
	ev       Event
	refs     []MessageID
	waiting  int
	deadline time.Time
	held     bool
	released bool
}

// orderer releases messages so that none shows before a message it names that is known, and a message whose
// reference does not arrive within the hold shows anyway.
type orderer struct {
	mu                            sync.Mutex
	self                          []byte
	hold                          time.Duration
	maxHeld, maxKnown, maxParents int
	known                         map[MessageID]*entry
	ring                          []MessageID // released ids, oldest first
	waiters                       map[MessageID][]*entry
	queue                         []*entry // held entries in arrival order
	heldN                         int
	tips                          map[MessageID]*Event
	lastOwn                       MessageID
}

func newOrderer(self []byte, l Limits) *orderer {
	return &orderer{self: self, hold: l.ParentHold, maxHeld: l.MaxHeld, maxKnown: l.MaxKnown, maxParents: l.MaxParents,
		known: map[MessageID]*entry{}, waiters: map[MessageID][]*entry{}, tips: map[MessageID]*Event{}}
}

// add takes a received message and returns the events it releases, in release order.
func (o *orderer) add(ev Event, now time.Time) []Event {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.known[ev.ID] != nil {
		return nil
	}
	e := &entry{ev: ev, refs: refsOf(&ev)}
	o.known[ev.ID] = e
	for _, r := range e.refs {
		if k := o.known[r]; k == nil || !k.released {
			e.waiting++
			o.waiters[r] = append(o.waiters[r], e)
		}
	}
	var out []Event
	if e.waiting == 0 {
		return o.releaseFrom(e, out)
	}
	if o.heldN >= o.maxHeld {
		out = o.forceOldest(out)
	}
	e.held, e.deadline = true, now.Add(o.hold)
	o.heldN++
	o.queue = append(o.queue, e)
	return out
}

// addOwn records a message this device sent, which is released at once.
func (o *orderer) addOwn(ev Event) []Event {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.known[ev.ID] != nil {
		return nil
	}
	e := &entry{ev: ev, refs: refsOf(&ev)}
	o.known[ev.ID] = e
	return o.releaseFrom(e, nil)
}

// expire releases the messages whose hold ended.
func (o *orderer) expire(now time.Time) []Event {
	o.mu.Lock()
	defer o.mu.Unlock()
	var out []Event
	for {
		o.trim()
		if len(o.queue) == 0 || o.queue[0].deadline.After(now) {
			return out
		}
		out = o.force(o.queue[0], out)
	}
}

// nextDeadline returns when the oldest held message must show, the zero time when none is held.
func (o *orderer) nextDeadline() time.Time {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.trim()
	if len(o.queue) == 0 {
		return time.Time{}
	}
	return o.queue[0].deadline
}

// trim drops released entries from the front of the queue.
func (o *orderer) trim() {
	for len(o.queue) > 0 && o.queue[0].released {
		o.queue = o.queue[1:]
	}
}

func (o *orderer) forceOldest(out []Event) []Event {
	o.trim()
	if len(o.queue) == 0 {
		return out
	}
	return o.force(o.queue[0], out)
}

// force releases a held entry although a reference is missing.
func (o *orderer) force(e *entry, out []Event) []Event {
	for _, r := range e.refs {
		if w := slices.DeleteFunc(o.waiters[r], func(x *entry) bool { return x == e }); len(w) == 0 {
			delete(o.waiters, r)
		} else {
			o.waiters[r] = w
		}
	}
	e.waiting = 0
	return o.releaseFrom(e, out)
}

// releaseFrom releases e and every entry that becomes ready, the earliest author time first.
func (o *orderer) releaseFrom(e *entry, out []Event) []Event {
	ready := []*entry{e}
	for len(ready) > 0 {
		i := 0
		for j := range ready {
			if before(&ready[j].ev, &ready[i].ev) < 0 {
				i = j
			}
		}
		e := ready[i]
		ready = slices.Delete(ready, i, i+1)
		if e.released {
			continue
		}
		e.released = true
		if e.held {
			e.held = false
			o.heldN--
		}
		out = append(out, e.ev)
		o.remember(e)
		for _, w := range o.waiters[e.ev.ID] {
			if w.waiting--; w.waiting == 0 {
				ready = append(ready, w)
			}
		}
		delete(o.waiters, e.ev.ID)
	}
	return out
}

// remember records a released message as a tip and forgets the oldest ones past maxKnown.
func (o *orderer) remember(e *entry) {
	for _, r := range e.refs {
		delete(o.tips, r)
	}
	ev := e.ev
	o.tips[ev.ID] = &ev
	if bytes.Equal(ev.Device, o.self) {
		o.lastOwn = ev.ID
	}
	o.ring = append(o.ring, ev.ID)
	for len(o.ring) > o.maxKnown {
		old := o.ring[0]
		o.ring = o.ring[1:]
		delete(o.known, old)
		delete(o.tips, old)
	}
	if len(o.ring) == 0 {
		o.ring = nil
	}
}

// parents returns what the next message of this device names: its previous message, then the newest other tips.
func (o *orderer) parents() []MessageID {
	o.mu.Lock()
	defer o.mu.Unlock()
	var out []MessageID
	if k := o.known[o.lastOwn]; k != nil && o.lastOwn != (MessageID{}) {
		out = append(out, o.lastOwn)
	}
	tips := make([]*Event, 0, len(o.tips))
	for id, ev := range o.tips {
		if id != o.lastOwn {
			tips = append(tips, ev)
		}
	}
	slices.SortFunc(tips, func(a, b *Event) int { return before(b, a) })
	for _, t := range tips {
		if len(out) >= o.maxParents {
			break
		}
		out = append(out, t.ID)
	}
	return out
}
