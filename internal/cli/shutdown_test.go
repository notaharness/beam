//go:build beamtest

package cli_test

import (
	"os"
	"os/exec"
	"syscall"
	"testing"
	"time"

	"github.com/notaharness/beam/internal/control"
)

// docs/10 "shutdown ends sessions": a daemon, a process of its own stopped by
// SIGTERM, tears down the streams it serves before it exits: a pty session
// and an exec whose processes ignore SIGHUP and SIGTERM are gone by the time
// the daemon is, the pty's after its SIGKILL grace.
func TestShutdownEndsSessions(t *testing.T) {
	a, b := newMachine(t, "alpha"), newMachine(t, "beta")
	a.knows(t, b)
	b.knows(t, a)
	a.start(t)
	d := exec.Command(os.Args[0], "daemon", "--derp-map", relay.MapURL, "--directory", dirURL)
	d.Env, d.Stderr = append(os.Environ(), "BEAM_CONFIG_DIR="+b.dir), os.Stderr
	if err := d.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan struct{})
	go func() { d.Wait(); close(exited) }()
	t.Cleanup(func() { d.Process.Kill(); <-exited })
	waitFor(t, 10*time.Second, "beta's socket", func() bool { return answering(b) })
	waitState(t, a, b, "connected")

	dir := t.TempDir()
	stubborn := func(name string) []string {
		return []string{"sh", "-c", "trap '' HUP TERM; echo $$ > " + dir + "/" + name + "; exec sleep 300"}
	}
	openPTY(t, a, "beta", stubborn("pty")...)
	c, err := control.Connect(a.paths(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	var res struct {
		StreamID string `json:"streamId"`
	}
	if err := c.Call("exec.open", map[string]any{"peer": "beta", "argv": stubborn("exec")}, &res); err != nil {
		t.Fatal(err)
	}
	ac, err := control.Attach(a.paths(), res.StreamID)
	if err != nil {
		t.Fatal(err)
	}
	defer ac.Close()
	ptyPid, execPid := waitPid(t, dir+"/pty"), waitPid(t, dir+"/exec")

	start := time.Now()
	d.Process.Signal(syscall.SIGTERM)
	select {
	case <-exited:
	case <-time.After(20 * time.Second):
		t.Fatal("the daemon did not exit")
	}
	if !ended(ptyPid) || !ended(execPid) {
		t.Errorf("when the daemon exited (after %v): pty ended %v, exec ended %v", time.Since(start), ended(ptyPid), ended(execPid))
	}
	if took := time.Since(start); took > 12*time.Second {
		t.Errorf("shutdown took %v", took)
	}
}
