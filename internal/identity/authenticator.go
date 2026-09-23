//go:build beamtest

package identity

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
)

// Authenticator is the software passkey behind the beamtest build tag
// (docs/10): it produces WebAuthn-shaped attestations and assertions for the
// beam relying party and a PRF computed as WebAuthn defines it. It is a
// protocol fixture, not evidence about any real authenticator. Its JSON is
// BEAM_TEST_AUTHENTICATOR's: the credential id, the PKCS #8 private key and
// the PRF secret.
type Authenticator struct {
	CredentialID string            `json:"credentialId"` // base64url
	Key          *ecdsa.PrivateKey `json:"-"`
	PRFSecret    []byte            `json:"prfSecret"`
}

type authenticatorJSON struct {
	CredentialID string `json:"credentialId"`
	PrivateKey   []byte `json:"privateKey"`
	PRFSecret    []byte `json:"prfSecret"`
}

// MarshalJSON includes the private key.
func (a *Authenticator) MarshalJSON() ([]byte, error) {
	k, err := x509.MarshalPKCS8PrivateKey(a.Key)
	if err != nil {
		return nil, err
	}
	return json.Marshal(authenticatorJSON{a.CredentialID, k, a.PRFSecret})
}

// UnmarshalJSON reads what MarshalJSON writes.
func (a *Authenticator) UnmarshalJSON(b []byte) error {
	var j authenticatorJSON
	if err := json.Unmarshal(b, &j); err != nil {
		return err
	}
	k, err := x509.ParsePKCS8PrivateKey(j.PrivateKey)
	if err != nil {
		return err
	}
	key, ok := k.(*ecdsa.PrivateKey)
	if !ok {
		return errors.New("authenticator: not an ES256 key")
	}
	*a = Authenticator{CredentialID: j.CredentialID, Key: key, PRFSecret: j.PRFSecret}
	return nil
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

// Attest is what a create for beam returns: the client data, and an
// attestation object with no attestation statement whose authenticator data
// (user present and verified, attested credential data) carries this
// credential.
func (a *Authenticator) Attest(challenge []byte) (clientData, attestationObject []byte) {
	id, _ := base64.RawURLEncoding.DecodeString(a.CredentialID)
	authData := AuthenticatorData(RPID, FlagUP|FlagUV|flagAT)
	authData = append(authData, make([]byte, 16)...) // AAGUID
	authData = append(authData, byte(len(id)>>8), byte(len(id)))
	authData = append(append(authData, id...), a.Credential().PublicKey...)
	// CBOR {"fmt": "none", "attStmt": {}, "authData": authData}
	att := []byte{0xa3, 0x63, 'f', 'm', 't', 0x64, 'n', 'o', 'n', 'e', 0x67, 'a', 't', 't', 'S', 't', 'm', 't', 0xa0,
		0x68, 'a', 'u', 't', 'h', 'D', 'a', 't', 'a', 0x59, byte(len(authData) >> 8), byte(len(authData))}
	return ClientDataJSON("webauthn.create", challenge, Origin), append(att, authData...)
}

// flagAT marks authenticator data that carries attested credential data.
const flagAT byte = 0x40

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
