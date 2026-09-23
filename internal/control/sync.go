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

// delta is a record to push on a sync stream. acked, when set, hears once
// the peer has read it: the stream is ordered, so the pong to a ping sent
// after the record proves the peer read the record first.
type delta struct {
	r     identity.Record
	acked chan<- struct{}
}

// runSync is the dialer's sync stream, from the dump on.
func (d *daemon) runSync(ctx context.Context, e *enrolment, peerID string, ps *peerState, tun *transport.Tunnel) {
	octx, cancel := context.WithTimeout(ctx, dialTimeout)
	sc, err := tun.Open(octx, stream.Header{V: 1, Kind: stream.KindSync})
	cancel()
	if err != nil {
		d.setState(ps, stateOffline, nil, nil)
		return
	}
	defer sc.Close()
	deltas := make(chan delta, deltaBuffer)
	d.capture(ps, tun, deltas)
	dump, err := e.store.Records()
	if err != nil {
		d.setState(ps, stateOffline, nil, nil)
		return
	}
	d.at("dumped", peerID)
	fctx, stopFlush := context.WithCancel(ctx)
	up := false
	pushSync(ctx, sc, append(dump, e.fleet.Entry), deltas, func() {
		up = true
		d.setState(ps, stateConnected, tun, deltas)
		d.seen(e, peerID)
		d.emitPeer(e, peerID)
		go d.flush(fctx, e, peerID, ps, tun)
	})
	stopFlush()
	d.setState(ps, stateOffline, nil, nil)
	if up {
		d.emitPeer(e, peerID)
	}
}

// pushSync sends the dump (every record this machine holds, its own entry
// included) and "end", calls up, then sends deltas and a ping every 15 s
// until ctx ends or the stream fails. Every pong moves the write deadline
// 30 s on, so the ping after 30 s without one fails, and so does a write the
// peer never takes; ctx's end closes the stream under a blocked write too.
func pushSync(ctx context.Context, sc *stream.Conn, dump []identity.Record, deltas chan delta, up func()) {
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
	pongs, stop := make(chan int64, 16), make(chan struct{})
	defer close(stop)
	go readPongs(sc, pongs, stop)
	ping := time.NewTicker(pingEvery)
	defer ping.Stop()
	acks := ackPings{}
	for {
		var err error
		select {
		case <-ctx.Done():
			return
		case dl := <-deltas:
			err = acks.push(sc, dl)
		case t, ok := <-pongs:
			if !ok {
				return
			}
			sc.SetWriteDeadline(time.Now().Add(pongTimeout))
			acks.pong(t)
		case t := <-ping.C:
			err = sc.WriteJSON(stream.Control, stream.Ctl{Kind: "ping", T: t.UnixMilli()})
		}
		if err != nil {
			return
		}
	}
}

// ackPings are the deltas waiting to hear the peer read them, by the ping
// sent after each.
type ackPings map[int64]chan<- struct{}

// push sends a delta, and a ping after it when it waits for an ack.
func (a ackPings) push(sc *stream.Conn, dl delta) error {
	err := sendRecords(sc, []identity.Record{dl.r})
	if err != nil || dl.acked == nil {
		return err
	}
	t := time.Now().UnixNano()
	a[t] = dl.acked
	return sc.WriteJSON(stream.Control, stream.Ctl{Kind: "ping", T: t})
}

// pong acks the delta the ping answered was sent after.
func (a ackPings) pong(t int64) {
	if c, ok := a[t]; ok {
		c <- struct{}{} // it has room for every peer the delta went to
		delete(a, t)
	}
}

// readPongs hands pushSync the time of each pong until the stream ends or
// stop closes.
func readPongs(sc *stream.Conn, pongs chan<- int64, stop <-chan struct{}) {
	defer close(pongs)
	for {
		t, p, err := sc.ReadFrame()
		if err != nil || t == stream.Close {
			return
		}
		var ctl stream.Ctl
		if t == stream.Control && json.Unmarshal(p, &ctl) == nil && ctl.Kind == "pong" {
			select {
			case pongs <- ctl.T:
			case <-stop:
				return
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
func (d *daemon) serveSync(e *enrolment, peerID string, sc *stream.Conn) {
	defer sc.Close()
	defer e.node.Retire(sc)
	if e.isRevoked(peerID) {
		refuse(sc, "revoked")
		return
	}
	d.seen(e, peerID)
	if sc.WriteLine(stream.Response{OK: true}) != nil {
		return
	}
	d.mu.Lock()
	if e != d.en {
		d.mu.Unlock()
		return
	}
	d.inbound[peerID]++
	d.mu.Unlock()
	d.emitPeer(e, peerID)
	defer func() {
		d.mu.Lock()
		d.inbound[peerID]--
		d.mu.Unlock()
		d.emitPeer(e, peerID)
	}()
	for {
		sc.SetReadDeadline(time.Now().Add(pongTimeout))
		t, p, err := sc.ReadFrame()
		if err != nil || t == stream.Close {
			return
		}
		d.syncFrameIn(e, sc, t, p)
	}
}

func (d *daemon) syncFrameIn(e *enrolment, sc *stream.Conn, t stream.Type, p []byte) {
	switch t {
	case stream.Data:
		var f syncFrame
		if json.Unmarshal(p, &f) != nil || len(f.Records) > maxRecordsPerFrame {
			return
		}
		for _, r := range f.Records {
			d.learn(e, r)
		}
	case stream.Control:
		var ctl stream.Ctl
		if json.Unmarshal(p, &ctl) == nil && ctl.Kind == "ping" {
			_ = sc.WriteJSON(stream.Control, stream.Ctl{Kind: "pong", T: ctl.T}) // a lost stream ends the read loop
		}
	}
}
