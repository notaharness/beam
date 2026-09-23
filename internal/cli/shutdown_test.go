//go:build beamtest

package cli_test

import (
	"os/exec"
	"syscall"
	"testing"
	"time"

	"github.com/notaharness/beam/internal/control"
)

// served is beta, an enrolled daemon in a process of its own, which alpha
// reaches, serving a pty and an exec that alpha opened; each writes its pid
// to dir/pty and dir/exec. exited closes once beta's daemon has exited.
func served(t *testing.T, dir, ptyScript, execScript string) (b *machine, d *exec.Cmd, exited <-chan struct{}) {
	t.Helper()
	a := newMachine(t, "alpha")
	b = newMachine(t, "beta")
	a.knows(t, b)
	b.knows(t, a)
	a.start(t)
	d, exited = b.process(t)
	waitFor(t, 10*time.Second, "beta's socket", func() bool { return answering(b) })
	waitState(t, a, b, "connected")

	openPTY(t, a, "beta", "sh", "-c", ptyScript)
	c, err := control.Connect(a.paths(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	var res struct {
		StreamID string `json:"streamId"`
	}
	if err := c.Call("exec.open", map[string]any{"peer": "beta", "argv": []string{"sh", "-c", execScript}}, &res); err != nil {
		t.Fatal(err)
	}
	ac, err := control.Attach(a.paths(), res.StreamID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ac.Close() })
	waitPid(t, dir+"/pty")
	waitPid(t, dir+"/exec")
	return b, d, exited
}

// stubborn is a script that ignores SIGHUP and writes its pid to file.
func stubborn(file string) string { return "trap '' HUP; echo $$ > " + file + "; exec sleep 300" }

// docs/10 "shutdown ends sessions": a daemon, a process of its own stopped by
// SIGTERM, stops taking connections at once and tears down the streams it
// serves before it exits: a pty session and an exec whose processes ignore
// SIGHUP are gone by the time the daemon is, the pty's after its SIGKILL
// grace. A process that left the exec's group (docs/04) keeps its output
// open, and does not hold the shutdown.
func TestShutdownEndsSessions(t *testing.T) {
	dir := t.TempDir()
	// The escaped pid is written whole, as the cleanup kills it.
	escape := "perl -e 'setpgrp; exec \"sleep\", \"300\"' & echo $! > " + dir + "/e && mv " + dir + "/e " + dir + "/escaped; " +
		stubborn(dir+"/exec")
	b, d, exited := served(t, dir, stubborn(dir+"/pty"), escape)
	ptyPid, execPid, escaped := waitPid(t, dir+"/pty"), waitPid(t, dir+"/exec"), waitPid(t, dir+"/escaped")
	t.Cleanup(func() { syscall.Kill(escaped, syscall.SIGKILL) })

	start := time.Now()
	d.Process.Signal(syscall.SIGTERM)
	waitFor(t, 2*time.Second, "beta's socket to close", func() bool { return !answering(b) })
	select {
	case <-exited:
		t.Fatal("the daemon exited before its pty session's grace")
	default:
	}
	select {
	case <-exited:
	case <-time.After(20 * time.Second):
		t.Fatal("the daemon did not exit")
	}
	if !ended(ptyPid) || !ended(execPid) {
		t.Errorf("when the daemon exited: pty ended %v, exec ended %v", ended(ptyPid), ended(execPid))
	}
	if took := time.Since(start); took > 8*time.Second {
		t.Errorf("shutdown took %v: the escaped process held it", took)
	}
}

// docs/06 Shutdown: a daemon told to stop while a fleet reset tears its
// streams down waits for that teardown too.
func TestShutdownDuringReset(t *testing.T) {
	dir := t.TempDir()
	b, d, exited := served(t, dir, stubborn(dir+"/pty"), stubborn(dir+"/exec"))
	ptyPid := waitPid(t, dir+"/pty")
	go b.beam("reset\n", "fleet", "reset")
	time.Sleep(time.Second) // the reset is waiting on the pty's grace
	d.Process.Signal(syscall.SIGTERM)
	select {
	case <-exited:
	case <-time.After(20 * time.Second):
		t.Fatal("the daemon did not exit")
	}
	if !ended(ptyPid) {
		t.Error("the pty session outlived the daemon")
	}
}
