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
	if c.verifyAssertion(r.Assertion, r.Challenge()) != nil {
		return BadAssertion
	}
	if r.Kind == Member && revoked(r.PeerID) {
		return Revoked
	}
	return nil
}

func wellFormed(r Record) error {
	if r.V != 1 || !ValidPeerID(r.PeerID) || r.IssuedAt > maxSafe || r.IssuedAt < -maxSafe {
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

// verifyAssertion checks a get assertion for this relying party with user
// verification, ignoring the sign count.
func (c Credential) verifyAssertion(a *Assertion, challenge []byte) error {
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
