//go:build beamtest

package cli_test

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"
)

// systemVerb is a child verb of Windows' alone:
//
//	breakaway        starts `child sleep` out of its job: prints "refused"
//	                 if it cannot, else "left", and ends it
func systemVerb(verb string, rest []string) int {
	switch verb {
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
