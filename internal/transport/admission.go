package transport

import (
	"crypto/hmac"
	"encoding/json"
	"net"
	"slices"
	"sync"
	"time"

	"github.com/notaharness/beam/internal/stream"
)

// Admission budgets (docs/03).
const (
	helloTimeout = 5 * time.Second
	maxUnbound   = 16
)

// AdmitFunc verifies a dialer's signed entry, including that its peer is not
// revoked. It returns the entry's peer id and node key, or a refusal reason.
type AdmitFunc func(entry json.RawMessage) (peerID string, nodePublic [32]byte, reason string)

// HandleFunc serves one stream from an admitted peer. The header is read and
// its version checked; the handler writes the response and owns c.
type HandleFunc func(peerID string, h stream.Header, c *stream.Conn)

// admission binds each tunnel's client key to a peer through hello
// (docs/03, Admission). A key it has seen has one record; a key without one
// is unbound. Each operation makes its whole change under mu.
type admission struct {
	key    *Key
	admit  AdmitFunc
	handle HandleFunc

	mu      sync.Mutex
	tunnels map[[32]byte]*tunnel
	pending [][32]byte // the pending records' keys, oldest first: the eviction order

	hook func(point string, c [32]byte) // tests pause here; nil otherwise
}

type tunnel struct {
	state   int
	peer    string            // bound
	hello   net.Conn          // pending
	streams map[net.Conn]bool // bound
}

// Tunnel states.
const (
	pending = iota // its hello is under way
	bound          // attributed to peer; carries streams
	dead           // failed, evicted or superseded; carries nothing again
)

// Stream classes.
const (
	refused     = iota // close it unread
	helloStream        // run hello on it
	peerStream         // serve it for the bound peer
)

func newAdmission(k *Key, admit AdmitFunc, handle HandleFunc) *admission {
	return &admission{key: k, admit: admit, handle: handle, tunnels: map[[32]byte]*tunnel{}}
}

// serve takes one accepted stream from the tunnel whose client key is c.
func (a *admission) serve(conn net.Conn, c [32]byte) {
	peer, class := a.classify(c, conn)
	switch class {
	case peerStream:
		a.serveBound(stream.NewConn(conn), peer)
	case helloStream:
		conn.SetDeadline(time.Now().Add(helloTimeout))
		a.hello(stream.NewConn(conn), c)
		conn.Close()
	default:
		conn.Close()
		return
	}
	a.leave(c, conn)
}

// classify decides what a new stream is and registers it in one step: the
// first stream of an unbound key starts its hello, and a bound tunnel's
// stream joins those retirement closes. A hello beyond the budget evicts the
// oldest pending one first.
func (a *admission) classify(c [32]byte, conn net.Conn) (peer string, class int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	t, seen := a.tunnels[c]
	switch {
	case !seen:
		if len(a.pending) == maxUnbound {
			a.kill(a.pending[0])
		}
		a.tunnels[c] = &tunnel{state: pending, hello: conn}
		a.pending = append(a.pending, c)
		return "", helloStream
	case t.state == bound:
		t.streams[conn] = true
		return t.peer, peerStream
	}
	return "", refused
}

// finish settles c's hello once verification, done outside the lock, has a
// verdict. Only a record still pending takes it: a pass leaves the budget
// and binds in one step, superseding the peer's older tunnel so exactly one
// tunnel carries its opens; a refusal fails c.
func (a *admission) finish(c [32]byte, peer, reason string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	t := a.tunnels[c]
	if t.state != pending {
		return // evicted while it verified
	}
	a.dequeue(c)
	if reason != "" {
		*t = tunnel{state: dead}
		return
	}
	a.killPeer(peer)
	*t = tunnel{state: bound, peer: peer, streams: map[net.Conn]bool{}}
}

// drop fails the tunnels bound to peer, a revoked member.
func (a *admission) drop(peer string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.killPeer(peer)
}

// retireConn fails the bound tunnel a stream arrived on.
func (a *admission) retireConn(conn net.Conn) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for c, t := range a.tunnels {
		if t.streams[conn] {
			a.kill(c)
			return
		}
	}
}

