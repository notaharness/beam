package control

import "golang.org/x/sys/unix"

// peerUID is the uid of the process at the other end of the Unix socket fd.
func peerUID(fd int) (int, error) {
	cred, err := unix.GetsockoptUcred(fd, unix.SOL_SOCKET, unix.SO_PEERCRED)
	if err != nil {
		return 0, err
	}
	return int(cred.Uid), nil
}
