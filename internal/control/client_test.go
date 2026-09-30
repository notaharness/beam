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
// not this user's daemon.
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
	c, err := Dial(Paths{Socket: path})
	if err != nil {
		t.Fatalf("dial this user's listener: %v", err)
	}
	defer c.Close()
	conn := c.c.(*net.UnixConn)
	if err := listenedBy(conn, os.Getuid()+1); !errors.Is(err, fs.ErrPermission) {
		t.Errorf("a listener of another uid: %v, want a permission error", err)
	}
	if err := listenedBy(conn, os.Getuid()); err != nil {
		t.Errorf("a listener of this uid: %v", err)
	}
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
