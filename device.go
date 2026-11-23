package main

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net/netip"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sync"
	"time"

	kologossip "github.com/ciranda-dev/kolo-gossip"
	koloid "github.com/ciranda-dev/kolo-id"
	kolostore "github.com/ciranda-dev/kolo-store"
	koloswarm "github.com/ciranda-dev/kolo-swarm"

	"github.com/ciranda-dev/kolo-mls/backendstore"

	"github.com/ciranda-dev/kolo-group-wails3-example/internal/room"
)

// dataLayout is the format of the data folder. A folder of another layout is refused and left as it is.
const dataLayout = 1

// The wait before the first new try to reach the entry nodes, and its longest, as in the other Kolo apps.
const (
	netRetryMin = 2 * time.Second
	netRetryMax = 30 * time.Second
)

// tuning lets a test change the settings of the parts a device builds; the app leaves it nil.
type tuning func(*koloswarm.Config, *kologossip.Config, *room.Config)

// device is one Kolo Group device: its keys, stores, swarm, gossip and room node. The window and -headless both run one
// over the same data folder:
//
//	layout.json                 {"layout":1}
//	person.key, device.key      the person's kolo-id and this device's kolo-id
//	device.cert                 the person's certificate for the device
//	mls/, state/, logs/, app/   the MLS state, the rooms' state with the admins' braid, the logs, and the messages the window was told of
//	welcomes                    the hashes of the welcomes -welcome-file has joined
type device struct {
	set    settings
	person *koloid.Identity
	key    *koloid.Identity
	cert   *koloid.DeviceCert
	sw     *koloswarm.Swarm
	gossip *kologossip.Gossip
	node   *room.Node
	msgs   *msgStore
	logs   *kolostore.Store
	emit   func(name string, data any)

	backends []kolostore.Backend
	ctx      context.Context
	cancel   context.CancelFunc
	wg       sync.WaitGroup
	inflight sync.WaitGroup // calls that create or join a room, which Close waits for before it closes the node

	mu       sync.Mutex
	peers    map[[32]byte]int // connections by remote device key
	netErr   error            // why the entry nodes could not be reached, nil once one answered
	online   bool
	relays   []RelayState // the configured relays, in order
	relaySeq uint64
	watched  map[room.GroupID]bool
	readers  map[room.GroupID]bool // rooms joined with a read link, which a restart does not keep
	gone     map[room.GroupID]bool // rooms this device was removed from or left: the node still lists them
	closed   bool
}

// openDevice opens the data folder and starts the device. Entry nodes that do not answer are not an error: the device
// asks again by itself, and Status says it is offline meanwhile.
func openDevice(ctx context.Context, set settings, emit func(string, any), tune tuning) (d *device, err error) {
	if err := checkDataDir(set.DataDir); err != nil {
		return nil, err
	}
	d = &device{set: set, emit: emit, peers: map[[32]byte]int{}, watched: map[room.GroupID]bool{}, readers: map[room.GroupID]bool{}, gone: map[room.GroupID]bool{}}
	if d.emit == nil {
		d.emit = func(string, any) {}
	}
	defer func() {
		if err != nil {
			d.Close()
		}
	}()
	if d.person, err = loadIdentity(filepath.Join(set.DataDir, "person.key")); err != nil {
		return nil, err
	}
	if d.key, err = loadIdentity(filepath.Join(set.DataDir, "device.key")); err != nil {
		return nil, err
	}
	if d.cert, err = loadCert(filepath.Join(set.DataDir, "device.cert"), d.person, d.key); err != nil {
		return nil, err
	}
	var stores [4]kolostore.Backend
	for i, name := range []string{"mls", "state", "logs", "app"} {
		if stores[i], err = kolostore.OpenPebble(filepath.Join(set.DataDir, name)); err != nil {
			return nil, err
		}
		d.backends = append(d.backends, stores[i])
	}
	d.msgs = newMsgStore(stores[3])
	d.ctx, d.cancel = context.WithCancel(ctx)
	sc := &koloswarm.Config{LookupInterval: set.LookupInterval, Relays: set.relays, BootstrapNodes: set.bootstrap,
		Allow: func(k ed25519.PublicKey, a netip.AddrPort) bool {
			n := d.roomNode()
			return n != nil && n.Allow(k, a)
		}}
	for _, r := range set.relays {
		d.relays = append(d.relays, RelayState{Addr: r.Addr, Key: hex.EncodeToString(r.PublicKey), State: "waiting"})
	}
	gc := &kologossip.Config{}
	rc := &room.Config{Device: d.key, Limits: room.Limits{PinTTL: set.PinTTL}}
	for _, p := range set.pins {
		rc.Pins = append(rc.Pins, p.peer())
	}
	if tune != nil {
		tune(sc, gc, rc)
	}
	if d.sw, err = koloswarm.New(d.ctx, set.Listen, d.key, sc); err != nil {
		return nil, err
	}
	d.bootstrap()
	gc.Cert, gc.Swarm = d.cert, d.sw
	if d.gossip, err = kologossip.New(d.key, gc); err != nil {
		return nil, err
	}
	rc.Cert, rc.Swarm, rc.Gossip = d.cert, d.sw, d.gossip
	d.logs = kolostore.New(stores[2])
	rc.MLSStore, rc.Backend, rc.Store = backendstore.New(stores[0], []byte("mls/")), stores[1], d.logs
	node, err := room.Open(d.ctx, rc)
	if err != nil {
		return nil, err
	}
	d.mu.Lock()
	d.node = node
	d.mu.Unlock()
	d.wg.Go(d.serve)
	for _, r := range node.Rooms() {
		d.watch(r)
	}
	return d, nil
}

