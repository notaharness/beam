//go:build unix

package control

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
)

// configDir is $XDG_CONFIG_HOME/beam, else ~/.config/beam (docs/02).
func configDir(getenv func(string) string) (string, error) {
	switch {
	case getenv("XDG_CONFIG_HOME") != "":
		return filepath.Join(getenv("XDG_CONFIG_HOME"), "beam"), nil
	case getenv("HOME") != "":
		return filepath.Join(getenv("HOME"), ".config", "beam"), nil
	}
	return "", errors.New("no BEAM_CONFIG_DIR, XDG_CONFIG_HOME or HOME")
}

// defaultSocket is run/beam.sock in dir (docs/06).
func defaultSocket(dir string) string { return filepath.Join(dir, "run", "beam.sock") }

// maxSocket is the longest socket path a Unix socket address holds here: 107
// bytes on Linux, 103 on macOS.
var maxSocket = len(syscall.RawSockaddrUnix{}.Path) - 1

// checkSocket refuses a socket path longer than a socket address holds.
func checkSocket(sock string) error {
	if len(sock) > maxSocket {
		return fmt.Errorf("socket path too long (%d bytes, at most %d): %s; set BEAM_SOCKET to a shorter one", len(sock), maxSocket, sock)
	}
	return nil
}

// MkdirPrivate makes dir, and any parent it lacks, for this user alone.
func MkdirPrivate(dir string) error { return os.MkdirAll(dir, 0o700) }

// owned refuses a path another user owns (docs/06): a starting daemon takes
// over no socket someone else put at a shared path. A path with nothing at it
// is left for the listen.
func owned(path string) error {
	st, err := os.Stat(path)
	if err != nil {
		return nil
	}
	if uid := st.Sys().(*syscall.Stat_t).Uid; int(uid) != os.Getuid() {
		return fmt.Errorf("%s belongs to another user (uid %d): %w", path, uid, fs.ErrPermission)
	}
	return nil
}
