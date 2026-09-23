package control

import (
	"context"
	"encoding/json"
	"time"

	"github.com/notaharness/beam/internal/identity"
	"github.com/notaharness/beam/internal/stream"
	"github.com/notaharness/beam/internal/transport"
)

// Sync frame limits (docs/02, Sync).
const (
	maxRecordsPerFrame = 200
	deltaBuffer        = 256
)

type syncFrame struct {
	Records []json.RawMessage `json:"records"`
}

// runSync is the dialer's sync stream, from the dump on.
func (d *daemon) runSync(ctx context.Context, peerID string, ps *peerState, tun *transport.Tunnel) {
	octx, cancel := context.WithTimeout(ctx, dialTimeout)
	sc, err := tun.Open(octx, stream.Header{V: 1, Kind: stream.KindSync})
	cancel()
	if err != nil {
		d.setState(ps, stateOffline, nil, nil)
		return
	}
	defer sc.Close()
	deltas := make(chan identity.Record, deltaBuffer)
	d.capture(ps, tun, deltas)
	dump, err := d.store.Records()
	if err != nil {
		d.setState(ps, stateOffline, nil, nil)
		return
	}
	d.at("dumped", peerID)
	up := false
	pushSync(ctx, sc, append(dump, d.fleet.Entry), deltas, func() {
		up = true
		d.setState(ps, stateConnected, tun, deltas)
		d.seen(peerID)
		d.emitPeer(peerID)
	})
	d.setState(ps, stateOffline, nil, nil)
	if up {
		d.emitPeer(peerID)
	}
}

// pushSync sends the dump (every record this machine holds, its own entry
// included) and "end", calls up, then sends deltas and a ping every 15 s
// until ctx ends or the stream fails. Every pong moves the write deadline
// 30 s on, so the ping after 30 s without one fails, and so does a write the
// peer never takes; ctx's end closes the stream under a blocked write too.
func pushSync(ctx context.Context, sc *stream.Conn, dump []identity.Record, deltas chan identity.Record, up func()) {
	defer context.AfterFunc(ctx, func() { sc.Close() })()
	sc.SetWriteDeadline(time.Now().Add(pongTimeout))
	for i := 0; i < len(dump); i += maxRecordsPerFrame {
		if sendRecords(sc, dump[i:min(i+maxRecordsPerFrame, len(dump))]) != nil {
			return
		}
	}
	if sc.WriteJSON(stream.Control, stream.Ctl{Kind: "end"}) != nil {
		return
	}
	up()
	pongs := make(chan struct{}, 1)
	go readPongs(sc, pongs)
	ping := time.NewTicker(pingEvery)
	defer ping.Stop()
	for {
		var err error
		select {
		case <-ctx.Done():
			return
		case r := <-deltas:
			err = sendRecords(sc, []identity.Record{r})
		case _, ok := <-pongs:
			if !ok {
				return
			}
			sc.SetWriteDeadline(time.Now().Add(pongTimeout))
		case t := <-ping.C:
			err = sc.WriteJSON(stream.Control, stream.Ctl{Kind: "ping", T: t.UnixMilli()})
		}
		if err != nil {
			return
		}
	}
}

func readPongs(sc *stream.Conn, pongs chan struct{}) {
	defer close(pongs)
	for {
		t, p, err := sc.ReadFrame()
		if err != nil || t == stream.Close {
			return
		}
		var ctl stream.Ctl
		if t == stream.Control && json.Unmarshal(p, &ctl) == nil && ctl.Kind == "pong" {
			select {
			case pongs <- struct{}{}:
			default:
			}
		}
	}
}

func sendRecords(sc *stream.Conn, recs []identity.Record) error {
	var f syncFrame
	for _, r := range recs {
		b, _ := json.Marshal(r)
		f.Records = append(f.Records, b)
	}
	return sc.WriteJSON(stream.Data, f)
}

