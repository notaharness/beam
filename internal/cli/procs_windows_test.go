//go:build beamtest

package cli_test

import (
	"fmt"
	"testing"

	"golang.org/x/sys/windows"
)

// ended reports whether pid runs no more.
func ended(pid int) bool {
	h, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(pid))
	if err != nil {
		return true
	}
	defer windows.CloseHandle(h)
	ev, err := windows.WaitForSingleObject(h, 0)
	return err == nil && ev == windows.WAIT_OBJECT_0
}

// cleanExit reports, once m's daemon pid has exited, whether it shut down:
// it exits 0, and one killed exits as its killer says. The process is held
// open from now, for its exit code.
func cleanExit(t *testing.T, _ *machine, pid int) func() error {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { windows.CloseHandle(h) })
	return func() error {
		var code uint32
		if err := windows.GetExitCodeProcess(h, &code); err != nil {
			return err
		}
		if code != 0 {
			return fmt.Errorf("the daemon exited %d: it did not shut down cleanly", code)
		}
		return nil
	}
}
