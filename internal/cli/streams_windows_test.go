//go:build beamtest

package cli_test

import (
	"strings"
	"testing"
)

// docs/04: a Windows acceptor serves no pty and, as yet, no exec; each is
// refused kind, which connect and exec report and exit 1 on.
func TestStreamsRefusedOnWindows(t *testing.T) {
	ms := fleet(t, "alpha", "beta")
	a, b := ms[0], ms[1]
	waitState(t, a, b, "connected")
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"connect", "beta"}, "kind: pty is not supported on windows"},
		{[]string{"exec", "beta", "--", "cmd", "/c", "exit"}, "kind: exec is not supported on windows"},
	} {
		if r := a.beam("", tc.args...); r.code != 1 || !strings.Contains(r.err, tc.want) {
			t.Errorf("%v: %+v, want exit 1 and %s", tc.args, r, tc.want)
		}
	}
}
