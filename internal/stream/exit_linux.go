package stream

import (
	"encoding/binary"
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"
)

// siginfo_t si_code values for SIGCHLD (<asm-generic/siginfo.h>).
const (
	cldExited = 1
	cldKilled = 2
	cldDumped = 3
)

// awaitExit returns process pid's wait status once it has exited, leaving it
// unreaped: waitid with WNOWAIT, whose siginfo carries the status.
func awaitExit(pid int) (syscall.WaitStatus, error) {
	var info unix.Siginfo
	for {
		err := unix.Waitid(unix.P_PID, pid, &info, unix.WEXITED|unix.WNOWAIT, nil)
		if err == nil {
			break
		}
		if err != unix.EINTR {
			return 0, err
		}
	}
	// si_pid, si_uid and si_status follow si_signo, si_errno and si_code and
	// the union's alignment padding, on the 64-bit Linux beam builds for.
	raw := (*[unsafe.Sizeof(info)]byte)(unsafe.Pointer(&info))
	status := syscall.WaitStatus(binary.NativeEndian.Uint32(raw[24:]))
	switch info.Code {
	case cldExited:
		return status << 8, nil
	case cldDumped:
		return status | 0x80, nil
	}
	return status, nil // cldKilled: the signal
}
