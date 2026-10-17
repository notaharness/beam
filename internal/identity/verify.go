package identity

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/notaharness/beam/internal/transport"
)

// The relying party every assertion must name.
const (
	RPID   = "beam.n10.is"
	Origin = "https://beam.n10.is"
)

// Refusal is why a record is refused. Its text is the wire reason token.
type Refusal string

func (r Refusal) Error() string { return string(r) }

// Refusals, in the order verification checks for them (docs/02).
const (
	BadEntry     Refusal = "bad-entry"
	WrongPasskey Refusal = "wrong-passkey"
	BadAssertion Refusal = "bad-assertion"
	Revoked      Refusal = "revoked"
)

// Credential is the fleet's passkey as a verifier knows it.
type Credential struct {
	ID        string // base64url credential id
	PublicKey []byte // COSE public key
}

// FleetID is the lowercase hex of SHA-256 of the credential's COSE public key.
func (c Credential) FleetID() string {
	h := sha256.Sum256(c.PublicKey)
	return hex.EncodeToString(h[:])
}

// es256Key and ed25519Key are the bytes of a COSE key as CTAP2 encodes it,
// around its coordinates: {1: 2, 3: -7, -1: 1, -2: x, -3: y} and
// {1: 1, 3: -8, -1: 6, -2: x}.
var (
	es256Key   = [][]byte{{0xa5, 0x01, 0x02, 0x03, 0x26, 0x20, 0x01, 0x21, 0x58, 0x20}, {0x22, 0x58, 0x20}}
	ed25519Key = [][]byte{{0xa4, 0x01, 0x01, 0x03, 0x27, 0x20, 0x06, 0x21, 0x58, 0x20}}
)

// CanonicalKey reports whether pk is an ES256 or Ed25519 COSE key as CTAP2
// encodes it, with no other entry: the one encoding of its public key, so
// that the key names one fleet (docs/02, fleetId).
func CanonicalKey(pk []byte) bool {
	return layout(pk, es256Key) || layout(pk, ed25519Key)
}

// layout reports whether b is the parts of l, each followed by 32 bytes.
func layout(b []byte, l [][]byte) bool {
	for _, part := range l {
		if !bytes.HasPrefix(b, part) || len(b) < len(part)+32 {
			return false
		}
		b = b[len(part)+32:]
	}
	return len(b) == 0
}

// Verify checks a record (docs/02, "The membership entry"). For a member,
// revoked reports whether its peer id has a stored revocation.
func (c Credential) Verify(r Record, revoked func(peerID string) bool) error {
	if err := wellFormed(r); err != nil {
		return err
	}
	if r.Assertion == nil {
		return BadAssertion
	}
	if !sameID(r.Assertion.CredentialID, c.ID) {
		return WrongPasskey
	}
	if c.VerifyAssertion(r.Assertion, r.Challenge()) != nil {
		return BadAssertion
	}
	if r.Kind == Member && revoked(r.PeerID) {
		return Revoked
	}
	return nil
}

func wellFormed(r Record) error {
	// issuedAt needs no check: ParseRecord's canonical form admits only safe
	// integers, and every record verified here was parsed.
	if r.V != 1 || !ValidPeerID(r.PeerID) {
		return BadEntry
	}
	switch r.Kind {
	case Member:
		return wellFormedMember(r)
	case Revoke:
		if r.NodePublic != "" || r.Address != "" || r.Label != "" {
			return BadEntry
		}
		return nil
	}
	return BadEntry
}

func wellFormedMember(r Record) error {
	pub, err := base64.RawURLEncoding.DecodeString(r.NodePublic)
	if err != nil || len(pub) != 32 || PeerID([32]byte(pub)) != r.PeerID {
		return BadEntry
	}
	if k, err := transport.AddressKey(r.Address); err != nil || k != [32]byte(pub) {
		return BadEntry
	}
	if !ValidLabel(r.Label) {
		return BadEntry
	}
	return nil
}

func sameID(a, b string) bool {
	x, err1 := base64.RawURLEncoding.DecodeString(a)
	y, err2 := base64.RawURLEncoding.DecodeString(b)
	return err1 == nil && err2 == nil && len(x) > 0 && bytes.Equal(x, y)
}

// VerifyAssertion checks a get assertion over challenge for this relying
// party with user verification, ignoring the sign count.
func (c Credential) VerifyAssertion(a *Assertion, challenge []byte) error {
	raw := func(s string) []byte {
		b, _ := base64.RawURLEncoding.DecodeString(s)
		return b
	}
	car := protocol.CredentialAssertionResponse{
		PublicKeyCredential: protocol.PublicKeyCredential{
			Credential: protocol.Credential{ID: a.CredentialID, Type: string(protocol.PublicKeyCredentialType)},
			RawID:      raw(a.CredentialID),
		},
		AssertionResponse: protocol.AuthenticatorAssertionResponse{
			AuthenticatorResponse: protocol.AuthenticatorResponse{ClientDataJSON: raw(a.ClientDataJSON)},
			AuthenticatorData:     raw(a.AuthenticatorData),
			Signature:             raw(a.Signature),
		},
	}
	p, err := car.Parse()
	if err != nil {
		return err
	}
	return p.Verify(base64.RawURLEncoding.EncodeToString(challenge), RPID, "", []string{Origin}, nil, nil,
		protocol.TopOriginDefaultVerificationMode, false, true, false, c.PublicKey, protocol.SignaturePolicy{})
}
