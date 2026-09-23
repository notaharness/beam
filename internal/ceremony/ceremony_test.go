//go:build beamtest

package ceremony

import (
	"bytes"
	"context"
	"crypto/ecdh"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/notaharness/beam/internal/fakeworker"
	"github.com/notaharness/beam/internal/identity"
)

// worker is the slots under test: the real worker under wrangler dev when
// BEAM_WORKER_URL names it (CI), else the fake, which this holds to the same
// contract.
func worker(t *testing.T) string {
	if u := os.Getenv("BEAM_WORKER_URL"); u != "" {
		return u
	}
	s := httptest.NewServer(fakeworker.New())
	t.Cleanup(s.Close)
	return s.URL
}

var challenge = []byte("0123456789abcdef0123456789abcdef")

const peerID = "b7f39a210c4e55d1b7f39a210c4e55d1"

// sealed is plain sealed to c's key for c's slot, as the page seals it.
func sealed(c *Ceremony, plain string) []byte {
	return seal(b64(c.key.PublicKey().Bytes()), c.slot, []byte(plain))
}

// start is a get ceremony on w that no test authenticator answers.
func start(t *testing.T, w string) *Ceremony {
	t.Helper()
	t.Setenv("BEAM_TEST_AUTHENTICATOR", "")
	c, err := Start(Request{Kind: Add, Label: "laptop", PeerID: peerID, Challenge: challenge}, w)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Close)
	return c
}