// bootstrap joins the network the settings choose. When no entry node answers it goes on asking, waiting 2 seconds,
// then 4, up to 30, until one does.
func (d *device) bootstrap() {
	err := d.sw.Bootstrap(d.ctx, nil)
	d.setNet(err)
	if err == nil || d.ctx.Err() != nil {
		return
	}
	d.wg.Go(func() {
		for wait := netRetryMin; ; wait = min(2*wait, netRetryMax) {
			select {
			case <-d.ctx.Done():
				return
			case <-time.After(wait):
			}
			err := d.sw.Bootstrap(d.ctx, nil)
			d.setNet(err)
			if err == nil {
				return
			}
		}
	})
}

func (d *device) setNet(err error) {
	d.mu.Lock()
	d.online, d.netErr = err == nil, err
	d.mu.Unlock()
}

// serve gives every connection of the swarm to the room node, and keeps count of the devices connected.
func (d *device) serve() {
	for {
		ev, err := d.sw.Next(d.ctx)
		if err != nil {
			return
		}
		switch ev.Kind {
		case koloswarm.Connected:
			d.mu.Lock()
			d.peers[[32]byte(ev.PublicKey)]++
			d.mu.Unlock()
			d.wg.Go(func() { d.node.ServeConn(d.ctx, ev.Peer.Conn(), ev.Peer.Outbound()) })
		case koloswarm.Lost:
			d.mu.Lock()
			k := [32]byte(ev.PublicKey)
			if d.peers[k]--; d.peers[k] <= 0 {
				delete(d.peers, k)
			}
			d.mu.Unlock()
		case koloswarm.RelayChanged:
			d.relayChanged(ev)
		}
	}
}

// watch delivers the events of a room to the window until the room ends.
func (d *device) watch(r *room.Room) {
	id := r.ID()
	d.mu.Lock()
	if d.watched[id] || d.closed {
		d.mu.Unlock()
		return
	}
	d.watched[id] = true
	d.mu.Unlock()
	d.wg.Go(func() {
		for {
			ev, err := r.Next(d.ctx)
			if errors.Is(err, room.ErrRemoved) {
				d.markGone(id)
			}
			if err != nil {
				return
			}
			d.deliver(id, ev)
			if ev.Kind == room.Removed {
				d.markGone(id)
			}
		}
	})
}

// roomNode returns the room node, nil while the device opens.
func (d *device) roomNode() *room.Node {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.node
}

// markGone records that this device is no longer in the room.
func (d *device) markGone(id room.GroupID) {
	d.mu.Lock()
	d.gone[id] = true
	d.mu.Unlock()
}

// Peers returns how many devices are connected.
func (d *device) Peers() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.peers)
}

