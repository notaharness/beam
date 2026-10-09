//go:build beamtest

package cli_test

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/notaharness/beam/internal/control"
)

// docs/02: a daemon started for a $BEAM_DIR that does not exist yet, as the
// first command on a machine starts one, makes it this user's alone, and
// each directory it lacked.
func TestDetachMakesPrivateDir(t *testing.T) {
	parent := filepath.Join(beamDir(t), "new")
	m := &machine{dir: filepath.Join(parent, "beam")}
	if r := m.beam("", "daemon", "--detach", "--derp-map", relay.MapURL); r.code != 0 {
		t.Fatalf("detach: %+v", r)
	}
	for _, d := range []string{parent, m.dir} {
		if err := control.CheckPrivate(d); err != nil {
			t.Errorf("%s: %v", d, err)
		}
	}
	waitFor(t, 10*time.Second, "the daemon's socket", func() bool { return answering(m) })
	c, err := control.Dial(m.paths())
	if err != nil {
		t.Fatal(err)
	}
	c.Call("daemon.shutdown", nil, nil)
	c.Close()
	waitFor(t, 10*time.Second, "the daemon to stop", func() bool { return !answering(m) })
}