// docs/10 Test authenticator: create then get answer themselves through the
// ceremony's slot, and what they carry verifies as a browser's would.
func TestTestAuthenticator(t *testing.T) {
	w := worker(t)
	a := identity.NewAuthenticator()
	j, _ := json.Marshal(a)
	t.Setenv("BEAM_TEST_AUTHENTICATOR", string(j))
	c, err := Start(Request{Kind: Create, Label: "laptop", PeerID: peerID, Challenge: challenge, FleetName: "home"}, w)
	if err != nil {
		t.Fatal(err)
	}
	r, err := c.Wait(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	cred, err := r.Credential(challenge)
	if err != nil || cred.ID != a.CredentialID || !bytes.Equal(cred.PublicKey, a.Credential().PublicKey) {
		t.Fatalf("credential %+v, %v", cred, err)
	}
	if _, err := r.Credential([]byte("another challenge")); err == nil {
		t.Error("a create over another challenge verified")
	}
	c, _ = Start(Request{Kind: Remove, Label: "laptop", PeerID: peerID, Challenge: challenge}, w)
	r, err = c.Wait(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := cred.VerifyAssertion(r.Assertion(), challenge); err != nil {
		t.Errorf("assertion: %v", err)
	}
	kDir, _, err := r.DirectoryKeys()
	if want, _ := identity.DirectoryKeys(a.PRF([]byte(identity.PRFSalt))); err != nil || !bytes.Equal(kDir, want) {
		t.Errorf("K_dir %x, %v", kDir, err)
	}
}

// docs/02 init step 2: a create verifies only for its challenge, as a create,
// from beam's origin, for beam's RP, with the user verified. Each vector
// breaks one check of an otherwise valid create.
func TestCredentialChecks(t *testing.T) {
	a := identity.NewAuthenticator()
	create := func(typ, origin, rpID string, flags byte) Result {
		return Result{CredentialID: a.CredentialID, ClientDataJSON: identity.ClientDataJSON(typ, challenge, origin),
			AttestationObject: a.AttestationObject(rpID, flags)}
	}
	uv := identity.FlagUP | identity.FlagUV
	if _, err := create("webauthn.create", identity.Origin, identity.RPID, uv).Credential(challenge); err != nil {
		t.Fatalf("the valid create: %v", err)
	}
	for name, r := range map[string]Result{
		"type":     create("webauthn.get", identity.Origin, identity.RPID, uv),
		"origin":   create("webauthn.create", "https://beam.n10.is.example", identity.RPID, uv),
		"rpIdHash": create("webauthn.create", identity.Origin, "n10.is", uv),
		"UV":       create("webauthn.create", identity.Origin, identity.RPID, identity.FlagUP),
	} {
		if _, err := r.Credential(challenge); err == nil {
			t.Errorf("a create with the wrong %s verified", name)
		}
	}
}

// docs/02 Ceremonies: the fragment is the table's, n only for a create; the
// slot is the hash of a read key the URL does not carry, and k a key that
// the daemon holds the private half of.
func TestURL(t *testing.T) {
	for _, kind := range []string{Create, Add, Remove} {
		c, err := Start(Request{Kind: kind, Label: "lap top", PeerID: peerID, Challenge: []byte{1, 2}, FleetName: "home"}, "http://127.0.0.1:1")
		if err != nil {
			t.Fatal(err)
		}
		c.Close()
		u, _ := url.Parse(c.URL)
		h := sha256.Sum256(c.readKey)
		want := url.Values{"o": {kind}, "s": {b64(h[:16])}, "k": {b64(c.key.PublicKey().Bytes())}, "c": {"AQI"}, "l": {"lap top"},
			"f": {"b7f39a210c4e55d1"}}
		if kind == Create {
			want.Set("n", "home")
		}
		if u.Scheme+"://"+u.Host+u.Path != Page || u.Fragment != want.Encode() {
			t.Errorf("%s: %s", kind, c.URL)
		}
		if strings.Contains(c.URL, b64(c.readKey)) {
			t.Errorf("%s: the read key is in the URL", kind)
		}
	}
}

// docs/02 Ceremonies: the page's outcomes map to their errors; what does not
// open under the ceremony's key and for its slot, or is not a result, ends it
// ceremony-state.
func TestSlotResults(t *testing.T) {
	w := worker(t)
	other, _ := ecdh.X25519().GenerateKey(nil)
	for _, tc := range []struct {
		name   string
		sealed func(c *Ceremony) []byte
		err    error
	}{
		{"cancelled", func(c *Ceremony) []byte { return sealed(c, "result=cancelled") }, ErrCancelled},
		{"prf unsupported", func(c *Ceremony) []byte { return sealed(c, "result=prf-unsupported") }, ErrPRFUnsupported},
		{"failed", func(c *Ceremony) []byte { return sealed(c, "result=failed") }, ErrFailed},
		{"ok without a credential", func(c *Ceremony) []byte { return sealed(c, "result=ok") }, ErrBadResult},
		{"not a form", func(c *Ceremony) []byte { return sealed(c, "result=%zz") }, ErrState},
		{"another key", func(c *Ceremony) []byte {
			return seal(b64(other.PublicKey().Bytes()), c.slot, []byte("result=cancelled"))
		}, ErrState},
		{"another slot", func(c *Ceremony) []byte {
			return seal(b64(c.key.PublicKey().Bytes()), b64(make([]byte, 16)), []byte("result=cancelled"))
		}, ErrState},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := start(t, w)
			if code, err := Write(c.URL, w, tc.sealed(c)); err != nil || code != http.StatusCreated {
				t.Fatalf("write: %d, %v", code, err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if _, err := c.Wait(ctx); !errors.Is(err, tc.err) {
				t.Errorf("%v, want %v", err, tc.err)
			}
		})
	}
}

// A worker that fails to answer the slot read is asked again until the
// result comes.
func TestSlotReadRetries(t *testing.T) {
	defer func(d time.Duration) { retry = d }(retry)
	retry = 50 * time.Millisecond
	fake := fakeworker.New()
	s := httptest.NewServer(fake)
	defer s.Close()
	fake.SetDown(true)
	c := start(t, s.URL)
	time.Sleep(200 * time.Millisecond) // reads refused meanwhile
	fake.SetDown(false)
	if code, err := Write(c.URL, s.URL, sealed(c, "result=prf-unsupported")); err != nil || code != http.StatusCreated {
		t.Fatalf("write: %d, %v", code, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := c.Wait(ctx); !errors.Is(err, ErrPRFUnsupported) {
		t.Errorf("%v, want %v", err, ErrPRFUnsupported)
	}
}

// docs/02 Ceremonies: a result nobody waits for expires at the ceremony's
// deadline; a wait after it hears ceremony-timeout, not the stale result.
func TestAnsweredExpires(t *testing.T) {
	defer SetTimeout(500 * time.Millisecond)()
	a := identity.NewAuthenticator()
	j, _ := json.Marshal(a)
	t.Setenv("BEAM_TEST_AUTHENTICATOR", string(j))
	c, err := Start(Request{Kind: Add, PeerID: peerID, Challenge: challenge}, worker(t))
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-c.TimedOut():
	case <-time.After(5 * time.Second):
		t.Fatal("an answered ceremony nobody waited for never expired")
	}
	if _, err := c.Wait(context.Background()); !errors.Is(err, ErrTimeout) {
		t.Errorf("a wait after the deadline: %v, want %v", err, ErrTimeout)
	}
}

// Close ends the wait on the slot: a write after it is left unread.
func TestCloseStopsReading(t *testing.T) {
	w := worker(t)
	c := start(t, w)
	time.Sleep(100 * time.Millisecond) // the read is under way
	c.Close()
	Write(c.URL, w, sealed(c, "result=cancelled"))
	select {
	case o := <-c.done:
		t.Errorf("a closed ceremony read %+v", o)
	case <-time.After(300 * time.Millisecond):
	}
}