// netStatus says whether an entry node answered, and why not when none did.
func (d *device) netStatus() (online bool, why string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.netErr != nil {
		why = d.netErr.Error()
	}
	return d.online, why
}

// begin starts a call that creates or joins a room; the caller ends it with the function it returns. Close waits for it.
func (d *device) begin() (end func(), err error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return nil, room.ErrClosed
	}
	d.inflight.Add(1)
	return d.inflight.Done, nil
}

// Close stops the device and keeps its state.
func (d *device) Close() error {
	d.mu.Lock()
	if d.closed {
		d.mu.Unlock()
		return nil
	}
	d.closed = true
	node := d.node
	d.mu.Unlock()
	if d.cancel != nil {
		d.cancel()
	}
	d.inflight.Wait()
	var errs []error
	if node != nil {
		errs = append(errs, node.Close())
	}
	if d.gossip != nil {
		errs = append(errs, d.gossip.Close())
	}
	if d.sw != nil {
		errs = append(errs, d.sw.Close())
	}
	d.wg.Wait()
	if d.logs != nil {
		errs = append(errs, d.logs.Close())
	}
	for _, b := range slices.Backward(d.backends) {
		errs = append(errs, b.Close())
	}
	return errors.Join(errs...)
}

// checkDataDir makes the data folder, private, and refuses one that other users can open or that has another layout.
func checkDataDir(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if st, err := os.Stat(dir); err != nil {
		return err
	} else if runtime.GOOS != "windows" && st.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("kolo-group: the data folder %s can be opened by other users: run chmod 700 on it", dir)
	}
	path := filepath.Join(dir, "layout.json")
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return os.WriteFile(path, fmt.Appendf(nil, "{\"layout\":%d}\n", dataLayout), 0o600)
	}
	if err != nil {
		return err
	}
	var l struct {
		Layout int `json:"layout"`
	}
	if err := json.Unmarshal(b, &l); err != nil || l.Layout != dataLayout {
		return fmt.Errorf("kolo-group: the data folder %s has a layout this version cannot open: use another -data", dir)
	}
	return nil
}

// loadIdentity returns the kolo-id kept at path, making and keeping a new one the first time.
func loadIdentity(path string) (*koloid.Identity, error) {
	id, err := koloid.Load(path)
	if !errors.Is(err, fs.ErrNotExist) {
		return id, err
	}
	if id, err = koloid.Generate(); err != nil {
		return nil, err
	}
	if err = id.SaveNew(path); err != nil && !errors.Is(err, koloid.ErrSavedNotSynced) {
		return nil, err
	}
	return id, nil
}

// loadCert returns the certificate of the person for the device kept at path, issuing it the first time.
func loadCert(path string, person, dev *koloid.Identity) (*koloid.DeviceCert, error) {
	cert, err := koloid.LoadCert(path)
	if !errors.Is(err, fs.ErrNotExist) {
		return cert, err
	}
	offer, err := person.OfferDeviceCert(dev.PublicKey(), &koloid.IssueOptions{Serial: 1})
	if err != nil {
		return nil, err
	}
	if cert, err = dev.AcceptDeviceCert(offer, person.PublicKey()); err != nil {
		return nil, err
	}
	return cert, koloid.SaveCert(path, cert)
}

// deliver keeps what a room tells and tells the window. A room joined with a read link is not kept across a restart, so
// its messages are not either.
func (d *device) deliver(id room.GroupID, ev room.Event) {
	out := eventInfo(id, ev, d.person.PublicKey())
	d.mu.Lock()
	reader := d.readers[id]
	d.mu.Unlock()
	if !reader {
		var err error
		switch {
		case ev.Kind == room.Message && ev.Err == nil:
			var kept bool
			if out.Message, kept, err = d.msgs.add(out.Room, out.Message); err == nil && !kept {
				return
			}
		case ev.Kind == room.Stored && ev.Err == nil:
			err = d.msgs.stored(out.Room, out.Message.ID)
		case ev.Kind == room.Removed:
			err = d.msgs.forget(out.Room)
		}
		if err != nil {
			log.Printf("kolo-group: keeping a message of the room failed: %v", err)
		}
	}
	d.emit("group:event", out)
}
