package identity

import (
	"bytes"
	"testing"
)

// docs/02 "fleetId": a new fleet's key is the one COSE encoding CTAP2 gives
// it, so one public key names one fleet. Each vector breaks the encoding of
// an otherwise canonical key.
func TestCanonicalKey(t *testing.T) {
	es256 := NewAuthenticator().Credential().PublicKey
	ed25519 := append([]byte{0xa4, 0x01, 0x01, 0x03, 0x27, 0x20, 0x06, 0x21, 0x58, 0x20}, bytes.Repeat([]byte{7}, 32)...)
	with := func(b []byte, f func([]byte) []byte) []byte { return f(bytes.Clone(b)) }
	for _, tc := range []struct {
		name string
		key  []byte
		ok   bool
	}{
		{"ES256", es256, true},
		{"Ed25519", ed25519, true},
		{"entries reordered", with(es256, func(b []byte) []byte { b[1], b[2], b[3], b[4] = b[3], b[4], b[1], b[2]; return b }), false},
		{"an entry more", with(es256, func(b []byte) []byte { b[0] = 0xa6; return append(b, 0x04, 0x00) }), false},
		{"a trailing byte", append(bytes.Clone(es256), 0), false},
		{"an integer not minimal", with(es256, func(b []byte) []byte { return append([]byte{0xa5, 0x01, 0x18, 0x02}, b[3:]...) }), false},
		{"an indefinite map", with(es256, func(b []byte) []byte { b[0] = 0xbf; return append(b, 0xff) }), false},
		{"another curve", with(es256, func(b []byte) []byte { b[6] = 0x02; return b }), false},
		{"ES256 with an Ed25519 header", with(ed25519, func(b []byte) []byte { return append(b, es256[42:]...) }), false},
		{"truncated", es256[:76], false},
		{"empty", nil, false},
	} {
		if got := CanonicalKey(tc.key); got != tc.ok {
			t.Errorf("CanonicalKey(%s) = %v", tc.name, got)
		}
	}
}
