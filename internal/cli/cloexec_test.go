//go:build beamtest

package cli_test

import (
	"os"
	"testing"
)

// docs/07 The daemon: a descriptor the daemon inherited, beyond stdin, stdout
// and stderr, is not handed on to the processes it starts for peers. Here
// the daemon inherits fd 3, as one started from Electron inherits some.
func TestInheritedFdsStayHome(t *testing.T) {
	extra, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer extra.Close()
	a, _, _, _ := pair(t, extra)
	ac, _ := openPTY(t, a, "beta", "sh", "-c", "if [ -e /dev/fd/3 ]; then echo fd3=open; else echo fd3=closed; fi; exec sleep 300")
	readUntil(t, ac, "fd3=closed")
}