// serveSync is the acceptor's sync stream: verify and apply each record, and
// answer pings. The dialer keeps its tunnel exactly as long as this stream,
// pinging every 15 s, so when the stream ends or falls silent the tunnel is
// gone, and so is every stream it opened here, even one whose own close was
// lost with it.
func (d *daemon) serveSync(peerID string, sc *stream.Conn) {
	defer sc.Close()
	defer d.node.Retire(sc)
	if d.isRevoked(peerID) {
		refuse(sc, "revoked")
		return
	}
	d.seen(peerID)
	if sc.WriteLine(stream.Response{OK: true}) != nil {
		return
	}
	d.mu.Lock()
	d.inbound[peerID]++
	d.mu.Unlock()
	d.emitPeer(peerID)
	defer func() {
		d.mu.Lock()
		d.inbound[peerID]--
		d.mu.Unlock()
		d.emitPeer(peerID)
	}()
	for {
		sc.SetReadDeadline(time.Now().Add(pongTimeout))
		t, p, err := sc.ReadFrame()
		if err != nil || t == stream.Close {
			return
		}
		d.syncFrameIn(sc, t, p)
	}
}

func (d *daemon) syncFrameIn(sc *stream.Conn, t stream.Type, p []byte) {
	switch t {
	case stream.Data:
		var f syncFrame
		if json.Unmarshal(p, &f) != nil || len(f.Records) > maxRecordsPerFrame {
			return
		}
		for _, r := range f.Records {
			d.learn(r)
		}
	case stream.Control:
		var ctl stream.Ctl
		if json.Unmarshal(p, &ctl) == nil && ctl.Kind == "ping" {
			_ = sc.WriteJSON(stream.Control, stream.Ctl{Kind: "pong", T: ctl.T}) // a lost stream ends the read loop
		}
	}
}

// learn verifies a record from any source and applies what is new: a
// revocation ends the peer here; a member is pinned (or superseded) and
// dialed. What is new is pushed on to every connected peer.
func (d *daemon) learn(raw json.RawMessage) {
	r, err := identity.ParseRecord(raw)
	if err != nil || d.cred.Verify(r, d.isRevoked) != nil {
		return
	}
	var changed bool
	if r.Kind == identity.Revoke {
		changed, _ = d.store.Revoke(r, now())
		if changed {
			d.at("revoking", r.PeerID)
			d.applyRevocation(r.PeerID)
		}
	} else if r.PeerID != d.fleet.Entry.PeerID {
		changed = d.pin(r)
	}
	if changed {
		d.broadcast(r)
	}
}

// pin stores a verified member and dials it. It reports whether it was new or
// superseded the pinned entry.
func (d *daemon) pin(r identity.Record) bool {
	_, known, _ := d.store.Peer(r.PeerID)
	changed, err := d.store.Pin(r, now())
	if err != nil || !changed {
		return false
	}
	d.mu.Lock()
	d.startDialerLocked(r.PeerID)
	d.mu.Unlock()
	if !known {
		d.emit("peer.new", d.peerView(r.PeerID))
	} else {
		d.emitPeer(r.PeerID)
	}
	return true
}

func (d *daemon) isRevoked(peerID string) bool {
	r, err := d.store.IsRevoked(peerID)
	return r || err != nil
}

// applyRevocation ends everything with a revoked peer on this machine.
func (d *daemon) applyRevocation(peerID string) {
	d.mu.Lock()
	d.stopPeerLocked(peerID)
	d.mu.Unlock()
	d.node.Drop(peerID) // its inbound tunnel, and every stream on it
	d.emitPeer(peerID)
}

// broadcast pushes a record on every live sync stream. A peer whose buffer is
// full loses its tunnel, and the reconnect's full dump carries the record.
func (d *daemon) broadcast(r identity.Record) {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, ps := range d.peers {
		if ps.deltas == nil {
			continue
		}
		select {
		case ps.deltas <- r:
		default:
			ps.tunnel.Close()
		}
	}
}

// admit is transport's hello check: verify the entry, pin it if new, and dial
// back; contact from a known peer resets the backoff.
func (d *daemon) admit(raw json.RawMessage) (string, [32]byte, string) {
	r, err := identity.ParseRecord(raw)
	if err == nil && r.Kind != identity.Member {
		err = identity.BadEntry
	}
	if err == nil {
		err = d.cred.Verify(r, d.isRevoked)
	}
	if err != nil {
		return "", [32]byte{}, err.Error()
	}
	if d.pin(r) {
		d.broadcast(r)
	} else {
		d.mu.Lock()
		d.startDialerLocked(r.PeerID) // inbound contact resets the backoff
		d.mu.Unlock()
	}
	pub, _ := decodeKey(r.NodePublic)
	return r.PeerID, pub, ""
}
