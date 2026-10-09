package cli

import (
	"syscall"

	"golang.org/x/sys/windows"
)

// detachedAttr starts the detached daemon in a process group of its own,
// without a console: the console's Ctrl+C and close reach it no more than a
// terminal's hangup reaches a Unix daemon in its own session.
func detachedAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{CreationFlags: windows.CREATE_NEW_PROCESS_GROUP | windows.DETACHED_PROCESS}
}

// closeOnExec has nothing to do on Windows: a process the daemon starts
// inherits only the handles it is given, which Go lists for each one.
func closeOnExec() {}
