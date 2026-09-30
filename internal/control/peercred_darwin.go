package control

import "golang.org/x/sys/unix"

// peerUID is the uid of the process at the other end of the Unix socket fd.
func peerUID(fd int) (int, error) {
	cred, err := unix.GetsockoptXucred(fd, unix.SOL_LOCAL, unix.LOCAL_PEERCRED)
	if err != nil {
		return 0, err
	}
	return int(cred.Uid), nil
}
