package stream

import (
	"os"
	"testing"
	"time"
)

// docs/04 Input: the acceptor reads on while the opener leaves its output
// unread, so a close behind a window of input it answers at once (resizes,
// ignored controls) is read and ends the process.
func TestCloseReadUnderStalledOutput(t *testing.T) {
	for _, tc := range []struct {
		kind  string
		input Ctl
		serve func(*Conn, Header, Spawn)
	}{
		{KindPTY, Ctl{Kind: "resize", Cols: 100, Rows: 30}, PTY},
		{KindExec, Ctl{Kind: "ignored"}, Exec},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			opener, acceptor := pipe(t)
			h := Header{V: 1, Kind: tc.kind, Argv: []string{"yes"}, Cols: 80, Rows: 24}
			go tc.serve(acceptor, h, Spawn{Env: os.Environ(), Home: t.TempDir()})
			var r Response
			if err := opener.ReadLine(&r); err != nil || !r.OK {
				t.Fatalf("response %+v, %v", r, err)
			}
			opener.SetWriteDeadline(time.Now().Add(3 * time.Second))
			for range InputWindow { // their answers meet the stalled output
				if err := opener.WriteJSON(Control, tc.input); err != nil {
					t.Fatalf("input not read: %v", err)
				}
			}
			time.Sleep(200 * time.Millisecond)
			if err := opener.WriteJSON(Close, CloseMsg{Reason: "detached"}); err != nil {
				t.Fatalf("the close was not read: %v", err)
			}
			opener.SetReadDeadline(time.Now().Add(10 * time.Second))
			for {
				typ, p, err := opener.ReadFrame()
				if err != nil {
					t.Fatalf("no close: %v", err)
				}
				if typ == Close {
					if m := ParseClose(p); m.Signal == nil {
						t.Fatalf("close %+v, want the process ended by a signal", m)
					}
					return
				}
			}
		})
	}
}

// docs/04 Input: a frame's place comes back only as its taken is written, so
// answers queued behind stalled data output keep the window at four, and a
// fifth frame is an overrun, which ends the stream window.
func TestWindowHeldUnderStalledOutput(t *testing.T) {
	for _, tc := range []struct {
		kind  string
		input Ctl
		serve func(*Conn, Header, Spawn)
	}{
		{KindPTY, Ctl{Kind: "resize", Cols: 100, Rows: 30}, PTY},
		{KindExec, Ctl{Kind: "ignored"}, Exec},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			opener, acceptor := pipe(t)
			h := Header{V: 1, Kind: tc.kind, Argv: []string{"yes"}, Cols: 80, Rows: 24}
			go tc.serve(acceptor, h, Spawn{Env: os.Environ(), Home: t.TempDir()})
			var r Response
			if err := opener.ReadLine(&r); err != nil || !r.OK {
				t.Fatalf("response %+v, %v", r, err)
			}
			time.Sleep(300 * time.Millisecond) // for the data output to stall
			opener.SetWriteDeadline(time.Now().Add(3 * time.Second))
			for i := range InputWindow + 1 {
				if i == InputWindow {
					time.Sleep(200 * time.Millisecond) // for the answers to wait behind it
				}
				if err := opener.WriteJSON(Control, tc.input); err != nil {
					t.Fatalf("input %d not read: %v", i, err)
				}
			}
			opener.SetReadDeadline(time.Now().Add(10 * time.Second))
			for {
				typ, p, err := opener.ReadFrame()
				if err != nil {
					t.Fatalf("no close: %v", err)
				}
				if typ == Close {
					if m := ParseClose(p); m.Reason != "window" {
						t.Fatalf("close %+v, want window", m)
					}
					return
				}
			}
		})
	}
}
