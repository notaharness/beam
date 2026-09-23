//go:build beamtest

package cli_test

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"golang.org/x/sys/unix"
)

// szomb is a zombie's p_stat (sys/proc.h).
const szomb = 5

// ended reports whether pid runs no more: it is gone, or a zombie not yet
// reaped (the acceptor keeps an exited leader until its group is torn down).
func ended(pid int) bool {
	if syscall.Kill(pid, 0) != nil {
		return true
	}
	kp, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	return err == nil && kp.Proc.P_stat == szomb
}

// watchExec gives the test an executable of its own and reports whether it
// has run. Darwin has no inotify to see the exec itself, so the executable is
// a script that leaves a marker: a start killed before the script's first
// command goes unseen here, where Linux would see it.
func watchExec(t *testing.T) (bin string, started func() bool) {
	t.Helper()
	dir := t.TempDir()
	bin, marker := filepath.Join(dir, "run-me"), filepath.Join(dir, "ran")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n: > "+marker+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, func() bool {
		_, err := os.Stat(marker)
		return err == nil
	}
}
