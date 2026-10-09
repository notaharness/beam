//go:build beamtest

package cli_test

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"syscall"

	"golang.org/x/sys/windows"
)

// systemVerb is a child verb of Windows' alone:
//
//	signal handle    sets the event handle, inherited, if it can
//	breakaway        starts `child sleep` out of its job: prints "refused"
//	                 if it cannot, else "left", and ends it
func systemVerb(verb string, rest []string) int {
	switch verb {
	case "signal":
		h, _ := strconv.ParseUint(rest[0], 10, 64)
		if windows.SetEvent(windows.Handle(h)) != nil {
			fmt.Println("no handle")
		}
	case "breakaway":
		gc := exec.Command(os.Args[0], "child", "sleep")
		gc.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_BREAKAWAY_FROM_JOB}
		if gc.Start() != nil {
			fmt.Println("refused")
			return 0
		}
		_ = gc.Process.Kill()
		fmt.Println("left")
	default:
		return 2
	}
	return 0
}
