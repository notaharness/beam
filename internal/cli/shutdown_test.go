//go:build beamtest

package cli_test

import (
	"os"
	"testing"
	"time"

	"github.com/notaharness/beam/internal/stream"
)

// docs/04, docs/06 Shutdown: a daemon that stops tears down the streams it
// serves before it exits: a pty session whose processes ignore SIGHUP gets
// its SIGKILL after the grace, and is gone by the time the daemon is.
func TestShutdownEndsSessions(t *testing.T) {
	b := fleet(t, "beta")[0]
	tun, _ := rawTunnel(t, b)
	dir := t.TempDir()
	os.WriteFile(dir+"/child.sh", []byte("trap '' HUP; echo $$ > "+dir+"/child; exec sleep 300\n"), 0o600)
	rawOpen(t, tun, stream.Header{V: 1, Kind: stream.KindPTY, Cols: 80, Rows: 24, Argv: []string{"sh", "-c",
		"trap '' HUP; sh " + dir + "/child.sh >/dev/null 2>&1 </dev/null & echo $$ > " + dir + "/leader; exec sleep 300"}})
	leader, child := waitPid(t, dir+"/leader"), waitPid(t, dir+"/child")
	start := time.Now()
	b.stop()
	if !ended(leader) || !ended(child) {
		t.Errorf("after the daemon stopped (%v): leader ended %v, child ended %v", time.Since(start), ended(leader), ended(child))
	}
	if d := time.Since(start); d > 12*time.Second {
		t.Errorf("shutdown took %v", d)
	}
}
