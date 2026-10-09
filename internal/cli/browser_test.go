//go:build beamtest && unix

package cli_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/notaharness/beam/internal/cli"
	"github.com/notaharness/beam/internal/identity"
)

// docs/10 Test authenticator: a CLI given BEAM_TEST_AUTHENTICATOR opens no
// browser for a ceremony, which answers itself; without it, on a machine
// with a display, one opens.
func TestTestAuthenticatorOpensNoBrowser(t *testing.T) {
	bin := t.TempDir()
	opened := filepath.Join(bin, "opened")
	for _, name := range []string{"open", "xdg-open"} {
		os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\ntouch "+opened+"\n"), 0o755)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	prev := owner
	t.Cleanup(func() { owner = prev; authenticate(prev) })
	for _, given := range []bool{false, true} {
		owner = identity.NewAuthenticator() // a fleet of its own, the daemon's passkey
		authenticate(owner)
		m := blank(t, "alpha")
		m.start(t)
		vars := []string{"BEAM_CONFIG_DIR=" + m.dir, "HOME=" + os.Getenv("HOME"), "PATH=" + os.Getenv("PATH"), "DISPLAY=:0"}
		if given {
			vars = append(vars, "BEAM_TEST_AUTHENTICATOR="+os.Getenv("BEAM_TEST_AUTHENTICATOR"))
		}
		var out, errb strings.Builder
		if code := cli.Main([]string{"init", "--label", "alpha"}, vars, strings.NewReader(""), &out, &errb); code != 0 {
			t.Fatalf("init: %d %q", code, errb.String())
		}
		time.Sleep(500 * time.Millisecond) // the browser starts without being waited for
		if _, err := os.Stat(opened); (err == nil) == given {
			t.Errorf("BEAM_TEST_AUTHENTICATOR given %v: a browser opened %v", given, err == nil)
		}
		os.Remove(opened)
	}
}
