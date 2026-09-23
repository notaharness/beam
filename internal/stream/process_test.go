package stream

import (
	"bufio"
	"errors"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// D30: the leader is reaped under the lock signals take, so no signal to its
// group can follow the reap that frees the group id.
func TestReapExcludesSignals(t *testing.T) {
	cmd := exec.Command("true")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	g := newGroup(cmd)
	<-g.exited
	g.mu.Lock() // a signal under way
	reaped := make(chan struct{})
	go func() {
		g.reap()
		close(reaped)
	}()
	time.Sleep(200 * time.Millisecond)
	if syscall.Kill(cmd.Process.Pid, 0) != nil {
		t.Error("the leader was reaped while a signal to its group was under way")
	}
	g.mu.Unlock()
	<-reaped
	if syscall.Kill(cmd.Process.Pid, 0) == nil {
		t.Error("the leader was not reaped")
	}
}

// D30: the exit status is read without reaping the leader, also for one that
// exited before the watch began.
func TestExitedBeforeWatch(t *testing.T) {
	cmd, _ := start(t, "exit 7")
	waitUntil(t, "the leader's exit", func() bool { return zombie(cmd.Process.Pid) })
	ws, err := awaitExit(cmd.Process.Pid)
	if err != nil || ws.ExitStatus() != 7 {
		t.Fatalf("status %v (exit %d), %v; want exit 7", ws, ws.ExitStatus(), err)
	}
	if !zombie(cmd.Process.Pid) {
		t.Error("the leader was reaped")
	}
	_ = cmd.Wait()
}

// A watcher that fails leaves the stream's teardown intact: the group is
// killed while its leader still holds the id, never waited on under the lock
// signals take, and the leader is reaped for its status.
func TestWatcherFails(t *testing.T) {
	broken := errors.New("watcher failed")
	for _, tc := range []struct {
		name   string
		script string
		watch  func(int) (syscall.WaitStatus, error)
		want   func(syscall.WaitStatus) bool
	}{
		{"before the exit", "sleep 30",
			func(int) (syscall.WaitStatus, error) { return 0, broken },
			func(ws syscall.WaitStatus) bool { return ws.Signaled() && ws.Signal() == syscall.SIGKILL }},
		{"after the exit", "exit 7",
			func(pid int) (syscall.WaitStatus, error) { _, _ = awaitExit(pid); return 0, broken },
			func(ws syscall.WaitStatus) bool { return ws.Exited() && ws.ExitStatus() == 7 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cmd, out := start(t, "sleep 30 & echo $!; "+tc.script)
			child := childPid(t, out)
			g := watchGroup(cmd, tc.watch)
			select {
			case <-g.exited:
			case <-time.After(5 * time.Second):
				t.Fatal("the stream never learned the leader ended")
			}
			if !tc.want(g.status) {
				t.Errorf("status %v", g.status)
			}
			signalled := make(chan struct{})
			go func() { g.signal(syscall.SIGKILL); g.reap(); close(signalled) }()
			select {
			case <-signalled:
			case <-time.After(5 * time.Second):
				t.Fatal("teardown blocked")
			}
			waitUntil(t, "the group's child to die", func() bool { return syscall.Kill(child, 0) != nil })
			if syscall.Kill(cmd.Process.Pid, 0) == nil {
				t.Error("the leader was not reaped")
			}
		})
	}
}

// start runs script in its own process group, its stdout piped.
func start(t *testing.T, script string) (*exec.Cmd, *bufio.Reader) {
	t.Helper()
	cmd := exec.Command("/bin/sh", "-c", script)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) })
	return cmd, bufio.NewReader(out)
}

// childPid reads the pid the script printed first.
func childPid(t *testing.T, out *bufio.Reader) int {
	t.Helper()
	line, err := out.ReadString('\n')
	pid, perr := strconv.Atoi(strings.TrimSpace(line))
	if err != nil || perr != nil {
		t.Fatalf("child pid %q: %v", line, err)
	}
	return pid
}

func waitUntil(t *testing.T, what string, ok func() bool) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); !ok(); time.Sleep(20 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
	}
}
