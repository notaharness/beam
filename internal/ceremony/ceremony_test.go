//go:build beamtest

package ceremony

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/notaharness/beam/internal/identity"
)

func post(t *testing.T, c *Ceremony, host string, f url.Values) *http.Response {
	t.Helper()
	req, _ := http.NewRequest("POST", "http://127.0.0.1:"+strconv.Itoa(c.Port)+"/cb/result", strings.NewReader(f.Encode()))
	req.Host = host
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp
}

func local(c *Ceremony) string { return "127.0.0.1:" + strconv.Itoa(c.Port) }

// docs/10 Test authenticator: create then get answer themselves through
// /cb/result, and what they carry verifies as a browser's would.
func TestTestAuthenticator(t *testing.T) {
	a := identity.NewAuthenticator()
	j, _ := json.Marshal(a)
	t.Setenv("BEAM_TEST_AUTHENTICATOR", string(j))
	challenge := []byte("0123456789abcdef0123456789abcdef")
	c, err := Start(Request{Op: Create, Action: "Create your fleet", Label: "laptop", Challenge: challenge, FleetName: "home"})
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
	c, _ = Start(Request{Op: Get, Challenge: challenge})
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
	challenge := []byte("0123456789abcdef0123456789abcdef")
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

// docs/02 Ceremonies: the URL's fragment carries what the page needs, and
// fleetName only for a create.
func TestURL(t *testing.T) {
	for _, op := range []string{Create, Get} {
		c, err := Start(Request{Op: op, Action: "Add laptop", Label: "laptop", Fingerprint: "b7f3 9a21 0c4e 55d1", Challenge: []byte{1, 2}, FleetName: "home"})
		if err != nil {
			t.Fatal(err)
		}
		c.Close()
		u, _ := url.Parse(c.URL)
		f, _ := url.ParseQuery(u.Fragment)
		want := url.Values{"op": {op}, "state": {c.state}, "port": {strconv.Itoa(c.Port)}, "action": {"Add laptop"},
			"label": {"laptop"}, "fingerprint": {"b7f3 9a21 0c4e 55d1"}, "challenge": {"AQI"}}
		if op == Create {
			want.Set("fleetName", "home")
		}
		if u.Scheme+"://"+u.Host+u.Path != Page || f.Encode() != want.Encode() {
			t.Errorf("%s: %s", op, c.URL)
		}
	}
}

// docs/02 Ceremonies: a callback with another state ends the ceremony
// ceremony-state; the page's own outcomes map to their errors; a request
// naming another host is refused.
func TestCallbacks(t *testing.T) {
	for _, tc := range []struct {
		name string
		host string
		form url.Values
		code int
		err  error
	}{
		{"wrong state", "", url.Values{"state": {"nope"}, "result": {"ok"}}, 400, ErrState},
		{"cancelled", "", url.Values{"result": {"cancelled"}}, 200, ErrCancelled},
		{"prf unsupported", "", url.Values{"result": {"prf-unsupported"}}, 200, ErrPRFUnsupported},
		{"failed", "", url.Values{"result": {"failed"}}, 200, ErrFailed},
		{"ok without a credential", "", url.Values{"result": {"ok"}}, 200, ErrBadResult},
		{"another host", "localhost", url.Values{"result": {"ok"}}, 400, ErrCancelled},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, err := Start(Request{Op: Get})
			if err != nil {
				t.Fatal(err)
			}
			if tc.form.Get("state") == "" {
				tc.form.Set("state", c.state)
			}
			host := local(c)
			if tc.host != "" {
				host = tc.host + ":" + strconv.Itoa(c.Port)
			}
			if resp := post(t, c, host, tc.form); resp.StatusCode != tc.code {
				t.Errorf("status %d, want %d", resp.StatusCode, tc.code)
			}
			ctx, cancel := context.WithCancel(context.Background())
			if tc.host != "" {
				cancel() // nothing arrived: the ceremony waits on
			}
			defer cancel()
			if _, err := c.Wait(ctx); !errors.Is(err, tc.err) {
				t.Errorf("%v, want %v", err, tc.err)
			}
		})
	}
}

// docs/02 Ceremonies: the page gets its answer from /cb/result although the
// result ends the wait, and the wait closes the listener, at once.
func TestResultAnswered(t *testing.T) {
	for range 100 {
		c, err := Start(Request{Op: Get})
		if err != nil {
			t.Fatal(err)
		}
		go c.Wait(context.Background())
		f := url.Values{"state": {c.state}, "result": {"failed"}}
		resp, err := http.PostForm("http://"+local(c)+"/cb/result", f)
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil || string(body) != "done, close this tab" {
			t.Fatalf("the page got %q, %v", body, err)
		}
	}
}

// A request that stalls holds Close for a second at most, then is cut off.
func TestCloseStalled(t *testing.T) {
	c, err := Start(Request{Op: Get})
	if err != nil {
		t.Fatal(err)
	}
	conn, err := net.Dial("tcp", local(c))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	fmt.Fprint(conn, "POST /cb/result HTTP/1.1\r\n")
	time.Sleep(100 * time.Millisecond) // the server has read it
	start := time.Now()
	c.Close()
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := conn.Read(make([]byte, 1)); err != io.EOF || time.Since(start) > 2*time.Second {
		t.Errorf("after %v: %v, want the connection closed", time.Since(start), err)
	}
}

// docs/02 Ceremonies: /cb is not cached and runs only its own script, which
// may post back to its own origin.
func TestLanding(t *testing.T) {
	c, err := Start(Request{Op: Get})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	resp, err := http.Get("http://" + local(c) + "/cb")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	csp := resp.Header.Get("Content-Security-Policy")
	for _, want := range []string{"default-src 'none'", "script-src '" + cspHash(landingScript) + "'", "connect-src 'self'"} {
		if !strings.Contains(csp, want) {
			t.Errorf("CSP %q lacks %q", csp, want)
		}
	}
	if resp.Header.Get("Cache-Control") != "no-store" || !strings.Contains(string(body), "<script>"+landingScript+"</script>") {
		t.Errorf("headers %v, body %s", resp.Header, body)
	}
}
