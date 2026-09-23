//go:build beamtest

package cli_test

import (
	"encoding/json"
	"os"
	"syscall"
	"testing"
	"time"

	"github.com/notaharness/beam/internal/control"
)

// docs/06 Events: a daemon that stops tells its subscribers why, once it no
// longer listens: requested by daemon.shutdown, a signal, or its parent gone
// (--exit-with-parent).
func TestShutdownEvent(t *testing.T) {
	for _, tc := range []struct {
		reason string
		start  func(t *testing.T, m *machine) (stop func())
	}{
		{"requested", func(t *testing.T, m *machine) func() {
			m.start(t)
			return func() {
				c, err := control.Connect(m.paths(), nil)
				if err != nil {
					t.Fatal(err)
				}
				defer c.Close()
				c.Call("daemon.shutdown", nil, nil)
			}
		}},
		{"signal", func(t *testing.T, m *machine) func() {
			d, _ := m.process(t)
			return func() { d.Process.Signal(syscall.SIGTERM) }
		}},
		{"parent-exited", func(t *testing.T, m *machine) func() {
			r, w, _ := os.Pipe()
			t.Cleanup(func() { r.Close(); w.Close() })
			go m.run(r, os.Stderr, os.Stderr, "daemon", "--exit-with-parent", "--derp-map", relay.MapURL)
			return func() { w.Close() }
		}},
	} {
		t.Run(tc.reason, func(t *testing.T) {
			m := blank(t, "fresh")
			stop := tc.start(t, m)
			waitFor(t, 10*time.Second, "the daemon's socket", func() bool { return answering(m) })
			next := events(t, m)
			stop()
			var ev struct {
				Reason string `json:"reason"`
			}
			if err := json.Unmarshal(next("shutdown"), &ev); err != nil || ev.Reason != tc.reason {
				t.Errorf("shutdown %+v, %v; want reason %s", ev, err, tc.reason)
			}
			if answering(m) {
				t.Error("the daemon still listens after its shutdown event")
			}
			waitFor(t, 10*time.Second, "the daemon to stop", func() bool { return !answering(m) })
		})
	}
}
