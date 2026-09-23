package cli

import (
	"os"
	"strconv"
	"syscall"
)

// closeOnExec marks every descriptor the daemon inherited above stderr
// close-on-exec, so no process it starts inherits it (docs/07). Where
// /dev/fd cannot be listed they stay as they are.
func closeOnExec() {
	entries, _ := os.ReadDir("/dev/fd")
	for _, e := range entries {
		if fd, err := strconv.Atoi(e.Name()); err == nil && fd > 2 {
			syscall.CloseOnExec(fd) // one that closed meanwhile needs nothing
		}
	}
}
