//go:build beamtest

package ceremony

import (
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"

	"github.com/notaharness/beam/internal/identity"
)

func init() { answer = testAnswer }

// testAnswer is the test authenticator (docs/10): with
// BEAM_TEST_AUTHENTICATOR set, a ceremony skips the browser. The
// authenticator answers as the page would and posts the result to the
// ceremony's own /cb/result, whose checks it goes through like any other.
func testAnswer(c *Ceremony) {
	var a identity.Authenticator
	if json.Unmarshal([]byte(os.Getenv("BEAM_TEST_AUTHENTICATOR")), &a) != nil {
		return // no test authenticator: the browser answers
	}
	f := url.Values{"state": {c.state}, "result": {"ok"}, "credentialId": {a.CredentialID}}
	if c.req.Op == Create {
		cd, att := a.Attest(c.req.Challenge)
		f.Set("clientDataJSON", b64(cd))
		f.Set("attestationObject", b64(att))
	} else {
		as := a.Assert(c.req.Challenge)
		f.Set("clientDataJSON", as.ClientDataJSON)
		f.Set("authenticatorData", as.AuthenticatorData)
		f.Set("signature", as.Signature)
		f.Set("prf", b64(a.PRF([]byte(identity.PRFSalt))))
	}
	resp, err := http.Post("http://127.0.0.1:"+strconv.Itoa(c.Port)+"/cb/result", "application/x-www-form-urlencoded", strings.NewReader(f.Encode()))
	if err == nil {
		resp.Body.Close()
	}
}
