package control

import (
	"errors"
	"io/fs"
	"os"
	"testing"
)

// docs/06: a client talks only to a socket its own user owns. One another
// user put at the path, where BEAM_SOCKET names a shared directory, is not
// this user's daemon.
func TestDialOwnSocketOnly(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root owns /dev/null")
	}
	if _, err := Dial(Paths{Socket: "/dev/null"}); !errors.Is(err, fs.ErrPermission) {
		t.Fatalf("dial a path root owns: %v, want a permission error", err)
	}
}
