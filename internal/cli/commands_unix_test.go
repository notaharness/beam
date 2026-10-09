//go:build beamtest && unix

package cli_test

import (
	"testing"

	"github.com/notaharness/beam/internal/stream"
)

// Commands a peer runs in the scenarios that run on every system.
var (
	trueArgv  = []string{"true"}
	sleepArgv = []string{"sleep", "30"}
)

// envArgv prints the variable name's value.
func envArgv(name string) []string { return []string{"sh", "-c", "echo $" + name} }

// openShell opens a session on peer through m that prints up and runs on.
func openShell(t *testing.T, m *machine, peer string) *stream.Conn {
	t.Helper()
	ac, _ := openPTY(t, m, peer, "sh", "-c", "echo up; exec sleep 300")
	return ac
}

// systemVerb is a child verb of this system's alone: none on Unix.
func systemVerb(string, []string) int { return 2 }
