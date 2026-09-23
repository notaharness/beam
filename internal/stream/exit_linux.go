package stream

import "golang.org/x/sys/unix"

// awaitExit returns once process pid has exited, leaving it unreaped.
func awaitExit(pid int) error {
	for {
		var info unix.Siginfo
		err := unix.Waitid(unix.P_PID, pid, &info, unix.WEXITED|unix.WNOWAIT, nil)
		if err != unix.EINTR {
			return err
		}
	}
}
