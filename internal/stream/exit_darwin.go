package stream

import (
	"syscall"

	"golang.org/x/sys/unix"
)

// awaitExit returns process pid's wait status once it has exited, leaving it
// unreaped: kqueue's NOTE_EXIT with NOTE_EXITSTATUS. A process that exited
// before the watch began cannot be watched (ESRCH).
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
		case n == 1:
			return syscall.WaitStatus(events[0].Data), nil
		}
	}
}
