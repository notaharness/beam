//go:build unix

package cli

import "syscall"

// detachedAttr starts the detached daemon in a new session, without the
// terminal of the command that started it.
func detachedAttr() *syscall.SysProcAttr { return &syscall.SysProcAttr{Setsid: true} }
