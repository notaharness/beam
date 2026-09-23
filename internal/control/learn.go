package control

import (
	"encoding/json"

	"github.com/notaharness/beam/internal/identity"
)

// learn verifies a record from any source and applies what is new: a
// revocation ends the peer here; a member is pinned (or superseded) and
// dialed. What is new is pushed on to every connected peer.
func (d *daemon) learn(e *enrolment, raw json.RawMessage) {
	r, err := identity.ParseRecord(raw)
	if err != nil || e.cred.Verify(r, e.isRevoked) != nil {
		return
	}
	d.at("storing", r.PeerID)
	var changed bool
	if r.Kind == identity.Revoke {
		changed = d.revoke(e, r)
		if changed {
			d.at("revoking", r.PeerID)
			d.applyRevocation(e, r.PeerID)
		}
	} else if r.PeerID != e.self() {
		changed = d.pin(e, r)
	}
	if changed {
		d.broadcast(e, r, nil)
	}
}

// revoke stores a verified revocation while e is the enrolment, and reports
// whether it was new. The check and the write are one step under d.mu, which
// unenroll takes to end e before it empties state.db: what an ended enrolment
// verified never reaches the store after that.
func (d *daemon) revoke(e *enrolment, r identity.Record) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	if e != d.en {
		return false
	}
	changed, err := e.store.Revoke(r, now())
	return err == nil && changed
}

// pin stores a verified member and dials it: at its new address, if it
// superseded the pinned entry with one. It reports whether it was new or
// superseded the pinned entry. Like revoke, it writes only while e is the
// enrolment, in one step under d.mu.
func (d *daemon) pin(e *enrolment, r identity.Record) bool {
	d.mu.Lock()
	if e != d.en {
		d.mu.Unlock()
		return false
	}
	old, known, _ := e.store.Peer(r.PeerID)
	changed, err := e.store.Pin(r, now())
	if err != nil || !changed {
		d.mu.Unlock()
		return false
	}
	if known && old.Entry.Address != r.Address {
		d.endDialerLocked(r.PeerID)
	}
	d.startDialerLocked(e, r.PeerID)
	d.mu.Unlock()
	if !known {
		d.emit("peer.new", d.peerView(e, r.PeerID))
	} else {
		d.emitPeer(e, r.PeerID)
	}
	return true
}

func (e *enrolment) isRevoked(peerID string) bool {
	r, err := e.store.IsRevoked(peerID)
	return r || err != nil
}

// applyRevocation ends everything with a revoked peer on this machine, while
// e is the enrolment: an ended one's store is gone, and its peer ids may be
// another fleet's now.
func (d *daemon) applyRevocation(e *enrolment, peerID string) {
	d.mu.Lock()
	if e != d.en {
		d.mu.Unlock()
		return
	}
	d.stopPeerLocked(peerID)
	d.mu.Unlock()
	e.node.Drop(peerID) // its inbound tunnel, and every stream on it
	d.emitPeer(e, peerID)
}

// broadcast pushes a record of e's on every live sync stream, while e is the
// enrolment, and returns how many it went to; acked, when set, hears from
// each peer that read it and needs room for them all. A peer whose buffer is
// full loses its tunnel, and the reconnect's full dump carries the record.
func (d *daemon) broadcast(e *enrolment, r identity.Record, acked chan<- struct{}) int {
	d.mu.Lock()
	defer d.mu.Unlock()
	if e != d.en {
		return 0
	}
	n := 0
	for _, ps := range d.peers {
		if ps.deltas == nil {
			continue
		}
		select {
		case ps.deltas <- delta{r, acked}:
			n++
		default:
			ps.tunnel.Close()
		}
	}
	return n
}
