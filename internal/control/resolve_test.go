package control

import (
	"slices"
	"testing"

	"github.com/notaharness/beam/internal/identity"
	"github.com/notaharness/beam/internal/store"
)

// docs/07 Peer arguments: an id prefix reaches any peer; an alias or label
// reaches a revoked peer only if no other peer has it.
func TestMatching(t *testing.T) {
	alias := "box"
	peer := func(id, label string, revoked bool) store.Peer {
		return store.Peer{Entry: identity.Record{PeerID: id, Label: label}, Revoked: revoked}
	}
	old, cur, other := peer("aaaaaaaa01", "beta", true), peer("bbbbbbbb02", "beta", false), peer("cccccccc03", "gamma", true)
	other.Alias = &alias
	ps := []store.Peer{old, cur, other}
	for _, tc := range []struct {
		arg  string
		want []string
	}{
		{"beta", []string{"bbbbbbbb02"}},
		{"gamma", []string{"cccccccc03"}},
		{"box", []string{"cccccccc03"}},
		{"aaaaaaaa", []string{"aaaaaaaa01"}},
		{"delta", nil},
	} {
		var got []string
		for _, p := range matching(ps, tc.arg) {
			got = append(got, p.Entry.PeerID)
		}
		if !slices.Equal(got, tc.want) {
			t.Errorf("matching(%q) = %v, want %v", tc.arg, got, tc.want)
		}
	}
	if got := matching([]store.Peer{cur, peer("dddddddd04", "beta", false)}, "beta"); len(got) != 2 {
		t.Errorf("two current peers labelled beta: %d matches, want 2", len(got))
	}
}
