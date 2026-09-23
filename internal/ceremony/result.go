package ceremony

import (
	"encoding/base64"
	"errors"
	"net/url"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/protocol/webauthncose"
	"github.com/notaharness/beam/internal/identity"
)

// Result is what the page sent back for an ok ceremony. Create fills
// AttestationObject; Get fills AuthenticatorData, Signature and PRF, the
// extension's output for identity.PRFSalt.
type Result struct {
	CredentialID      string
	ClientDataJSON    []byte
	AttestationObject []byte
	AuthenticatorData []byte
	Signature         []byte
	PRF               []byte
}

// ErrBadResult is an ok result that is not one.
var ErrBadResult = errors.New("the page's result is malformed")

// parse reads the callback's fields (docs/02, Ceremonies; binary fields are
// unpadded base64url).
func parse(f url.Values) (Result, error) {
	switch f.Get("result") {
	case "ok":
	case "prf-unsupported":
		return Result{}, ErrPRFUnsupported
	case "cancelled":
		return Result{}, ErrCancelled
	default:
		return Result{}, ErrFailed
	}
	r := Result{CredentialID: f.Get("credentialId")}
	for _, field := range []struct {
		name string
		to   *[]byte
	}{
		{"clientDataJSON", &r.ClientDataJSON}, {"attestationObject", &r.AttestationObject},
		{"authenticatorData", &r.AuthenticatorData}, {"signature", &r.Signature}, {"prf", &r.PRF},
	} {
		b, err := base64.RawURLEncoding.DecodeString(f.Get(field.name))
		if err != nil {
			return Result{}, ErrBadResult
		}
		*field.to = b
	}
	if _, err := base64.RawURLEncoding.DecodeString(r.CredentialID); err != nil || r.CredentialID == "" {
		return Result{}, ErrBadResult
	}
	return r, nil
}

// Credential verifies a create result made over challenge, for beam, with
// user verification, and returns the credential it registered.
func (r Result) Credential(challenge []byte) (identity.Credential, error) {
	raw, _ := base64.RawURLEncoding.DecodeString(r.CredentialID)
	ccr := protocol.CredentialCreationResponse{
		PublicKeyCredential: protocol.PublicKeyCredential{
			Credential: protocol.Credential{ID: r.CredentialID, Type: string(protocol.PublicKeyCredentialType)},
			RawID:      raw,
		},
		AttestationResponse: protocol.AuthenticatorAttestationResponse{
			AuthenticatorResponse: protocol.AuthenticatorResponse{ClientDataJSON: r.ClientDataJSON},
			AttestationObject:     r.AttestationObject,
		},
	}
	p, err := ccr.Parse()
	if err != nil {
		return identity.Credential{}, err
	}
	params := []protocol.CredentialParameter{
		{Type: protocol.PublicKeyCredentialType, Algorithm: webauthncose.AlgES256},
		{Type: protocol.PublicKeyCredentialType, Algorithm: webauthncose.AlgEdDSA},
	}
	if _, err := p.Verify(b64(challenge), identity.RPID, []string{identity.Origin}, nil, nil, protocol.TopOriginDefaultVerificationMode,
		false, true, true, nil, params, protocol.AttestationPolicy{}, protocol.SignaturePolicy{}); err != nil {
		return identity.Credential{}, err
	}
	return identity.Credential{ID: r.CredentialID, PublicKey: p.Response.AttestationObject.AuthData.AttData.CredentialPublicKey}, nil
}

// Assertion is a get result as a record's assertion.
func (r Result) Assertion() *identity.Assertion {
	return &identity.Assertion{
		CredentialID:      r.CredentialID,
		ClientDataJSON:    b64(r.ClientDataJSON),
		AuthenticatorData: b64(r.AuthenticatorData),
		Signature:         b64(r.Signature),
	}
}

// DirectoryKeys derives K_dir and T_read from a get result's PRF output.
func (r Result) DirectoryKeys() (kDir, tRead []byte, err error) {
	if len(r.PRF) != 32 {
		return nil, nil, ErrPRFUnsupported
	}
	kDir, tRead = identity.DirectoryKeys(r.PRF)
	return kDir, tRead, nil
}
