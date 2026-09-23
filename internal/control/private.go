package control

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
)

// private are the files in $BEAM_DIR that only their user may touch.
var private = []string{keyFile, fleetFile, stateFile, stateFile + "-wal", stateFile + "-shm", derpFileName,
	"daemon.log", "run/beam.lock"}

// Private makes p's directories, 0700 where missing, and checks docs/02's
// rule on what exists: owned by this user, the directories writable and the
// files readable by no one else. It changes no permissions.
func (p Paths) Private() error {
	for _, dir := range []string{p.Dir, filepath.Join(p.Dir, "run"), filepath.Dir(p.Socket)} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
		if err := owned(dir, 0o022, "go-w"); err != nil {
			return err
		}
	}
	for _, name := range private {
		if err := owned(p.file(name), 0o077, "go-rwx"); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	return nil
}

// owned checks that path is this user's and grants none of the bits in
// others, the fix for which is chmod fix.
func owned(path string, others fs.FileMode, fix string) error {
	st, err := os.Stat(path)
	if err != nil {
		return err
	}
	if uid := st.Sys().(*syscall.Stat_t).Uid; int(uid) != os.Getuid() {
		return fmt.Errorf("unsafe: %s belongs to uid %d, not to this user; use a directory of your own", path, uid)
	}
	if st.Mode().Perm()&others != 0 {
		return fmt.Errorf("unsafe: %s has mode %s; beam changes no permissions: chmod %s %s, or use another directory",
			path, st.Mode().Perm(), fix, path)
	}
	return nil
}
