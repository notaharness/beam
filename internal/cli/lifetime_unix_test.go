//go:build beamtest && unix

package cli_test

import (
	"fmt"
	"os"
	"testing"
)

// cleanExit reports, once m's daemon pid has exited, whether it shut down:
// it removes its socket, which a daemon killed leaves behind.
func cleanExit(_ *testing.T, m *machine, _ int) func() error {
	return func() error {
		if _, err := os.Stat(m.paths().Socket); !os.IsNotExist(err) {
			return fmt.Errorf("the socket file is left behind: the daemon did not shut down cleanly (%v)", err)
		}
		return nil
	}
}
