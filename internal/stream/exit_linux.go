package stream

import (
	"bytes"
	"errors"
	"os"
	"strconv"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

// awaitExit returns process pid's wait status once it has exited, leaving it
// unreaped: waitid with WNOWAIT, then exit_code, field 52 of the zombie's
// /proc/<pid>/stat (proc(5)), which is the status in waitpid's form.
func awaitExit(pid int) (syscall.WaitStatus, error) {
	for {
		var info unix.Siginfo
		err := unix.Waitid(unix.P_PID, pid, &info, unix.WEXITED|unix.WNOWAIT, nil)
		if err == unix.EINTR {
			continue
		}
		if err != nil {
			return 0, err
		}
		break
	}
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return 0, err
	}
	fields := strings.Fields(string(b[bytes.LastIndexByte(b, ')')+1:])) // from field 3, after the command's name
	if len(fields) < 50 {
		return 0, errors.New("/proc stat has no exit_code")
	}
	code, err := strconv.Atoi(fields[49])
	return syscall.WaitStatus(code), err
}
