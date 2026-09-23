package control

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/notaharness/beam/internal/stream"
)

// docs/06 Attach: a taken from the peer answers input the client has
// outstanding, or the stream ends "window" there and then: a taken for nothing
// never grants the client credit beyond its window.
func TestAttachTakenForNothing(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		input  int // frames the client sends, and the peer reads, first
		takens int // then the peer's takens
		after  int // then the client's frames, the peer reading none, before it leaves
	}{
		{"unsolicited", 0, 1, 0},
		{"duplicate", 1, 2, 0},
		{"many, then more than a window and the client leaves", 0, 8, stream.InputWindow + 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			clientSide, daemonSide := net.Pipe()
			peerSide, remote := net.Pipe()
			defer clientSide.Close()
			defer peerSide.Close()
			cc, peer := stream.NewConn(clientSide), stream.NewConn(peerSide)
			cl := &client{ac: stream.NewConn(daemonSide), in: make(chan frame, stream.InputWindow)}
			ctx, cancel := context.WithCancelCause(context.Background())
			defer cancel(nil)
			go cl.read(ctx, cancel)
			ended := make(chan stream.CloseMsg, 1)
			go func() { ended <- pump(ctx, cl, stream.NewConn(remote)); remote.Close() }()
			for range tc.input {
				if cc.WriteFrame(stream.Data, []byte("x")) != nil {
					t.Fatal("input refused")
				}
				if _, _, err := peer.ReadFrame(); err != nil {
					t.Fatal(err)
				}
			}
			go func() { // what the daemon relays
				for {
					if _, _, err := cc.ReadFrame(); err != nil {
						return
					}
				}
			}()
			go func() {
				for range tc.takens {
					if peer.WriteJSON(stream.Control, stream.Ctl{Kind: "taken"}) != nil {
						return
					}
				}
				for range tc.after {
					if cc.WriteFrame(stream.Data, []byte("x")) != nil {
						return
					}
				}
				if tc.after > 0 {
					clientSide.Close()
				}
			}()
			select {
			case m := <-ended:
				if m.Reason != "window" {
					t.Fatalf("ended %+v, want window", m)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("the stream went on")
			}
		})
	}
}
