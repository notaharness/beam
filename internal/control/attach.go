package control

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"time"

	"github.com/notaharness/beam/internal/stream"
	"github.com/notaharness/beam/internal/transport"
)

// reserveTTL is how long a pty.open or exec.open waits for its attach.
const reserveTTL = 10 * time.Second

// reservation is an opened stream not yet attached: nothing has been sent to
// the peer.
type reservation struct {
	peer  string
	h     stream.Header
	timer *time.Timer
}

func (d *daemon) reserve(peer string, h stream.Header) string {
	b := make([]byte, 16)
	rand.Read(b)
	id := hex.EncodeToString(b)
	r := &reservation{peer: peer, h: h}
	d.mu.Lock()
	defer d.mu.Unlock()
	r.timer = time.AfterFunc(reserveTTL, func() {
		d.mu.Lock()
		delete(d.reservations, id)
		d.mu.Unlock()
	})
	d.reservations[id] = r
	return id
}

// attach runs an attach connection: dial the peer if needed, open the remote
// stream, and pump frames until it ends. stream.close and the client's
// departure detach it at any point, and one that is detached before the
// remote open sends nothing. Every end reaches the client as a close frame.
func (d *daemon) attach(ac *stream.Conn, id string) {
	defer ac.Close()
	ctx, cancel := context.WithCancelCause(d.ctx)
	detach := func() { cancel(errDetached) }
	defer detach()
	d.mu.Lock()
	r, ok := d.reservations[id]
	if ok {
		r.timer.Stop()
		delete(d.reservations, id)
		d.active[id] = detach
	}
	d.mu.Unlock()
	if !ok {
		_ = ac.WriteJSON(stream.Close, stream.CloseMsg{Reason: "params", Detail: "no such stream, or it expired"}) // closing either way
		return
	}
	defer func() {
		d.mu.Lock()
		delete(d.active, id)
		d.mu.Unlock()
	}()
	in := make(chan frame, clientAhead)
	go fromClient(ctx, ac, in, detach)
	d.at("attached", r.peer)
	msg := d.relay(ctx, r, ac, in)
	ac.SetWriteDeadline(time.Now().Add(closeGrace))
	_ = ac.WriteJSON(stream.Close, msg) // closing either way
	d.emit("stream.closed", streamClosed{id, msg.Reason, msg.ExitCode, msg.Signal})
}

type streamClosed struct {
	StreamID string  `json:"streamId"`
	Reason   string  `json:"reason"`
	ExitCode *int    `json:"exitCode,omitempty"`
	Signal   *string `json:"signal,omitempty"`
}

// Attach limits.
const (
	clientAhead = 4           // client frames read ahead of the remote side
	closeGrace  = time.Second // for a client that stopped reading to take its close
)

// errDetached ends an attach that stream.close or its client ended; the
// daemon stopping ends the rest.
var errDetached = errors.New("detached")

// ended is the close of a stream whose attach ctx ended.
func ended(ctx context.Context) stream.CloseMsg {
	if context.Cause(ctx) == errDetached {
		return stream.CloseMsg{Reason: "detached"}
	}
	return stream.CloseMsg{Reason: "connection-lost"}
}

type frame struct {
	t stream.Type
	p []byte
}

// fromClient reads the client's frames into in from attach on, so that its
// departure detaches the stream even before the remote side is open.
func fromClient(ctx context.Context, ac *stream.Conn, in chan<- frame, detach func()) {
	defer close(in)
	defer detach()
	for {
		t, p, err := ac.ReadFrame()
		if err != nil {
			return
		}
		select {
		case in <- frame{t, p}:
		case <-ctx.Done():
			return
		}
	}
}

// relay opens the remote stream and pumps it; the end is its close.
func (d *daemon) relay(ctx context.Context, r *reservation, ac *stream.Conn, in <-chan frame) stream.CloseMsg {
	octx, cancel := context.WithTimeout(ctx, dialTimeout)
	rc, msg := d.openRemote(octx, r)
	cancel()
	switch {
	case rc != nil:
		defer rc.Close()
	case ctx.Err() != nil:
		return ended(ctx)
	default:
		return msg
	}
	return pump(ctx, ac, rc, in)
}

func (d *daemon) openRemote(ctx context.Context, r *reservation) (*stream.Conn, stream.CloseMsg) {
	tun, ok := d.tunnelTo(ctx, r.peer)
	if !ok {
		return nil, stream.CloseMsg{Reason: "offline"}
	}
	rc, err := tun.Open(ctx, r.h)
	var ref *transport.Refused
	switch {
	case errors.As(err, &ref):
		return nil, stream.CloseMsg{Reason: ref.Reason, Detail: ref.Detail}
	case err != nil:
		return nil, stream.CloseMsg{Reason: "offline", Detail: err.Error()}
	}
	return rc, stream.CloseMsg{}
}

// pump carries the client's frames to the peer and the peer's data frames to
// the client until the peer sends its close, its side ends without one
// (connection-lost), or the stream is detached.
func pump(ctx context.Context, ac, rc *stream.Conn, in <-chan frame) stream.CloseMsg {
	stop := context.AfterFunc(ctx, func() {
		rc.Close()
		ac.SetWriteDeadline(time.Now().Add(closeGrace))
	})
	defer stop()
	go func() {
		for f := range in {
			if rc.WriteFrame(f.t, f.p) != nil {
				return
			}
		}
	}()
	for {
		t, p, err := rc.ReadFrame()
		switch {
		case ctx.Err() != nil:
			return ended(ctx)
		case err != nil:
			return stream.CloseMsg{Reason: "connection-lost"}
		case t == stream.Close:
			return stream.ParseClose(p)
		case ac.WriteFrame(t, p) != nil:
			return stream.CloseMsg{Reason: "detached"} // the client has gone
		}
	}
}

// closeStream ends a reserved stream, or detaches an attached one.
func (d *daemon) closeStream(id string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	if r, ok := d.reservations[id]; ok {
		r.timer.Stop()
		delete(d.reservations, id)
		return true
	}
	detach, ok := d.active[id]
	if ok {
		detach()
	}
	return ok
}
