package mailbox

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/notaharness/beam/internal/store"
	"github.com/notaharness/beam/internal/stream"
)

// retryEvery is how long the flusher waits on an unacked head, a refusal it
// retries, or a msg stream it could not open (docs/05).
const retryEvery = 2 * time.Second

// writeTimeout bounds a send the peer does not take; the stream is then
// dropped and the head sent again on a new one.
const writeTimeout = 30 * time.Second

// refusedWait is how long the flusher waits, short of new mail, to open a msg
// stream the peer's grant refused again; only tests change it.
var refusedWait = 5 * time.Minute

// ErrNotGranted is an open of the msg stream that the peer's grant refused.
// Any other failed open is retried as an unacked head is.
var ErrNotGranted = errors.New("the peer's grant refuses the msg stream")

// Flush delivers peer's outbound queue while ctx lives (the life of the
// tunnel this machine dialed): it opens the msg stream once there is mail,
// sends the head, and moves on only when it is acked. wake says a new
// envelope was queued; settled reports each seq that left the queue, with ""
// for delivered or the reason it was quarantined.
func Flush(ctx context.Context, st *store.Store, peer string, open func(context.Context) (*stream.Conn, error),
	wake <-chan struct{}, settled func(seq int64, reason string)) {
	f := flusher{st: st, peer: peer, open: open, wake: wake, settled: settled}
	defer f.close()
	for ctx.Err() == nil {
		seq, env, ok, err := st.Head(peer)
		switch {
		case err != nil:
			sleep(ctx, retryEvery)
		case !ok:
			select {
			case <-wake:
			case <-ctx.Done():
			}
		default:
			f.deliver(ctx, seq, env)
		}
	}
}

// flusher is the dialer's side of one msg stream, which ctx's end closes.
type flusher struct {
	st      *store.Store
	peer    string
	open    func(context.Context) (*stream.Conn, error)
	wake    <-chan struct{}
	settled func(seq int64, reason string)

	sc   *stream.Conn
	acks chan Ack      // closed when the stream ends
	done chan struct{} // closed when the flusher drops the stream
	stop func() bool   // unties the stream from ctx
}

// deliver sends the head and settles it once acked. After a failed send it
// waits: for new mail or refusedWait if the peer refused the stream, else
// retryEvery.
func (f *flusher) deliver(ctx context.Context, seq int64, env []byte) {
	if err := f.send(ctx, env); err != nil {
		f.close()
		if !errors.Is(err, ErrNotGranted) {
			sleep(ctx, retryEvery)
			return
		}
		select {
		case <-f.wake:
		case <-time.After(refusedWait):
		case <-ctx.Done():
		}
		return
	}
	a, acked := f.await(ctx, idOf(env))
	if !acked {
		return // resend the head
	}
	reason, done, err := Settle(f.st, f.peer, seq, a)
	if err != nil || !done {
		sleep(ctx, retryEvery)
		return
	}
	f.settled(seq, reason)
}

func (f *flusher) send(ctx context.Context, env []byte) error {
	if f.sc == nil {
		sc, err := f.open(ctx)
		if err != nil {
			return err
		}
		f.sc, f.acks, f.done = sc, make(chan Ack), make(chan struct{})
		f.stop = context.AfterFunc(ctx, func() { sc.Close() })
		go readAcks(sc, f.acks, f.done)
	}
	f.sc.SetWriteDeadline(time.Now().Add(writeTimeout))
	return f.sc.WriteFrame(stream.Data, env)
}

func readAcks(sc *stream.Conn, acks chan<- Ack, done <-chan struct{}) {
	defer close(acks)
	for {
		t, p, err := sc.ReadFrame()
		if err != nil || t == stream.Close {
			return
		}
		var a Ack
		if t != stream.Control || json.Unmarshal(p, &a) != nil || a.Kind != "ack" {
			continue
		}
		select {
		case acks <- a:
		case <-done:
			return
		}
	}
}

// await waits up to retryEvery for the ack of envelope id, skipping acks of
// earlier sends of envelopes already settled.
func (f *flusher) await(ctx context.Context, id string) (Ack, bool) {
	timeout := time.After(retryEvery)
	for {
		select {
		case a, open := <-f.acks:
			if !open {
				f.close()
				return Ack{}, false
			}
			if a.ID == id {
				return a, true
			}
		case <-timeout:
			return Ack{}, false
		case <-ctx.Done():
			return Ack{}, false
		}
	}
}

func (f *flusher) close() {
	if f.sc != nil {
		f.stop()
		close(f.done)
		f.sc.Close()
		f.sc = nil
	}
}

func idOf(env []byte) string {
	var e struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(env, &e) // this machine's own envelope
	return e.ID
}

func sleep(ctx context.Context, d time.Duration) {
	select {
	case <-time.After(d):
	case <-ctx.Done():
	}
}

// Serve is the acceptor's side of a msg stream from peer (docs/04): every
// data frame is one envelope, answered with an ack once stored. stored is
// called after each envelope that was.
func Serve(sc *stream.Conn, st *store.Store, peer, self string, stored func()) {
	defer sc.Close()
	for {
		t, p, err := sc.ReadFrame()
		if err != nil || t == stream.Close {
			return
		}
		if t != stream.Data {
			continue
		}
		a := Accept(st, peer, self, p, time.Now().UnixMilli())
		if a.Accepted {
			stored()
		}
		if sc.WriteJSON(stream.Control, a) != nil {
			return
		}
	}
}
