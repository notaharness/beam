//go:build beamtest

package cli_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/notaharness/beam/internal/control"
	"github.com/notaharness/beam/internal/stream"
)

// Commands a peer runs in the scenarios that run on every system: this
// binary as child (child_test.go).
var (
	trueArgv  = []string{os.Args[0], "child", "exit", "0"}
	sleepArgv = []string{os.Args[0], "child", "sleep"}
)

// envArgv prints the variable name's value.
func envArgv(name string) []string { return []string{os.Args[0], "child", "env-value", name} }

// openShell opens a session on peer through m that prints up and runs on:
// an exec, Windows serving no pty.
func openShell(t *testing.T, m *machine, peer string) *stream.Conn {
	t.Helper()
	ac, _ := openExec(t, m, peer, os.Args[0], "child", "say", "up")
	return ac
}

// openExec opens an exec on peer through m and attaches to it.
func openExec(t *testing.T, m *machine, peer string, argv ...string) (*stream.Conn, string) {
	t.Helper()
	c, err := control.Connect(m.paths(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	var res struct {
		StreamID string `json:"streamId"`
	}
	if err := c.Call("exec.open", map[string]any{"peer": peer, "argv": argv}, &res); err != nil {
		t.Fatal(err)
	}
	ac, err := control.Attach(m.paths(), res.StreamID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ac.Close() })
	return ac, res.StreamID
}

// watchExec gives the test a copy of this binary and reports whether it has
// been run: as watched, it leaves a file beside itself (child_test.go).
func watchExec(t *testing.T) (bin string, started func() bool) {
	t.Helper()
	b, err := os.ReadFile(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	bin = filepath.Join(t.TempDir(), "watched.exe")
	if err := os.WriteFile(bin, b, 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, func() bool {
		_, err := os.Stat(bin + ".ran")
		return err == nil
	}
}
