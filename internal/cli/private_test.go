//go:build beamtest

package cli_test

import (
	"io"
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
		setup func(m *machine) (path string, vars []string)
	}{
		{"$BEAM_DIR writable by others", func(m *machine) (string, []string) { return m.dir, nil }},
		{"run writable by group", func(m *machine) (string, []string) {
			return filepath.Join(m.dir, "run"), nil
		}},
		{"the socket's directory writable by others", func(m *machine) (string, []string) {
			dir := filepath.Join(shortTemp(t), "s")
			os.Mkdir(dir, 0o700)
			return dir, []string{"BEAM_SOCKET=" + filepath.Join(dir, "beam.sock")}
		}},
		{"key.json readable by others", func(m *machine) (string, []string) { return filepath.Join(m.dir, "key.json"), nil }},
		{"fleet.json readable by group", func(m *machine) (string, []string) { return filepath.Join(m.dir, "fleet.json"), nil }},
		{"state.db readable by group", func(m *machine) (string, []string) { return filepath.Join(m.dir, "state.db"), nil }},
		{"daemon.log readable by others", func(m *machine) (string, []string) { return filepath.Join(m.dir, "daemon.log"), nil }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := newMachine(t, "alpha")
			os.MkdirAll(filepath.Join(m.dir, "run"), 0o700)
			for _, f := range []string{"state.db", "daemon.log"} {
				os.WriteFile(filepath.Join(m.dir, f), nil, 0o600)
			}
			path, vars := tc.setup(m)
			mode := os.FileMode(0o777)
			if st, _ := os.Stat(path); !st.IsDir() {
				mode = 0o644
			}
			if strings.Contains(tc.name, "group") {
				mode &^= 0o007
				mode |= 0o020
			}
			if err := os.Chmod(path, mode); err != nil {
				t.Fatal(err)
			}
			defer os.Chmod(path, 0o700) // so the test's cleanup can remove it
			for _, detach := range []bool{false, true} {
				args := []string{"daemon", "--derp-map", relay.MapURL, "--directory", dirURL}
				if detach {
					args = append(args, "--detach")
				}
				var out, errb strings.Builder
				all := append([]string{"BEAM_CONFIG_DIR=" + m.dir, "HOME=" + os.Getenv("HOME"), "PATH=" + os.Getenv("PATH")}, vars...)
				exited := make(chan int, 1)
				go func() { exited <- runWith(all, &out, &errb, args...) }()
				select {
				case code := <-exited:
					if code != 1 || !strings.Contains(errb.String(), path) || !strings.Contains(errb.String(), "chmod") {
						t.Errorf("detach %v: exit %d, %q", detach, code, errb.String())
						stopDaemon(m, vars)
					}
				case <-time.After(3 * time.Second):
					t.Errorf("detach %v: the daemon started", detach)
					stopDaemon(m, vars)
				}
				if st, _ := os.Stat(path); st.Mode().Perm() != mode {
					t.Errorf("the mode changed to %v", st.Mode().Perm())
				}
			}
		})
	}
}

// runWith is beam with vars as its whole environment and no stdin.
func runWith(vars []string, stdout, stderr io.Writer, args ...string) int {
	return cli.Main(args, vars, strings.NewReader(""), stdout, stderr)
}

// shortTemp is a temporary directory whose path leaves room for a socket.
func shortTemp(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "b")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

// stopDaemon stops a daemon a failing case started on m's directory, with
// vars naming its socket, if one answers.
func stopDaemon(m *machine, vars []string) {
	p, err := control.ResolvePaths(func(k string) string {
		for _, v := range append([]string{"BEAM_CONFIG_DIR=" + m.dir}, vars...) {
			if name, val, _ := strings.Cut(v, "="); name == k {
				return val
			}
		}
		return ""
	})
	if err != nil {
		return
	}
	for range 50 { // a detached daemon may not be listening yet
		if c, err := control.Dial(p); err == nil {
			c.Call("daemon.shutdown", nil, nil)
			c.Close()
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
}
