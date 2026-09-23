//go:build beamtest

package control

// SetHook makes every daemon in this process call f at each named point; nil
// removes it.
func SetHook(f func(self, point, peer string)) {
	if f == nil {
		hook.Store(nil)
		return
	}
	hook.Store(&f)
}