// clientOf is the client key of the tunnel bound to peer.
func (a *admission) clientOf(peer string) ([32]byte, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for c, t := range a.tunnels {
		if t.peer == peer {
			return c, true
		}
	}
	return [32]byte{}, false
}

func (a *admission) killPeer(peer string) {
	for c, t := range a.tunnels {
		if t.peer == peer { // only a bound record has a peer
			a.kill(c)
		}
	}
}

// kill fails c's record, detaching and closing its connections: a pending
// tunnel's hello, or a bound tunnel's streams.
func (a *admission) kill(c [32]byte) {
	t := a.tunnels[c]
	if t.hello != nil {
		t.hello.Close()
	}
	for conn := range t.streams {
		conn.Close()
	}
	a.dequeue(c)
	*t = tunnel{state: dead}
}

// leave unregisters a stream that classify admitted. A hello that ends while
// its record is still pending had no verdict, so its key is unbound again.
func (a *admission) leave(c [32]byte, conn net.Conn) {
	a.mu.Lock()
	defer a.mu.Unlock()
	switch t := a.tunnels[c]; t.state {
	case pending:
		a.dequeue(c)
		delete(a.tunnels, c)
	case bound:
		delete(t.streams, conn)
	}
}

func (a *admission) dequeue(c [32]byte) {
	if i := slices.Index(a.pending, c); i >= 0 {
		a.pending = slices.Delete(a.pending, i, i+1)
	}
}

func (a *admission) serveBound(sc *stream.Conn, peer string) {
	var h stream.Header
	sc.SetReadDeadline(time.Now().Add(helloTimeout))
	if sc.ReadLine(&h) != nil {
		sc.Close()
		return
	}
	sc.SetReadDeadline(time.Time{})
	if h.V != 1 {
		_ = sc.WriteLine(stream.Response{Reason: "version"}) // closing either way
		sc.Close()
		return
	}
	a.handle(peer, h, sc)
}

// hello runs the acceptor's half of hello (docs/03, Admission).
func (a *admission) hello(sc *stream.Conn, c [32]byte) {
	var h stream.Header
	if sc.ReadLine(&h) != nil {
		return
	}
	if reason := firstStreamRefusal(h); reason != "" {
		_ = sc.WriteLine(stream.Response{Reason: reason}) // closing either way
		return
	}
	if sc.WriteLine(stream.Response{OK: true}) != nil {
		return
	}
	typ, p, err := sc.ReadFrame()
	if err != nil {
		return
	}
	peer, reason := a.verify(typ, p, c)
	if a.hook != nil {
		a.hook("admitted", c)
	}
	a.finish(c, peer, reason)
	if a.hook != nil {
		a.hook("finished", c)
	}
	writeResult(sc, reason) // an evicted hello never hears it: eviction closed its stream
}

// firstStreamRefusal is why a tunnel's first stream is refused, if it is.
func firstStreamRefusal(h stream.Header) string {
	switch {
	case h.V != 1:
		return "version"
	case h.Kind != "hello":
		return "unauthenticated"
	}
	return ""
}

// verify checks membership, then possession of the key the entry names, with
// the client key taken from the tunnel rather than the frame.
func (a *admission) verify(typ stream.Type, p []byte, c [32]byte) (peer, reason string) {
	var f helloFrame
	if typ != stream.Data || json.Unmarshal(p, &f) != nil {
		return "", "bad-entry"
	}
	peer, s, reason := a.admit(f.Entry)
	if reason != "" {
		return "", reason
	}
	mac, err := possessionMAC(a.key.pk.Private.Raw32(), s, c, s, a.key.NodePublic(), f.Nonce)
	if err != nil || len(f.Nonce) != 32 || !hmac.Equal(mac, f.MAC) {
		return "", "possession"
	}
	return peer, ""
}

func writeResult(sc *stream.Conn, reason string) {
	b, _ := json.Marshal(stream.Response{OK: reason == "", Reason: reason})
	_ = sc.WriteFrame(stream.Data, b) // closing either way
}
