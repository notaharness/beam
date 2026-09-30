package control

import (
	"errors"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"testing"
)

// docs/06: a client talks only to a daemon its own user runs, by the
// credentials of the process listening, not the owner of the path: one
// another user listens with, where BEAM_SOCKET names a shared directory, is
// not this user's daemon. dial is how Dial and Attach connect.
func TestDialOwnListenerOnly(t *testing.T) {
	dir, err := os.MkdirTemp("", "beam") // short enough for a socket path
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	path := filepath.Join(dir, "s")
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	if c, err := dial(path, os.Getuid()+1); !errors.Is(err, fs.ErrPermission) {
		t.Errorf("dial a listener of another uid: %v, want a permission error", err)
		if c != nil {
			c.Close()
		}
	}
	c, err := dial(path, os.Getuid())
	if err != nil {
		t.Fatalf("dial this user's listener: %v", err)
	}
	c.Close()
}

// docs/06: a starting daemon takes over no socket path another user owns.
func TestListenOwnPathOnly(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root owns /dev/null")
	}
	if _, err := listen("/dev/null"); !errors.Is(err, fs.ErrPermission) {
		t.Fatalf("listen on a path root owns: %v, want a permission error", err)
	}
}
