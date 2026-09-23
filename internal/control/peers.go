package control

import (
	"context"
	"errors"
	"math/rand/v2"
	"time"

	"github.com/notaharness/beam/internal/transport"
)

// Lifecycle numbers (docs/03).
const (
	dialTimeout = 20 * time.Second
	minBackoff  = 2 * time.Second
	maxBackoff  = 5 * time.Minute
	pingEvery   = 15 * time.Second
	pongTimeout = 30 * time.Second
)

// Peer states (docs/03). revoked-by-fleet is the peer refusing this
// machine's hello as revoked: advice, not a record, but that peer will not
// take this machine again, so it is not dialed again while the daemon runs.
const (
	stateConnected      = "connected"
	stateOffline        = "offline"
	stateRevoked        = "revoked"
	stateRevokedByFleet = "revoked-by-fleet"
)

// peerState is this machine's dialed side of one peer.
type peerState struct {
	state   string
	tunnel  *transport.Tunnel // the dialed tunnel, from its dump on
	deltas  chan delta        // records to push on its sync stream
	kick    chan struct{}     // resets the backoff
	wake    chan struct{}     // tells the flusher there is mail
	changed chan struct{}     // closed and replaced on every state change
	cancel  context.CancelFunc
}

// startDialerLocked starts dialing a peer on e unless it is this machine,
// already dialed (then it resets the backoff) or e is no longer the
// enrolment. d.mu must be held.
func (d *daemon) startDialerLocked(e *enrolment, peerID string) {
	if e != d.en || peerID == e.self() {
		return
	}
	if ps, ok := d.peers[peerID]; ok {
		d.kickLocked(ps)
		return
	}
	ctx, cancel := context.WithCancel(e.ctx)
	ps := &peerState{state: stateOffline, kick: make(chan struct{}, 1), wake: make(chan struct{}, 1),
		changed: make(chan struct{}), cancel: cancel}
	d.peers[peerID] = ps
	go d.dialLoop(ctx, e, peerID, ps)
}

func (d *daemon) kickLocked(ps *peerState) {
	select {
	case ps.kick <- struct{}{}:
	default:
	}
}

func (d *daemon) setState(ps *peerState, state string, tun *transport.Tunnel, deltas chan delta) {
	d.mu.Lock()
	if ps.state != stateRevoked {
		ps.state, ps.tunnel, ps.deltas = state, tun, deltas
	}
	close(ps.changed)
	ps.changed = make(chan struct{})
	d.mu.Unlock()
}

// capture queues deltas for a tunnel before its dump's snapshot is taken, so
// every record reaches the peer in one or the other, and a queue that fills
// meanwhile fails the tunnel. The attempt has no outcome yet: nothing waiting
// on the peer's state wakes.
func (d *daemon) capture(ps *peerState, tun *transport.Tunnel, deltas chan delta) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if ps.state != stateRevoked {
		ps.tunnel, ps.deltas = tun, deltas
	}
}

// dialLoop keeps a tunnel to the peer up: dial, hello, sync; retry with
// jittered exponential backoff forever, reset by a kick.
func (d *daemon) dialLoop(ctx context.Context, e *enrolment, peerID string, ps *peerState) {
	backoff := minBackoff
	for ctx.Err() == nil {
		if d.tryPeer(ctx, e, peerID, ps) {
			backoff = minBackoff
		}
		wait := backoff/2 + rand.N(backoff)
		select {
		case <-ctx.Done():
			return
		case <-ps.kick:
			backoff = minBackoff
		case <-time.After(wait):
			backoff = min(2*backoff, maxBackoff)
		}
	}
}

// tryPeer dials once and, if hello succeeds, runs the tunnel until it dies. It
// reports whether the tunnel came up.
func (d *daemon) tryPeer(ctx context.Context, e *enrolment, peerID string, ps *peerState) bool {
	p, ok, err := e.store.Peer(peerID)
	if err != nil || !ok || p.Revoked {
		return false
	}
	dctx, cancel := context.WithTimeout(ctx, dialTimeout)
	tun, err := e.node.Dial(dctx, p.Entry.Address)
	cancel()
	var ref *transport.Refused
	switch {
	case errors.As(err, &ref) && ref.Reason == "revoked":
		d.o.Logf("%s refuses this machine as revoked; not dialing it again", peerID[:8])
		d.setState(ps, stateRevokedByFleet, nil, nil)
		ps.cancel()
		return false
	case err != nil:
		d.o.Logf("dial %s: %v", peerID[:8], err)
		d.setState(ps, stateOffline, nil, nil)
		return false
	}
	defer tun.Close()
	d.runSync(ctx, e, peerID, ps, tun)
	return true
}

// tunnelTo returns the live tunnel to a peer, while e is the enrolment: an
// ended one's peer ids may be another fleet's now. With none it asks the dial
// loop for an attempt now and reports its outcome, waiting at most until ctx
// ends.
func (d *daemon) tunnelTo(ctx context.Context, e *enrolment, peerID string) (*transport.Tunnel, bool) {
	d.mu.Lock()
	ps, ok := d.peers[peerID]
	ok = ok && e == d.en
	if !ok || ps.state == stateConnected || ps.state == stateRevokedByFleet {
		defer d.mu.Unlock()
		return ps.tunnelIfOK(ok)
	}
	d.kickLocked(ps)
	changed := ps.changed
	d.mu.Unlock()
	select {
	case <-changed:
	case <-ctx.Done():
		return nil, false
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	return ps.tunnelIfOK(e == d.en)
}

func (ps *peerState) tunnelIfOK(ok bool) (*transport.Tunnel, bool) {
	if !ok || ps.state != stateConnected {
		return nil, false
	}
	return ps.tunnel, true
}

// endDialerLocked ends dialing a peer, its tunnel included, so that a new
// dialer can take the peer's new address. d.mu must be held.
func (d *daemon) endDialerLocked(peerID string) {
	if ps, ok := d.peers[peerID]; ok {
		ps.cancel()
		if ps.tunnel != nil {
			ps.tunnel.Close()
		}
		delete(d.peers, peerID)
	}
}

// stopPeerLocked ends dialing a revoked peer. d.mu must be held.
func (d *daemon) stopPeerLocked(peerID string) {
	if ps, ok := d.peers[peerID]; ok {
		ps.cancel()
		if ps.tunnel != nil {
			ps.tunnel.Close()
		}
		ps.state, ps.tunnel, ps.deltas = stateRevoked, nil, nil // no delta goes to it again
		close(ps.changed)
		ps.changed = make(chan struct{})
	}
}

// revokedByFleet reports whether a peer refused this machine's hello as
// revoked.
func (d *daemon) revokedByFleet(peerID string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	ps, ok := d.peers[peerID]
	return ok && ps.state == stateRevokedByFleet
}
