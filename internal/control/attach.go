package control

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sync/atomic"
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
// remote open sends nothing; so does a client that overruns its input window.
// Every end reaches the client as a close frame.
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
	cl := &client{ac: ac, in: make(chan frame, stream.InputWindow)}
	go cl.read(ctx, cancel)
	d.at("attached", r.peer)
	msg := d.relay(ctx, r, cl)
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

// closeGrace is how long a client that stopped reading has to take its close.
const closeGrace = time.Second

// Why an attach ctx ended: stream.close or the client's end, or the client's
// window overrun; the daemon stopping ends the rest.
var (
	errDetached = errors.New("detached")
	errWindow   = errors.New("window")
)

// ended is the close of a stream whose attach ctx ended.
func ended(ctx context.Context) stream.CloseMsg {
	switch context.Cause(ctx) {
	case errDetached:
		return stream.CloseMsg{Reason: "detached"}
	case errWindow:
		return stream.CloseMsg{Reason: "window"}
	}
	return stream.CloseMsg{Reason: "connection-lost"}
}

type frame struct {
	t stream.Type
	p []byte
}

// client is an attach's client side. Its frames are read from attach on, so
// its departure ends the stream even before the remote side is open, and it
// is held to the input window (docs/04, Input) as the acceptor holds this
// daemon: at most a window of frames not yet answered taken, so in never
// fills and the reading never stops.
type client struct {
	ac          *stream.Conn
	in          chan frame   // read, not yet sent on
	outstanding atomic.Int32 // input frames not yet answered taken
}

// read reads the client's frames into in until its side ends or it sends
// close, which detach, or it overruns its window.
func (cl *client) read(ctx context.Context, end context.CancelCauseFunc) {
	defer close(cl.in)
	for {
		t, p, err := cl.ac.ReadFrame()
		switch {
		case err != nil || t == stream.Close:
			end(errDetached)
			return
		case cl.outstanding.Add(1) > stream.InputWindow:
			end(errWindow)
			return
		}
		select {
		case cl.in <- frame{t, p}:
		case <-ctx.Done():
			return
		}
	}
}

// relay opens the remote stream and pumps it; the end is its close.
func (d *daemon) relay(ctx context.Context, r *reservation, cl *client) stream.CloseMsg {
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
	return pump(ctx, cl, rc)
}

func (d *daemon) openRemote(ctx context.Context, r *reservation) (*stream.Conn, stream.CloseMsg) {
	tun, ok := d.tunnelTo(ctx, r.peer)
	switch {
	case !ok && d.revokedByFleet(r.peer):
		return nil, stream.CloseMsg{Reason: "revoked"}
	case !ok:
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

// pump carries the client's frames to the peer and the peer's frames to the
// client, counting each taken the peer answers, until the peer sends its
// close, its side ends without one (connection-lost), the stream is detached,
// or the peer answers taken with no input outstanding (window).
func pump(ctx context.Context, cl *client, rc *stream.Conn) stream.CloseMsg {
	stop := context.AfterFunc(ctx, func() {
		rc.Close()
		cl.ac.SetWriteDeadline(time.Now().Add(closeGrace))
	})
	defer stop()
	go func() {
		for f := range cl.in {
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
		case taken(t, p) && !cl.took():
			return stream.CloseMsg{Reason: "window"}
		}
		if cl.ac.WriteFrame(t, p) != nil {
			return stream.CloseMsg{Reason: "detached"} // the client has gone
		}
	}
}

// took counts an input frame answered taken, reporting false, and counting
// nothing, if none was outstanding.
func (cl *client) took() bool {
	for {
		n := cl.outstanding.Load()
		if n == 0 {
			return false
		}
		if cl.outstanding.CompareAndSwap(n, n-1) {
			return true
		}
	}
}

// taken reports whether a peer's frame answers an input frame taken.
func taken(t stream.Type, p []byte) bool {
	var ctl stream.Ctl
	return t == stream.Control && json.Unmarshal(p, &ctl) == nil && ctl.Kind == "taken"
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
