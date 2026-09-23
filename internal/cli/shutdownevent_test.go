//go:build beamtest

package cli_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"syscall"
	"testing"
	"time"

	"github.com/notaharness/beam/internal/control"
)

// docs/06 Events: a daemon that stops tells its subscribers why, as its last
// line to them: requested by daemon.shutdown, a signal, or its parent gone
// (--exit-with-parent).
func TestShutdownEvent(t *testing.T) {
	for _, tc := range []struct {
		reason string
		stop   func(t *testing.T, m *machine) (start func(), stop func())
	}{
		{"requested", func(t *testing.T, m *machine) (func(), func()) {
			return func() { m.start(t) }, func() {
				c, err := control.Connect(m.paths(), nil)
				if err != nil {
					t.Fatal(err)
				}
				defer c.Close()
				c.Call("daemon.shutdown", nil, nil)
			}
		}},
		{"signal", func(t *testing.T, m *machine) (func(), func()) {
			d := exec.Command(os.Args[0], "daemon", "--derp-map", relay.MapURL)
			d.Env, d.Stderr = append(os.Environ(), "BEAM_CONFIG_DIR="+m.dir), os.Stderr
			t.Cleanup(func() { d.Process.Kill(); d.Wait() })
			return func() {
					if err := d.Start(); err != nil {
						t.Fatal(err)
					}
				}, func() {
					d.Process.Signal(syscall.SIGTERM)
				}
		}},
		{"parent-exited", func(t *testing.T, m *machine) (func(), func()) {
			r, w, _ := os.Pipe()
			t.Cleanup(func() { r.Close(); w.Close() })
			return func() {
				go m.run(r, os.Stderr, os.Stderr, "daemon", "--exit-with-parent", "--derp-map", relay.MapURL)
			}, func() { w.Close() }
		}},
	} {
		t.Run(tc.reason, func(t *testing.T) {
			m := blank(t, "fresh")
			start, stop := tc.stop(t, m)
			start()
			waitFor(t, 10*time.Second, "the daemon's socket", func() bool { return answering(m) })
			next := events(t, m)
			stop()
			var ev struct {
				Reason string `json:"reason"`
			}
			if err := json.Unmarshal(next("shutdown"), &ev); err != nil || ev.Reason != tc.reason {
				t.Errorf("shutdown %+v, %v; want reason %s", ev, err, tc.reason)
			}
			waitFor(t, 10*time.Second, "the daemon to stop", func() bool { return !answering(m) })
		})
	}
}
