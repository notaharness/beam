//go:build beamtest

package ceremony

import (
	"bytes"
	"crypto/ecdh"
	"crypto/hpke"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"time"

	"github.com/notaharness/beam/internal/identity"
)

func init() { answer = testAnswer }

// SetTimeout makes every ceremony in this process time out after d, until
// the returned function restores the timeout.
func SetTimeout(d time.Duration) (restore func()) {
	was := Timeout
	Timeout = d
	return func() { Timeout = was }
}

// testAnswer is the test authenticator (docs/10): with
// BEAM_TEST_AUTHENTICATOR set, a ceremony skips the browser. The
// authenticator answers as the page would, through the ceremony's slot,
// whose checks it goes through like any other.
func testAnswer(c *Ceremony) {
	var a identity.Authenticator
	if json.Unmarshal([]byte(os.Getenv("BEAM_TEST_AUTHENTICATOR")), &a) != nil {
		return // no test authenticator: the browser answers
	}
	_, _ = Answer(c.URL, c.worker, &a)
}

// Answer answers the ceremony at ceremonyURL as the page would with a: it
// signs the fragment's challenge, seals the result to the fragment's key and
// writes it to the fragment's slot on worker. It returns the worker's status.
func Answer(ceremonyURL, worker string, a *identity.Authenticator) (int, error) {
	frag := fragment(ceremonyURL)
	challenge, _ := base64.RawURLEncoding.DecodeString(frag.Get("c"))
	f := url.Values{"result": {"ok"}, "credentialId": {a.CredentialID}}
	if frag.Get("o") == Create {
		cd, att := a.Attest(challenge)
		f.Set("clientDataJSON", b64(cd))
		f.Set("attestationObject", b64(att))
	} else {
		as := a.Assert(challenge)
		f.Set("clientDataJSON", as.ClientDataJSON)
		f.Set("authenticatorData", as.AuthenticatorData)
		f.Set("signature", as.Signature)
		f.Set("prf", b64(a.PRF([]byte(identity.PRFSalt))))
	}
	return Write(ceremonyURL, worker, seal(frag.Get("k"), frag.Get("s"), []byte(f.Encode())))
}

// seal seals plaintext as the page does, to key (unpadded base64url) and for
// slot.
func seal(key, slot string, plaintext []byte) []byte {
	raw, _ := base64.RawURLEncoding.DecodeString(key)
	pub, err := ecdh.X25519().NewPublicKey(raw)
	if err != nil {
		panic(err)
	}
	pk, _ := hpke.NewDHKEMPublicKey(pub)
	kdf, aead := suite()
	sealed, err := hpke.Seal(pk, kdf, aead, []byte(info+slot), plaintext)
	if err != nil {
		panic(err)
	}
	return sealed
}

// Write writes sealed to the slot in ceremonyURL's fragment on worker, as the
// page does, and returns the worker's status.
func Write(ceremonyURL, worker string, sealed []byte) (int, error) {
	body, _ := json.Marshal(map[string]string{"sealed": b64(sealed)})
	resp, err := http.Post(worker+"/v1/slots/"+fragment(ceremonyURL).Get("s"), "application/json", bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	resp.Body.Close()
	return resp.StatusCode, nil
}

func fragment(ceremonyURL string) url.Values {
	u, _ := url.Parse(ceremonyURL)
	f, _ := url.ParseQuery(u.Fragment)
	return f
}
