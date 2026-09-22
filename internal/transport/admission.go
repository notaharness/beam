package transport

import (
	"crypto/hmac"
	"encoding/json"
	"net"
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

// admission binds each tunnel's client key to a peer through hello.
type admission struct {
	key    *Key
	admit  AdmitFunc
	handle HandleFunc

	mu      sync.Mutex
	bound   map[[32]byte]string // client key → peer id
	failed  map[[32]byte]bool   // client keys whose hello was refused
	pending []unbound           // tunnels in hello, oldest first
}

type unbound struct {
	c    [32]byte
	conn net.Conn
}

func newAdmission(k *Key, admit AdmitFunc, handle HandleFunc) *admission {
	return &admission{key: k, admit: admit, handle: handle,
		bound: map[[32]byte]string{}, failed: map[[32]byte]bool{}}
}

// serve takes one accepted stream from the tunnel whose client key is c.
func (a *admission) serve(conn net.Conn, c [32]byte) {
	a.mu.Lock()
	peer, ok := a.bound[c]
	a.mu.Unlock()
	if ok {
		a.serveBound(stream.NewConn(conn), peer)
		return
	}
	defer conn.Close()
	if !a.enter(c, conn) {
		return
	}
	defer a.leave(conn)
	conn.SetDeadline(time.Now().Add(helloTimeout))
	a.hello(stream.NewConn(conn), c)
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

// enter registers an unbound tunnel's hello, closing the oldest one beyond the
// budget. It refuses a tunnel whose hello failed or is already under way.
func (a *admission) enter(c [32]byte, conn net.Conn) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.failed[c] {
		return false
	}
	for _, u := range a.pending {
		if u.c == c {
			return false
		}
	}
	if len(a.pending) == maxUnbound {
		a.pending[0].conn.Close()
		a.pending = a.pending[1:]
	}
	a.pending = append(a.pending, unbound{c, conn})
	return true
}

func (a *admission) leave(conn net.Conn) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for i, u := range a.pending {
		if u.conn == conn {
			a.pending = append(a.pending[:i], a.pending[i+1:]...)
			return
		}
	}
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
	if err != nil || typ != stream.Data {
		a.fail(c)
		return
	}
	peer, reason := a.verify(p, c)
	if reason != "" {
		a.fail(c)
		writeResult(sc, reason)
		return
	}
	a.bind(c, peer)
	writeResult(sc, "")
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
func (a *admission) verify(p []byte, c [32]byte) (peer, reason string) {
	var f helloFrame
	if json.Unmarshal(p, &f) != nil {
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

func (a *admission) fail(c [32]byte) {
	a.mu.Lock()
	a.failed[c] = true
	a.mu.Unlock()
}

// bind attributes the tunnel to peer. A peer has one dialed tunnel to us per
// process, so an older binding for it belongs to a process that is gone.
func (a *admission) bind(c [32]byte, peer string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for old, p := range a.bound {
		if p == peer {
			delete(a.bound, old)
		}
	}
	a.bound[c] = peer
}

func writeResult(sc *stream.Conn, reason string) {
	b, _ := json.Marshal(stream.Response{OK: reason == "", Reason: reason})
	_ = sc.WriteFrame(stream.Data, b) // closing either way
}
