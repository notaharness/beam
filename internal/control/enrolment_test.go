package control

import (
	"strings"
	"testing"

	"github.com/notaharness/beam/internal/identity"
)

// docs/02: an unlabelled machine takes its short host name, cut to a
// label's 64 characters, as its label.
func TestShortHost(t *testing.T) {
	long := strings.Repeat("a", 70)
	for _, tc := range []struct{ host, want string }{
		{"box", "box"},
		{"macbook.local", "macbook"},
		{long + ".local", long[:64]},
		{strings.Repeat("é", 70), strings.Repeat("é", 64)},
	} {
		got := shortHost(tc.host)
		if got != tc.want || !identity.ValidLabel(got) {
			t.Errorf("shortHost(%q) = %q, want %q, a valid label", tc.host, got, tc.want)
		}
	}
}
