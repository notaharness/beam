//go:build beamtest

package control

import "net"

// SetHook makes every daemon in this process call f at each named point; nil
// removes it.
func SetHook(f func(self, point, peer string)) {
	if f == nil {
		hook.Store(nil)
		return
	}
	hook.Store(&f)
}

// Listen listens on the socket at path as a daemon does, for a test that
// stands in for one.
func Listen(path string) (net.Listener, error) { return listen(path) }

// OpenSocket connects to the socket at path as an attach does, for a test
// that writes its own bytes.
func OpenSocket(path string) (net.Conn, error) { return openSocket(path) }
