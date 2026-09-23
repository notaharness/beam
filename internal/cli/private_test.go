//go:build beamtest

package cli_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/notaharness/beam/internal/cli"
	"github.com/notaharness/beam/internal/control"
)

// docs/02 "Private to its user": a daemon refuses a $BEAM_DIR, run directory
// or socket directory others can write, and a file of its own others can
// read or write, and changes no permissions; --detach refuses before it
// starts anything.
func TestPrivateDirs(t *testing.T) {
	for _, tc := range []struct {
		name  string
		mode  os.FileMode
		setup func(m *machine) (path, socket string)
	}{
		{"$BEAM_DIR writable by others", 0o777, func(m *machine) (string, string) { return m.dir, "" }},
		{"run writable by group", 0o770, func(m *machine) (string, string) { return filepath.Join(m.dir, "run"), "" }},
		{"the socket's directory writable by others", 0o777, func(m *machine) (string, string) {
			dir := filepath.Join(beamDir(t), "s")
			os.Mkdir(dir, 0o700)
			return dir, filepath.Join(dir, "beam.sock")
		}},
		{"key.json readable by others", 0o644, func(m *machine) (string, string) { return filepath.Join(m.dir, "key.json"), "" }},
		{"fleet.json readable by group", 0o640, func(m *machine) (string, string) { return filepath.Join(m.dir, "fleet.json"), "" }},
		{"state.db readable by group", 0o640, func(m *machine) (string, string) { return filepath.Join(m.dir, "state.db"), "" }},
		{"daemon.log readable by others", 0o644, func(m *machine) (string, string) { return filepath.Join(m.dir, "daemon.log"), "" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := newMachine(t, "alpha")
			os.MkdirAll(filepath.Join(m.dir, "run"), 0o700)
			for _, f := range []string{"state.db", "daemon.log"} {
				os.WriteFile(filepath.Join(m.dir, f), nil, 0o600)
			}
			path, socket := tc.setup(m)
			vars := []string{"BEAM_CONFIG_DIR=" + m.dir, "HOME=" + os.Getenv("HOME"), "PATH=" + os.Getenv("PATH")}
			p := m.paths()
			if socket != "" {
				vars, p.Socket = append(vars, "BEAM_SOCKET="+socket), socket
			}
			if err := os.Chmod(path, tc.mode); err != nil {
				t.Fatal(err)
			}
			defer os.Chmod(path, 0o700) // so the test's cleanup can remove it
			for _, detach := range []bool{false, true} {
				args := []string{"daemon", "--derp-map", relay.MapURL, "--directory", dirURL}
				if detach {
					args = append(args, "--detach")
				}
				var out, errb strings.Builder
				exited := make(chan int, 1)
				go func() { exited <- cli.Main(args, vars, strings.NewReader(""), &out, &errb) }()
				select {
				case code := <-exited:
					if code != 1 || !strings.Contains(errb.String(), path) || !strings.Contains(errb.String(), "chmod") {
						t.Errorf("detach %v: exit %d, %q", detach, code, errb.String())
						stopDaemon(p)
					}
				case <-time.After(3 * time.Second):
					t.Errorf("detach %v: the daemon started", detach)
					stopDaemon(p)
				}
				if st, _ := os.Stat(path); st.Mode().Perm() != tc.mode {
					t.Errorf("the mode changed to %v", st.Mode().Perm())
				}
			}
		})
	}
}

// docs/06 connect-or-spawn: a command whose daemon refuses to start fails at
// once, saying why.
func TestSpawnRefused(t *testing.T) {
	m := blank(t, "fresh")
	os.Chmod(m.dir, 0o777)
	start := time.Now()
	if r := m.beam("", "status"); r.code != 1 || !strings.Contains(r.err, "chmod go-w "+m.dir) || time.Since(start) > 2*time.Second {
		t.Errorf("%+v after %v", r, time.Since(start))
		stopDaemon(m.paths())
	}
}

// stopDaemon stops a daemon a failing case started at p, if one answers.
func stopDaemon(p control.Paths) {
	for range 50 { // a detached daemon may not be listening yet
		if c, err := control.Dial(p); err == nil {
			c.Call("daemon.shutdown", nil, nil)
			c.Close()
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
}
