package control

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
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
// stream, and pump frames until it closes. Every way it ends reaches the
// client as a close frame.
func (d *daemon) attach(ac *stream.Conn, id string) {
	defer ac.Close()
	d.mu.Lock()
	r, ok := d.reservations[id]
	if ok {
		r.timer.Stop()
		delete(d.reservations, id)
		d.active[id] = ac
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
	ctx, cancel := context.WithTimeout(d.ctx, dialTimeout)
	rc, msg := d.openRemote(ctx, r)
	cancel()
	if rc != nil {
		msg = pump(ac, rc)
	} else {
		_ = ac.WriteJSON(stream.Close, msg) // closing either way
	}
	d.emit("stream.closed", streamClosed{id, msg.Reason, msg.ExitCode, msg.Signal})
}

type streamClosed struct {
	StreamID string  `json:"streamId"`
	Reason   string  `json:"reason"`
	ExitCode *int    `json:"exitCode,omitempty"`
	Signal   *string `json:"signal,omitempty"`
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

// pump copies the client's frames to the peer as bytes, and the peer's frames
// to the client one by one, watching for the close. The stream is detached
// when the client's side ends first, and connection-lost when the peer's
// ends without a close.
func pump(ac, rc *stream.Conn) stream.CloseMsg {
	detached := make(chan struct{})
	go func() {
		_, _ = io.Copy(rc, ac) // ends with either side
		close(detached)
		rc.Close()
	}()
	for {
		t, p, err := rc.ReadFrame()
		select {
		case <-detached:
			return stream.CloseMsg{Reason: "detached"}
		default:
		}
		if err != nil {
			msg := stream.CloseMsg{Reason: "connection-lost"}
			_ = ac.WriteJSON(stream.Close, msg) // closing either way
			return msg
		}
		if ac.WriteFrame(t, p) != nil {
			rc.Close()
			return stream.CloseMsg{Reason: "detached"}
		}
		if t == stream.Close {
			return stream.ParseClose(p)
		}
	}
}

// closeStream ends a reserved stream, or detaches an attached one: pump
// closes the remote side once the client's has ended.
func (d *daemon) closeStream(id string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	if r, ok := d.reservations[id]; ok {
		r.timer.Stop()
		delete(d.reservations, id)
		return true
	}
	ac, ok := d.active[id]
	if ok {
		ac.Close()
	}
	return ok
}
