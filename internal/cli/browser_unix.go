//go:build unix

package cli

import (
	"os/exec"
	"runtime"
)

// desktop is the browser of a machine with a display: open on macOS,
// xdg-open under X11 or Wayland.
func desktop(getenv func(string) string) func(url string) error {
	switch {
	case runtime.GOOS == "darwin":
		return start("open")
	case getenv("DISPLAY") != "" || getenv("WAYLAND_DISPLAY") != "":
		return start("xdg-open")
	}
	return nil
}

// start runs name with the URL, without waiting for it.
func start(name string) func(url string) error {
	return func(url string) error { return exec.Command(name, url).Start() }
}
