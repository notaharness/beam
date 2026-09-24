//go:build beamtest

package cli

import "flag"

// directoryFlag is `beam daemon --directory URL`, in beamtest builds only: a
// daemon of its own process on the fake worker (docs/10).
func directoryFlag(fs *flag.FlagSet) *string { return fs.String("directory", "", "") }

// answersItself reports whether the test authenticator answers ceremonies
// here, so that no browser opens for them (docs/10).
func answersItself(getenv func(string) string) bool {
	return getenv("BEAM_TEST_AUTHENTICATOR") != ""
}
