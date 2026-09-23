package stream

import "golang.org/x/sys/unix"

// zombie reports whether pid has exited and is not yet reaped.
func zombie(pid int) bool {
	kp, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	return err == nil && kp.Proc.P_stat == szomb
}
