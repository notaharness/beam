//go:build beamtest

package cli_test

import (
	"bytes"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"

	"golang.org/x/sys/unix"
)

// ended reports whether pid runs no more: it is gone, or a zombie not yet
// reaped (the acceptor keeps an exited leader until its group is torn down).
func ended(pid int) bool {
	if syscall.Kill(pid, 0) != nil {
		return true
	}
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	i := bytes.LastIndexByte(b, ')') // the state follows the command's name
	return err == nil && i > 0 && i+2 < len(b) && b[i+2] == 'Z'
}

// watchExec copies true(1) to a file of the test's own and reports whether it
// has been executed: an exec opens it, which inotify reports, and a process
// start returns only once its exec has succeeded, so every start is seen.
func watchExec(t *testing.T) (bin string, started func() bool) {
	t.Helper()
	b, err := os.ReadFile("/bin/true")
	if err != nil {
		t.Fatal(err)
	}
	bin = filepath.Join(t.TempDir(), "run-me")
	if err := os.WriteFile(bin, b, 0o755); err != nil {
		t.Fatal(err)
	}
	fd, err := unix.InotifyInit1(unix.IN_NONBLOCK | unix.IN_CLOEXEC)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { unix.Close(fd) })
	if _, err := unix.InotifyAddWatch(fd, bin, unix.IN_OPEN); err != nil {
		t.Fatal(err)
	}
	return bin, func() bool {
		n, _ := unix.Read(fd, make([]byte, 4096))
		return n > 0
	}
}
