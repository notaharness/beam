//go:build unix

package control

import (
	"fmt"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"syscall"
)

func lock(path string) (func(), error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, ErrLocked
	}
	return func() { f.Close() }, nil
}

// listen serves the socket with mode 0600 in a directory of this user's,
// removing a file no one listens on. It takes over no path another user owns.
func listen(path string) (net.Listener, error) {
	if err := MkdirPrivate(filepath.Dir(path)); err != nil {
		return nil, err
	}
	if err := owned(path); err != nil {
		return nil, err
	}
	if c, err := net.Dial("unix", path); err == nil {
		c.Close()
		return nil, fmt.Errorf("%s: another daemon is listening", path)
	}
	os.Remove(path)
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	return ln, os.Chmod(path, 0o600)
}

// openSocket connects to the socket at path.
func openSocket(path string) (net.Conn, error) { return net.Dial("unix", path) }

// dial connects to the socket at path if its listener runs as this user.
func dial(path string) (net.Conn, error) {
	c, err := openSocket(path)
	if err != nil {
		return nil, err
	}
	if err := listenedBy(c.(*net.UnixConn), os.Getuid()); err != nil {
		c.Close()
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return c, nil
}

// listenedBy refuses a connection whose listener runs as another user than
// uid (docs/06): where BEAM_SOCKET names a shared directory, whoever listens
// there is not this user's daemon. It asks the kernel about the connection,
// so neither the path's owner nor a socket swapped in after a check counts.
func listenedBy(c *net.UnixConn, uid int) error {
	raw, err := c.SyscallConn()
	if err != nil {
		return err
	}
	var peer int
	var credErr error
	if err := raw.Control(func(fd uintptr) { peer, credErr = peerUID(int(fd)) }); err != nil {
		return err
	}
	if credErr != nil {
		return credErr
	}
	if peer != uid {
		return fmt.Errorf("listened on by another user (uid %d): %w", peer, fs.ErrPermission)
	}
	return nil
}
