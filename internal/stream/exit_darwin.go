package stream

import (
	"syscall"

	"golang.org/x/sys/unix"
)

// szomb is a zombie's p_stat (sys/proc.h).
const szomb = 5

// awaitExit returns process pid's wait status once it has exited, leaving it
// unreaped: kqueue's NOTE_EXIT with NOTE_EXITSTATUS, or, for a process that
// exited before the watch began (ESRCH), the zombie's p_xstat.
func awaitExit(pid int) (syscall.WaitStatus, error) {
	kq, err := unix.Kqueue()
	if err != nil {
		return 0, err
	}
	defer unix.Close(kq)
	change := []unix.Kevent_t{{Ident: uint64(pid), Filter: unix.EVFILT_PROC,
		Flags: unix.EV_ADD | unix.EV_ONESHOT, Fflags: unix.NOTE_EXIT | unix.NOTE_EXITSTATUS}}
	events := make([]unix.Kevent_t, 1)
	for {
		n, err := unix.Kevent(kq, change, events, nil)
		switch {
		case err == unix.EINTR:
		case err != nil:
			return 0, err
		case n == 1 && events[0].Flags&unix.EV_ERROR != 0 && unix.Errno(events[0].Data) == unix.ESRCH:
			return zombieStatus(pid)
		case n == 1 && events[0].Flags&unix.EV_ERROR != 0:
			return 0, unix.Errno(events[0].Data)
		case n == 1 && events[0].Fflags&unix.NOTE_EXIT != 0:
			return syscall.WaitStatus(events[0].Data), nil
		}
	}
}

// zombieStatus is the wait status of pid, which exited and is not reaped.
func zombieStatus(pid int) (syscall.WaitStatus, error) {
	kp, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	switch {
	case err != nil:
		return 0, err
	case kp.Proc.P_stat != szomb:
		return 0, unix.ESRCH
	}
	return syscall.WaitStatus(kp.Proc.P_xstat), nil
}
