package stream

import "golang.org/x/sys/unix"

// awaitExit returns once process pid has exited, leaving it unreaped.
func awaitExit(pid int) error {
	kq, err := unix.Kqueue()
	if err != nil {
		return err
	}
	defer unix.Close(kq)
	change := []unix.Kevent_t{{Ident: uint64(pid), Filter: unix.EVFILT_PROC,
		Flags: unix.EV_ADD | unix.EV_ONESHOT, Fflags: unix.NOTE_EXIT}}
	events := make([]unix.Kevent_t, 1)
	for {
		_, err := unix.Kevent(kq, change, events, nil)
		switch err {
		case unix.EINTR:
			continue
		case unix.ESRCH: // it had exited before the watch began
			return nil
		}
		return err
	}
}
