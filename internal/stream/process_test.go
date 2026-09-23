package stream

import (
	"os/exec"
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
