package mailbox

import (
	"context"
	"errors"
	"fmt"
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/notaharness/beam/internal/identity"
	"github.com/notaharness/beam/internal/store"
	"github.com/notaharness/beam/internal/stream"
)

const peerA, peerB = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

// knownB is B's entry as A pinned it: A can send to B.
var knownB = identity.Record{V: 1, Kind: identity.Member, PeerID: peerB, Label: "b"}

// docs/05 Delivery: a head whose ack does not come within 2 s is sent again,
// and leaves the queue once acked.
func TestFlushResendsUnackedHead(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, err := st.Pin(knownB, 1); err != nil {
		t.Fatal(err)
	}
	e, _ := New(peerA, peerB, "", "again", UTF8, 1)
	if _, err := st.Enqueue(peerB, 1, func(seq int64) ([]byte, error) { e.Seq = seq; b, _ := e.Marshal(); return b, nil }); err != nil {
		t.Fatal(err)
	}
	ours, theirs := net.Pipe()
	opened := false
	open := func(context.Context) (*stream.Conn, error) {
		if opened {
			return nil, errors.New("the msg stream is open already")
		}
		opened = true
		return stream.NewConn(ours), nil
	}
	settled := make(chan int64, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go Flush(ctx, st, peerB, open, make(chan struct{}), func(seq int64, _ string) { settled <- seq })
	peer := stream.NewConn(theirs)
	peer.SetReadDeadline(time.Now().Add(5 * time.Second))
	for range 2 { // the first send's ack is lost
		if _, p, err := peer.ReadFrame(); err != nil || idOf(p) != e.ID {
			t.Fatalf("got %q, %v; want %s sent again", p, err, e.ID)
		}
	}
	if err := peer.WriteJSON(stream.Control, Ack{Kind: "ack", ID: e.ID, Accepted: true}); err != nil {
		t.Fatal(err)
	}
	select {
	case seq := <-settled:
		if seq != e.Seq {
			t.Errorf("settled %d, want %d", seq, e.Seq)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the acked head was not settled")
	}
}

// docs/05 Delivery: a recipient whose grant refuses the msg stream is asked
// again after refusedWait with no new mail, and the head then goes out.
func TestFlushNotGrantedRetriesLater(t *testing.T) {
	defer func(was time.Duration) { refusedWait = was }(refusedWait)
	refusedWait = 200 * time.Millisecond
	st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, err := st.Pin(knownB, 1); err != nil {
		t.Fatal(err)
	}
	e, _ := New(peerA, peerB, "", "later", UTF8, 1)
	if _, err := st.Enqueue(peerB, 1, func(seq int64) ([]byte, error) { e.Seq = seq; b, _ := e.Marshal(); return b, nil }); err != nil {
		t.Fatal(err)
	}
	ours, theirs := net.Pipe()
	opens := 0 // open runs on the flusher's goroutine alone
	open := func(context.Context) (*stream.Conn, error) {
		if opens++; opens == 1 {
			return nil, fmt.Errorf("%w: grant", ErrNotGranted)
		}
		return stream.NewConn(ours), nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go Flush(ctx, st, peerB, open, make(chan struct{}), func(int64, string) {}) // never woken
	peer := stream.NewConn(theirs)
	peer.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, p, err := peer.ReadFrame(); err != nil || idOf(p) != e.ID {
		t.Fatalf("got %q, %v; want %s once the wait is over", p, err, e.ID)
	}
}

// docs/05 Delivery: a send the peer does not take holds the flusher neither
// past the tunnel's end nor for long: the stream is dropped and the head sent
// again on a new one.
func TestFlushWriteBounded(t *testing.T) {
	for _, tc := range []struct {
		name   string
		cancel bool
		within time.Duration
	}{
		{"the tunnel ends", true, 2 * time.Second},
		{"the peer never takes it", false, writeTimeout + 5*time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer st.Close()
			st.Pin(knownB, 1)
			e, _ := New(peerA, peerB, "", "stuck", UTF8, 1)
			st.Enqueue(peerB, 1, func(seq int64) ([]byte, error) { e.Seq = seq; b, _ := e.Marshal(); return b, nil })
			opens := make(chan struct{}, 2)
			open := func(context.Context) (*stream.Conn, error) {
				ours, theirs := net.Pipe() // theirs is never read
				t.Cleanup(func() { theirs.Close() })
				opens <- struct{}{}
				return stream.NewConn(ours), nil
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan struct{})
			go func() {
				defer close(done)
				Flush(ctx, st, peerB, open, make(chan struct{}), func(int64, string) {})
			}()
			<-opens
			time.Sleep(100 * time.Millisecond) // into the write
			want := done
			if tc.cancel {
				cancel()
			} else {
				reopened := make(chan struct{})
				go func() { <-opens; close(reopened) }()
				want = reopened
			}
			select {
			case <-want:
			case <-time.After(tc.within):
				t.Fatal("the flusher is still held by the write")
			}
		})
	}
}
