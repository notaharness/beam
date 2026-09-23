package control

import (
	"encoding/base64"
	"encoding/json"
	"os"

	"github.com/notaharness/beam/internal/identity"
	"github.com/notaharness/beam/internal/mailbox"
	"github.com/notaharness/beam/internal/store"
	"github.com/notaharness/beam/internal/stream"
)

// maxShells is the pty and exec limit per peer (docs/04).
const maxShells = 32

// granted is one inbound stream the peer's grant governs.
type granted struct {
	c    *stream.Conn
	kind string
}

// handle serves a stream an admitted peer opened here, unless e is ending.
// Revocation and the grant are read per open.
func (d *daemon) handle(e *enrolment, peerID string, h stream.Header, c *stream.Conn) {
	if !e.admitting() {
		c.Close()
		return
	}
	defer e.handlers.Done()
	switch h.Kind {
	case stream.KindSync:
		d.serveSync(e, peerID, c)
	case stream.KindPTY, stream.KindExec, stream.KindMsg:
		d.serveGranted(e, peerID, h, c)
	default:
		refuse(c, "kind")
	}
}

// serveGranted runs a stream the peer's grant governs. A revocation or grant
// change after admitStream closes it, and a stream closed before its process
// starts never starts it.
func (d *daemon) serveGranted(e *enrolment, id string, h stream.Header, c *stream.Conn) {
	p, release, reason := d.admitStream(e, id, h.Kind, c)
	if reason != "" {
		refuse(c, reason)
		return
	}
	defer release()
	d.seen(e, id)
	d.at("admitted", id)
	switch h.Kind {
	case stream.KindMsg:
		if c.WriteLine(stream.Response{OK: true}) == nil {
			mailbox.Serve(c, e.store, id, e.self(), e.mail.Offer)
		}
	case stream.KindPTY:
		stream.PTY(c, h, d.spawn(e, p))
	default:
		stream.Exec(c, h, d.spawn(e, p))
	}
}

func refuse(c *stream.Conn, reason string) {
	_ = c.WriteLine(stream.Response{Reason: reason}) // closing either way
	c.Close()
}

// seen records that peerID was heard from; the time is advisory.
func (d *daemon) seen(e *enrolment, peerID string) {
	if err := e.store.Seen(peerID, now()); err != nil {
		d.o.Logf("seen %s: %v", peerID[:8], err)
	}
}

// grantOf reads a stored grant; one that cannot be parsed is none.
func grantOf(g string) string {
	switch g {
	case store.GrantAll, store.GrantMsg:
		return g
	}
	return store.GrantNone
}

// allows reports whether grant lets a peer open kind here (docs/04).
func allows(grant, kind string) bool {
	switch grantOf(grant) {
	case store.GrantAll:
		return true
	case store.GrantMsg:
		return kind == stream.KindMsg
	}
	return false
}

// admitStream checks an inbound stream (its peer known and not revoked,
// within the grant, and within the limit: 32 pty and exec streams, and one
// msg stream, which keeps a sender's envelopes in order) and registers it in
// one step under d.mu, which peer.grant also takes; a revocation committed
// before the check refuses it. It returns the peer's row and release, which
// unregisters the stream, or why it is refused.
func (d *daemon) admitStream(e *enrolment, id, kind string, c *stream.Conn) (p store.Peer, release func(), reason string) {
	g := &granted{c, kind}
	d.mu.Lock()
	defer d.mu.Unlock()
	p, ok, err := e.store.Peer(id)
	switch {
	case e != d.en || err != nil || !ok || p.Revoked:
		return p, nil, "revoked"
	case !allows(p.Grant, kind):
		return p, nil, "grant"
	case d.countLocked(id, kind) >= limits[kind]:
		return p, nil, "limit"
	}
	if d.granted[id] == nil {
		d.granted[id] = map[*granted]bool{}
	}
	d.granted[id][g] = true
	return p, func() {
		d.mu.Lock()
		delete(d.granted[id], g)
		d.mu.Unlock()
	}, ""
}

// limits are how many streams of a kind a peer may have open here; pty and
// exec count together.
var limits = map[string]int{stream.KindPTY: maxShells, stream.KindExec: maxShells, stream.KindMsg: 1}

// countLocked is how many of the peer's open streams count against kind's
// limit. d.mu must be held.
func (d *daemon) countLocked(id, kind string) int {
	n := 0
	for g := range d.granted[id] {
		if (g.kind == stream.KindMsg) == (kind == stream.KindMsg) {
			n++
		}
	}
	return n
}

// spawn is the environment for a process a peer runs here (docs/04).
func (d *daemon) spawn(e *enrolment, p store.Peer) stream.Spawn {
	home, _ := os.UserHomeDir()
	label := p.Entry.Label
	if p.Alias != nil {
		label = *p.Alias
	}
	return stream.Spawn{
		Env:  os.Environ(),
		Home: home,
		Inject: map[string]string{
			"BEAM_DIR":          d.o.Paths.Dir,
			"BEAM_SOCKET":       d.o.Paths.Socket,
			"BEAM_PEER_ID":      e.self(),
			"BEAM_CALLER_ID":    p.Entry.PeerID,
			"BEAM_CALLER_LABEL": label,
		},
	}
}

// closeUngrantedLocked closes the peer's inbound streams grant no longer
// allows; the stream code then kills their processes. d.mu must be held.
func (d *daemon) closeUngrantedLocked(peerID, grant string) {
	for g := range d.granted[peerID] {
		if !allows(grant, g.kind) {
			g.c.Close()
		}
	}
}

func decodeKey(s string) ([32]byte, bool) {
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil || len(b) != 32 {
		return [32]byte{}, false
	}
	return [32]byte(b), true
}

// admit is transport's hello check: verify the entry. Once the dialer has
// proven it holds the entry's key, pin the entry if new and dial back;
// contact from a known peer resets the backoff.
func (d *daemon) admit(e *enrolment, raw json.RawMessage) (string, [32]byte, func(), string) {
	r, err := identity.ParseRecord(raw)
	if err == nil && r.Kind != identity.Member {
		err = identity.BadEntry
	}
	if err == nil {
		err = e.cred.Verify(r, e.isRevoked)
	}
	if err != nil {
		return "", [32]byte{}, nil, err.Error()
	}
	pub, _ := decodeKey(r.NodePublic)
	return r.PeerID, pub, func() {
		d.at("admitting", r.PeerID)
		if d.pin(e, r) {
			d.broadcast(e, r, nil)
		} else {
			d.mu.Lock()
			d.startDialerLocked(e, r.PeerID) // inbound contact resets the backoff
			d.mu.Unlock()
		}
	}, ""
}
