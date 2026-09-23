package directory

import (
	"bytes"
	"crypto/rand"
	"testing"

	"github.com/notaharness/beam/internal/identity"
)

func record(label string) identity.Record {
	return identity.Record{V: 1, Kind: identity.Member, PeerID: "0123456789abcdef0123456789abcdef", Label: label, IssuedAt: 1}
}

// docs/02 The directory: a blob opens only under its K_dir, for its fleet and
// statement hash, and only if the record inside hashes to that statement.
func TestSealOpen(t *testing.T) {
	kDir := make([]byte, 32)
	rand.Read(kDir)
	other := make([]byte, 32)
	rand.Read(other)
	const fleet = "f00d"
	r, junk := record("a"), record("b")
	h, blob := Seal(kDir, fleet, r)
	jh, jblob := Seal(kDir, fleet, junk)
	flipped := bytes.Clone(blob)
	flipped[len(flipped)-1] ^= 1
	for _, tc := range []struct {
		name  string
		kDir  []byte
		fleet string
		hash  []byte
		blob  []byte
		ok    bool
	}{
		{"its own", kDir, fleet, h[:], blob, true},
		{"another K_dir", other, fleet, h[:], blob, false},
		{"another fleet", kDir, "beef", h[:], blob, false},
		{"another statement hash", kDir, fleet, jh[:], blob, false},
		{"altered", kDir, fleet, h[:], flipped, false},
		{"short", kDir, fleet, h[:], blob[:10], false},
		{"a blob sealed for another statement", kDir, fleet, h[:], jblob, false},
	} {
		got, err := Open(tc.kDir, tc.fleet, tc.hash, tc.blob)
		if tc.ok != (err == nil) || (tc.ok && got.Label != r.Label) {
			t.Errorf("%s: %+v, %v", tc.name, got, err)
		}
	}
}
