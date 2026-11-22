package room

// One connection for gossip, kolo-sync and the admins' braid.
//
// kolo-swarm keeps one connection per peer key, and each of the three must be the only one that accepts streams on it,
// while the braid needs both kinds. So every stream starts with a tag byte that says which part it is for: tagGossip
// (unidirectional streams), tagSync (bidirectional) and tagBraid (both). The mux reads the tag of each stream on a
// goroutine of its own and hands the stream to the part of that tag, which looks like a whole connection to its user.

import (
	"context"
	"crypto/ed25519"
	"errors"
	"io"
	"sync"
	"time"

	koloswarm "github.com/ciranda-dev/kolo-swarm"
	kolotransport "github.com/ciranda-dev/kolo-transport"
)

const (
	tagGossip byte = 'm'
	tagSync   byte = 's'
	tagBraid  byte = 'b'
	// tagWait is how long a peer may take to send the tag of a stream it opened, as kolo-swarm's HelloTimeout default.
	tagWait    = 5 * time.Second
	codeBadTag = 3
)

var errPartClosed = errors.New("room: connection part closed")

// mux splits one connection into parts by the tag of each stream.
type mux struct {
	conn koloswarm.Conn
	wg   sync.WaitGroup // the handoff goroutines, which end with the connection

	gossip, sync, braid *muxConn
}

func newMux(c koloswarm.Conn) *mux {
	m := &mux{conn: c}
	m.gossip, m.sync, m.braid = m.newPart(tagGossip), m.newPart(tagSync), m.newPart(tagBraid)
	return m
}

func (m *mux) newPart(tag byte) *muxConn {
	ctx, cancel := context.WithCancel(m.conn.Context())
	return &muxConn{m: m, tag: tag, ctx: ctx, cancel: cancel,
		uni: make(chan *kolotransport.ReceiveStream), bidi: make(chan *kolotransport.Stream)}
}

func (m *mux) part(tag byte) *muxConn {
	switch tag {
	case tagGossip:
		return m.gossip
	case tagSync:
		return m.sync
	case tagBraid:
		return m.braid
	}
	return nil
}

// run hands every stream of the connection to its part until the connection ends.
func (m *mux) run() {
	m.wg.Go(func() {
		for {
			st, err := m.conn.AcceptUniStream(m.conn.Context())
			if err != nil {
				return
			}
			m.wg.Go(func() { handoff(m, st, false, func(p *muxConn) chan *kolotransport.ReceiveStream { return p.uni }) })
		}
	})
	m.wg.Go(func() {
		for {
			st, err := m.conn.AcceptStream(m.conn.Context())
			if err != nil {
				return
			}
			m.wg.Go(func() { handoff(m, st, true, func(p *muxConn) chan *kolotransport.Stream { return p.bidi }) })
		}
	})
}

// stop ends every part and waits for the goroutines of the mux.
func (m *mux) stop() {
	m.gossip.cancel()
	m.sync.cancel()
	m.braid.cancel()
	m.wg.Wait()
}

// tagged is what handoff needs of a *kolotransport.Stream and a *kolotransport.ReceiveStream.
type tagged interface {
	io.Reader
	SetReadDeadline(time.Time) error
	CancelRead(code uint64) error
}

// handoff reads the tag of one stream and waits until its part takes it, the part closes or the connection ends.
func handoff[S tagged](m *mux, st S, bidi bool, ch func(*muxConn) chan S) {
	cancel := func() {
		st.CancelRead(codeBadTag)
		if s, ok := any(st).(*kolotransport.Stream); ok {
			s.CancelWrite(codeBadTag)
		}
	}
	st.SetReadDeadline(time.Now().Add(tagWait))
	var tag [1]byte
	if _, err := io.ReadFull(st, tag[:]); err != nil || !allowedTag(tag[0], bidi) {
		cancel()
		return
	}
	st.SetReadDeadline(time.Time{})
	p := m.part(tag[0])
	select {
	case ch(p) <- st:
	case <-p.ctx.Done():
		cancel()
	}
}

// allowedTag says which tags each kind of stream takes: unidirectional streams of gossip and the braid, bidirectional
// streams of kolo-sync and the braid.
func allowedTag(tag byte, bidi bool) bool {
	return tag == tagBraid || bidi && tag == tagSync || !bidi && tag == tagGossip
}

// muxConn is one part of a connection. It has the methods of a koloswarm.Conn, so gossip, kolo-sync and the braid take
// it as a connection; Close ends the part only.
type muxConn struct {
	m      *mux
	tag    byte
	ctx    context.Context
	cancel context.CancelFunc
	uni    chan *kolotransport.ReceiveStream
	bidi   chan *kolotransport.Stream
}

func (p *muxConn) RemotePublicKey() ed25519.PublicKey { return p.m.conn.RemotePublicKey() }
func (p *muxConn) Context() context.Context           { return p.ctx }
func (p *muxConn) Close() error                       { p.cancel(); return nil }

func (p *muxConn) CloseWithError(uint64, string) error { return p.Close() }

func (p *muxConn) OpenStream(ctx context.Context) (*kolotransport.Stream, error) {
	if p.ctx.Err() != nil {
		return nil, errPartClosed
	}
	st, err := p.m.conn.OpenStream(ctx)
	if err != nil {
		return nil, err
	}
	st.SetWriteDeadline(time.Now().Add(tagWait))
	if _, err := st.Write([]byte{p.tag}); err != nil {
		st.CancelWrite(codeBadTag)
		st.CancelRead(codeBadTag)
		return nil, err
	}
	st.SetWriteDeadline(time.Time{})
	return st, nil
}

func (p *muxConn) OpenUniStream(ctx context.Context) (*kolotransport.SendStream, error) {
	if p.ctx.Err() != nil {
		return nil, errPartClosed
	}
	st, err := p.m.conn.OpenUniStream(ctx)
	if err != nil {
		return nil, err
	}
	st.SetWriteDeadline(time.Now().Add(tagWait))
	if _, err := st.Write([]byte{p.tag}); err != nil {
		st.CancelWrite(codeBadTag)
		return nil, err
	}
	st.SetWriteDeadline(time.Time{})
	return st, nil
}

func (p *muxConn) AcceptStream(ctx context.Context) (*kolotransport.Stream, error) {
	return accept(ctx, p, p.bidi)
}

func (p *muxConn) AcceptUniStream(ctx context.Context) (*kolotransport.ReceiveStream, error) {
	return accept(ctx, p, p.uni)
}

func accept[S any](ctx context.Context, p *muxConn, ch chan S) (S, error) {
	var zero S
	select {
	case st := <-ch:
		return st, nil
	case <-p.ctx.Done():
		return zero, errPartClosed
	case <-ctx.Done():
		return zero, ctx.Err()
	}
}
