package control

import (
	"encoding/base64"
	"os"

	"github.com/notaharness/beam/internal/store"
	"github.com/notaharness/beam/internal/stream"
)

// maxShells is the pty and exec limit per peer (docs/04).
const maxShells = 32

// shell is one inbound pty or exec stream.
type shell struct {
	c *stream.Conn
}

// handle serves a stream an admitted peer opened here. Revocation and the
// grant are read per open.
func (d *daemon) handle(peerID string, h stream.Header, c *stream.Conn) {
	p, ok, err := d.store.Peer(peerID)
	if err != nil || !ok || p.Revoked {
		refuse(c, "revoked")
		return
	}
	d.seen(peerID)
	switch h.Kind {
	case stream.KindSync:
		d.serveSync(peerID, c)
	case stream.KindPTY, stream.KindExec:
		d.serveShell(p, h, c)
	default:
		refuse(c, "kind")
	}
}

func refuse(c *stream.Conn, reason string) {
	_ = c.WriteLine(stream.Response{Reason: reason}) // closing either way
	c.Close()
}

// seen records that peerID was heard from; the time is advisory.
func (d *daemon) seen(peerID string) {
	if err := d.store.Seen(peerID, now()); err != nil {
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

func (d *daemon) serveShell(p store.Peer, h stream.Header, c *stream.Conn) {
	id := p.Entry.PeerID
	sh := &shell{c}
	d.mu.Lock()
	cur, _, err := d.store.Peer(id) // under the lock, so peer.grant cannot slip between check and register
	switch {
	case err != nil || grantOf(cur.Grant) != store.GrantAll:
		d.mu.Unlock()
		refuse(c, "grant")
		return
	case len(d.shells[id]) >= maxShells:
		d.mu.Unlock()
		refuse(c, "limit")
		return
	}
	if d.shells[id] == nil {
		d.shells[id] = map[*shell]bool{}
	}
	d.shells[id][sh] = true
	d.mu.Unlock()
	defer func() {
		d.mu.Lock()
		delete(d.shells[id], sh)
		d.mu.Unlock()
	}()
	sp := d.spawn(p)
	if h.Kind == stream.KindPTY {
		stream.PTY(c, h, sp)
	} else {
		stream.Exec(c, h, sp)
	}
}

// spawn is the environment for a process a peer runs here (docs/04).
func (d *daemon) spawn(p store.Peer) stream.Spawn {
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
			"BEAM_PEER_ID":      d.fleet.Entry.PeerID,
			"BEAM_CALLER_ID":    p.Entry.PeerID,
			"BEAM_CALLER_LABEL": label,
		},
	}
}

// closeShellsLocked closes the peer's inbound pty and exec streams; the stream
// code then kills their processes. d.mu must be held.
func (d *daemon) closeShellsLocked(peerID string) {
	for sh := range d.shells[peerID] {
		sh.c.Close()
	}
}

func decodeKey(s string) ([32]byte, bool) {
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil || len(b) != 32 {
		return [32]byte{}, false
	}
	return [32]byte(b), true
}
