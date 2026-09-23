//go:build beamtest

package cli

import "flag"

// directoryFlag is `beam daemon --directory URL`, in beamtest builds only: a
// daemon of its own process on the fake worker (docs/10).
func directoryFlag(fs *flag.FlagSet) *string { return fs.String("directory", "", "") }
