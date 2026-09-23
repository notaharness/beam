package control

import (
	"context"
	"encoding/json"
	"net"
	"testing"
	"time"

	"github.com/notaharness/beam/internal/identity"
	"github.com/notaharness/beam/internal/stream"
)

// docs/03: a sync peer that stops reading holds the dialer neither in its dump
// nor in a delta. The tunnel's end still ends the stream, and so does the
// liveness bound.
func TestSyncPeerStopsReading(t *testing.T) {
	t.Parallel()
	dump := []identity.Record{{V: 1, Kind: identity.Member, Label: "a"}}
	for _, tc := range []struct {
		name     string
		readDump bool // the peer takes the dump, then stops
		cancel   bool // the tunnel ends
		within   time.Duration
	}{
		{"tunnel ends during the dump", false, true, 5 * time.Second},
		{"tunnel ends during a delta", true, true, 5 * time.Second},
		{"no pong during a delta", true, false, pongTimeout + 5*time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ours, theirs := net.Pipe()
			defer theirs.Close()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			deltas := make(chan delta, 1)
			done := make(chan struct{})
			go func() {
				defer close(done)
				pushSync(ctx, stream.NewConn(ours), dump, deltas, func() {})
			}()
			if tc.readDump {
				readDump(t, stream.NewConn(theirs))
				deltas <- delta{r: dump[0]}
			}
			time.Sleep(200 * time.Millisecond) // the write is blocked by now
			if tc.cancel {
				cancel()
			}
			select {
			case <-done:
			case <-time.After(tc.within):
				t.Fatal("the dialer is still held by a peer that does not read")
			}
		})
	}
}

func readDump(t *testing.T, c *stream.Conn) {
	t.Helper()
	for {
		typ, p, err := c.ReadFrame()
		if err != nil {
			t.Fatal(err)
		}
		var ctl stream.Ctl
		if typ == stream.Control && json.Unmarshal(p, &ctl) == nil && ctl.Kind == "end" {
			return
		}
	}
}

// docs/03: a peer that answers every ping keeps its sync stream past the
// liveness bound.
func TestSyncKeptWhileAnswered(t *testing.T) {
	t.Parallel()
	ours, theirs := net.Pipe()
	defer theirs.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		pushSync(ctx, stream.NewConn(ours), nil, nil, func() {})
	}()
	peer := stream.NewConn(theirs)
	go func() {
		for {
			typ, p, err := peer.ReadFrame()
			if err != nil {
				return
			}
			var ctl stream.Ctl
			if typ == stream.Control && json.Unmarshal(p, &ctl) == nil && ctl.Kind == "ping" {
				_ = peer.WriteJSON(stream.Control, stream.Ctl{Kind: "pong", T: ctl.T})
			}
		}
	}()
	select {
	case <-done:
		t.Fatal("a peer that answers every ping lost its stream")
	case <-time.After(pongTimeout + pingEvery + time.Second):
	}
}
