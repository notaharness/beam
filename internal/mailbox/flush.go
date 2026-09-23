package mailbox

import (
	"context"
	"encoding/json"
	"time"

	"github.com/notaharness/beam/internal/store"
	"github.com/notaharness/beam/internal/stream"
)

// retryEvery is how long the flusher waits on an unacked head, a refusal it
// retries, or a msg stream it could not open (docs/05).
const retryEvery = 2 * time.Second

// Flush delivers peer's outbound queue while ctx lives (the life of the
// tunnel this machine dialed): it opens the msg stream once there is mail,
// sends the head, and moves on only when it is acked. wake says a new
// envelope was queued; settled reports each seq that left the queue, with ""
// for delivered or the reason it was quarantined.
func Flush(ctx context.Context, st *store.Store, peer string, open func(context.Context) (*stream.Conn, error),
	wake <-chan struct{}, settled func(seq int64, reason string)) {
	var f flusher
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
		case f.send(ctx, open, env) != nil:
			f.close()
			sleep(ctx, retryEvery)
		default:
			a, acked := f.await(ctx, idOf(env))
			if !acked {
				continue // resend the head
			}
			reason, done, err := Settle(st, peer, seq, a)
			if err != nil || !done {
				sleep(ctx, retryEvery)
			} else {
				settled(seq, reason)
			}
		}
	}
}

// flusher is the dialer's side of one msg stream.
type flusher struct {
	sc   *stream.Conn
	acks chan Ack      // closed when the stream ends
	done chan struct{} // closed when the flusher drops the stream
}

func (f *flusher) send(ctx context.Context, open func(context.Context) (*stream.Conn, error), env []byte) error {
	if f.sc == nil {
		sc, err := open(ctx)
		if err != nil {
			return err
		}
		f.sc, f.acks, f.done = sc, make(chan Ack), make(chan struct{})
		go readAcks(sc, f.acks, f.done)
	}
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
