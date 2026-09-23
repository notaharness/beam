//go:build beamtest

package identity

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
)

// Authenticator is the software passkey behind the beamtest build tag
// (docs/10): it produces WebAuthn-shaped assertions for the beam relying party
// and a PRF computed as WebAuthn defines it. It is a protocol fixture, not
// evidence about any real authenticator.
type Authenticator struct {
	CredentialID string            `json:"credentialId"` // base64url
	Key          *ecdsa.PrivateKey `json:"-"`
	PRFSecret    []byte            `json:"prfSecret"`
}

// NewAuthenticator makes a fresh credential.
func NewAuthenticator() *Authenticator {
	k, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	id := make([]byte, 16)
	rand.Read(id)
	secret := make([]byte, 32)
	rand.Read(secret)
	return &Authenticator{CredentialID: base64.RawURLEncoding.EncodeToString(id), Key: k, PRFSecret: secret}
}

// Credential is what a verifier knows of this authenticator.
func (a *Authenticator) Credential() Credential {
	x, y := make([]byte, 32), make([]byte, 32)
	a.Key.X.FillBytes(x)
	a.Key.Y.FillBytes(y)
	// COSE_Key {1: 2 (EC2), 3: -7 (ES256), -1: 1 (P-256), -2: x, -3: y}
	cose := append([]byte{0xa5, 0x01, 0x02, 0x03, 0x26, 0x20, 0x01, 0x21, 0x58, 0x20}, x...)
	cose = append(append(cose, 0x22, 0x58, 0x20), y...)
	return Credential{ID: a.CredentialID, PublicKey: cose}
}

// Authenticator data flags.
const (
	FlagUP byte = 0x01
	FlagUV byte = 0x04
)

// AuthenticatorData is rpIdHash ‖ flags ‖ a zero sign count.
func AuthenticatorData(rpID string, flags byte) []byte {
	h := sha256.Sum256([]byte(rpID))
	return append(h[:], flags, 0, 0, 0, 0)
}

// ClientDataJSON is the client data a browser produces for a ceremony.
func ClientDataJSON(typ string, challenge []byte, origin string) []byte {
	b, _ := json.Marshal(struct {
		Type        string `json:"type"`
		Challenge   string `json:"challenge"`
		Origin      string `json:"origin"`
		CrossOrigin bool   `json:"crossOrigin"`
	}{typ, base64.RawURLEncoding.EncodeToString(challenge), origin, false})
	return b
}

// Sign signs authenticator data and client data as an assertion does.
func (a *Authenticator) Sign(authData, clientData []byte) *Assertion {
	cd := sha256.Sum256(clientData)
	digest := sha256.Sum256(append(append([]byte{}, authData...), cd[:]...))
	sig, _ := ecdsa.SignASN1(rand.Reader, a.Key, digest[:])
	enc := base64.RawURLEncoding.EncodeToString
	return &Assertion{
		CredentialID:      a.CredentialID,
		ClientDataJSON:    enc(clientData),
		AuthenticatorData: enc(authData),
		Signature:         enc(sig),
	}
}

// Assert is a get assertion over challenge for beam, with user verification.
func (a *Authenticator) Assert(challenge []byte) *Assertion {
	return a.Sign(AuthenticatorData(RPID, FlagUP|FlagUV), ClientDataJSON("webauthn.get", challenge, Origin))
}

// SignRecord sets r's assertion.
func (a *Authenticator) SignRecord(r *Record) {
	r.Assertion = a.Assert(r.Challenge())
}

// PRF is the extension's output for salt: the client hashes the salt with
// WebAuthn's domain separator, and the authenticator MACs the result.
func (a *Authenticator) PRF(salt []byte) []byte {
	in := sha256.Sum256(append([]byte("WebAuthn PRF\x00"), salt...))
	m := hmac.New(sha256.New, a.PRFSecret)
	m.Write(in[:])
	return m.Sum(nil)
}
