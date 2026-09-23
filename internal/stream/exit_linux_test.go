package stream

import (
	"os"
	"os/exec"
	"syscall"
	"testing"

	"golang.org/x/sys/unix"
)

// A process that makes itself non-dumpable hides its exit code from
// /proc/<pid>/stat; the status is still its own.
func TestNonDumpableExitStatus(t *testing.T) {
	if os.Getenv("BEAM_TEST_NONDUMPABLE") != "" {
		_ = unix.Prctl(unix.PR_SET_DUMPABLE, 0, 0, 0, 0)
		os.Exit(7)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestNonDumpableExitStatus$")
	cmd.Env = append(os.Environ(), "BEAM_TEST_NONDUMPABLE=1")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	g := newGroup(cmd)
	<-g.exited
	g.reap()
	if m := exitMsg(g.status); *m.ExitCode != 7 || m.Signal != nil {
		t.Fatalf("close %+v (exit %d), want exit 7", m, *m.ExitCode)
	}
}
