package room

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// TestSixtyFourNodes runs a room of 64 devices on 127.0.0.1 in one process, over real swarm connections: the 63 others
// join in Adds of 8, every device sends in waves of eight, and every member opens every message.
func TestSixtyFourNodes(t *testing.T) {
	if testing.Short() {
		t.Skip("64 devices")
	}
	const n = 64
	server := newServer(t)
	// The parent cap is raised to the number of devices so that the test measures how many parents devices name.
	lim := Limits{MaxParents: n}
	first := newTNode(t, tnodeOpts{server: server, limits: lim})
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	r, err := first.n.Create(ctx, "sixty-four", Policy{})
	if err != nil {
		t.Fatal(err)
	}
	gr := &group{t: t, server: server, nodes: []*tnode{first}, rooms: []*Room{r}, recs: []*recorder{record(t, r)}}
	var rest []*tnode
	for range n - 1 {
		rest = append(rest, newTNode(t, tnodeOpts{server: server, limits: lim}))
	}
	start := time.Now()
	gr.joinAll(ctx, rest, 8)
	t.Logf("64 devices joined in %v, epoch %d", time.Since(start).Round(time.Millisecond), gr.rooms[0].Status().Epoch)
	for i, r := range gr.rooms {
		if got := len(r.Members()); got != n {
			t.Fatalf("member %d lists %d members", i, got)
		}
	}
	gr.waitMesh(4)
	// Every device sends, eight at a time: one process runs all 64 gossip nodes, whose work budgets count wall time.
	want := map[string]*tnode{}
	for from := 0; from < n; from += 8 {
		for i := from; i < from+8; i++ {
			body := fmt.Sprintf("hello from %d", i)
			want[body] = gr.nodes[i]
			go func() {
				if _, err := gr.rooms[i].Send(ctx, []byte(body), MessageID{}); err != nil {
					t.Error(err)
				}
			}()
		}
		gr.checkDelivered(want)
	}
	auths := gr.authenticators()
	for i, a := range auths {
		if string(a) != string(auths[0]) {
			t.Fatalf("member %d has another epoch authenticator", i)
		}
	}
	maxParents := 0
	for _, ev := range gr.recs[0].messages() {
		maxParents = max(maxParents, len(ev.Parents))
	}
	t.Logf("64 messages sent in 8 waves, every member opened all of them; the most parents one named: %d", maxParents)
	// All devices at once: the worst case for the number of tips. Delivery of this burst is measured, not asserted.
	before := len(gr.recs[0].messages())
	for i, r := range gr.rooms {
		go r.Send(ctx, []byte(fmt.Sprintf("burst %d", i)), MessageID{})
	}
	time.Sleep(5 * time.Second)
	burstParents, got := 0, 0
	for _, ev := range gr.recs[0].messages()[before:] {
		burstParents, got = max(burstParents, len(ev.Parents)), got+1
	}
	t.Logf("64 devices sending at once: member 0 received %d of 64 messages in 5 s; the most parents one named: %d", got, burstParents)
}
