//go:build !beamtest

package cli

import "flag"

// directoryFlag is the directory the daemon uses: beam.n10.is, which only a
// beamtest build lets a test replace.
func directoryFlag(*flag.FlagSet) *string { return new(string) }

// answersItself is false: only a beamtest build has a test authenticator.
func answersItself(func(string) string) bool { return false }
