//go:build beamtest

package cli_test

import (
	"bufio"
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/notaharness/beam/internal/ceremony"
	"github.com/notaharness/beam/internal/identity"
)

// answered runs beam args on m, with no test authenticator, and calls answer
// with each ceremony URL it shows.
func answered(t *testing.T, m *machine, answer func(url string), args ...string) (r result) {
	t.Helper()
	os.Unsetenv("BEAM_TEST_AUTHENTICATOR")
	defer authenticate(owner)
	pr, pw := io.Pipe()
	var out strings.Builder
	done := make(chan struct{})
	go func() {
		defer close(done)
		sc := bufio.NewScanner(pr)
		for sc.Scan() {
			out.WriteString(sc.Text() + "\n")
			if strings.HasPrefix(sc.Text(), ceremony.Page+"#") {
				answer(sc.Text())
			}
		}
	}()
	var errb bytes.Buffer
	r.code = m.run(strings.NewReader(""), pw, &errb, args...)
	pw.Close()
	<-done
	r.out, r.err = out.String(), errb.String()
	return r
}

// page answers ceremonies in turn as the page would, with results: ok (the
// owner's passkey) for "", else that result.
func page(t *testing.T, results ...string) func(string) {
	return func(url string) {
		if len(results) == 0 {
			return
		}
		var err error
		if results[0] == "" {
			_, err = ceremony.Answer(url, dirURL, owner)
		} else {
			_, err = ceremony.Refuse(url, dirURL, results[0])
		}
		if err != nil {
			t.Error(err)
		}
		results = results[1:]
	}
}

// docs/07 Ceremony errors: the page's results other than ok, as the ceremony
// commands print them: the token and the daemon's detail first, then what
// they mean, and for prf-unsupported the field the passkey lacked and what
// the page and the passkey need.
func TestCeremonyErrorsExplained(t *testing.T) {
	initFleet(t, "alpha")
	same := "Use the same fleet passkey; a new passkey creates a different fleet.\n"
	for _, c := range []struct {
		what    string
		results []string
		args    []string
		want    string
	}{
		{"init's create without PRF", []string{"prf-unsupported"}, []string{"init"},
			"prf-unsupported: no prf.enabled from the passkey's create\nThe selected passkey did not provide WebAuthn PRF."},
		{"init's get without PRF", []string{"", "prf-unsupported"}, []string{"init"},
			"prf-unsupported: no 32-byte prf.results.first from the passkey's get\nThe selected passkey did not provide WebAuthn PRF."},
		{"join without PRF", []string{"prf-unsupported"}, []string{"join"},
			"prf-unsupported: no 32-byte prf.results.first from the passkey's get\nThe selected passkey did not provide WebAuthn PRF. beam needs this extension to derive the encrypted fleet directory key. Browser, operating system and passkey provider must all support it.\n" +
				same + "beam needs WebAuthn PRF from the browser, the operating system and the passkey provider together, and X25519 in Web Crypto to seal the page's answer:\n"},
		{"join cancelled", []string{"cancelled"}, []string{"join"},
			"ceremony-cancelled\nPasskey request cancelled. No further approval is pending for this request.\n"},
		{"join failed", []string{"failed"}, []string{"join"},
			"bad-assertion: the browser's passkey call failed\nThe passkey request failed or its answer could not be verified. Check the browser’s message, then start again.\n"},
	} {
		t.Run(c.what, func(t *testing.T) {
			m := blank(t, "beta")
			m.start(t)
			r := answered(t, m, page(t, c.results...), c.args...)
			if r.code != 1 || !strings.HasPrefix(r.err, c.want) {
				t.Errorf("%+v\nwant stderr from %q", r, c.want)
			}
			if strings.Contains(r.err, same) != (c.args[0] == "join" && c.results[len(c.results)-1] == "prf-unsupported") {
				t.Errorf("same-passkey line: %q", r.err)
			}
		})
	}
}

// docs/07: revoke's prf-unsupported asks for the same fleet passkey too.
func TestRevokeWithoutPRF(t *testing.T) {
	a := initFleet(t, "alpha")
	connectedAll(t, a, join(t, "beta"))
	r := answered(t, a, page(t, "prf-unsupported"), "revoke", "beta")
	if r.code != 1 || !strings.HasPrefix(r.err, "prf-unsupported: no 32-byte prf.results.first from the passkey's get\n") ||
		!strings.Contains(r.err, "\nUse the same fleet passkey; a new passkey creates a different fleet.\n") {
		t.Errorf("revoke: %+v", r)
	}
}

// docs/07: a ceremony timing out, a directory that cannot be read, the
// daemon gone during the wait and a second init read as the table says.
func TestCeremonyEndsExplained(t *testing.T) {
	a := initFleet(t, "alpha")
	func() {
		defer ceremony.SetTimeout(time.Second)()
		m := blank(t, "beta")
		m.start(t)
		if r := answered(t, m, page(t), "join"); r.code != 1 || r.err != "ceremony-timeout\nThis passkey request expired after five minutes. Start again to get a new link and QR code.\n" {
			t.Errorf("timeout: %+v", r)
		}
	}()
	// a directory that answers the ceremony's slot and nothing else
	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/v1/slots/") {
			worker.ServeHTTP(w, r)
			return
		}
		http.Error(w, "down", http.StatusServiceUnavailable)
	}))
	defer down.Close()
	m := blank(t, "gamma")
	m.dirURL = down.URL
	m.start(t)
	r := m.beam("", "join")
	if r.code != 1 || !strings.HasPrefix(r.err, "directory-unavailable") ||
		!strings.HasSuffix(r.err, "\nCannot read the fleet directory. Check the connection to beam.n10.is and try joining again.\n") {
		t.Errorf("directory down: %+v", r)
	}
	lost := blank(t, "delta")
	lost.start(t)
	r = answered(t, lost, func(string) { go lost.stop() }, "join")
	if r.code != 1 || !strings.HasSuffix(r.err, "\nThe connection to beam was interrupted. Check beam status before retrying; the request may have completed.\n") {
		t.Errorf("the daemon gone during the wait: %+v", r)
	}
	if r := a.beam("", "init"); r.code != 1 || r.err != "already-enrolled\nThis machine already belongs to a fleet. Run beam status to inspect it.\n" {
		t.Errorf("init again: %+v", r)
	}
}

// docs/07: beside each ceremony's link, the Action, Machine and Machine
// fingerprint the page shows for it, in its words.
func TestCeremonyShowsAction(t *testing.T) {
	prev := owner
	owner = identity.NewAuthenticator()
	authenticate(owner)
	t.Cleanup(func() { owner = prev; authenticate(prev) })
	m := blank(t, "alpha")
	m.start(t)
	r := answered(t, m, page(t, "", ""), "init", "--label", "alpha", "--fleet-name", "homelab")
	fp := m.enrolled(t).entry.PeerID[:4] + " " + m.entry.PeerID[4:8] + " " + m.entry.PeerID[8:12] + " " + m.entry.PeerID[12:16]
	for _, want := range []string{
		"waiting for your passkey (create)\nAction               Create fleet passkey for “homelab”\nMachine              alpha\nMachine fingerprint  " + fp + "\nhttps://",
		"waiting for your passkey (sign)\nAction               Add “alpha” to fleet\nMachine              alpha\nMachine fingerprint  " + fp + "\nhttps://",
	} {
		if r.code != 0 || !strings.Contains(r.out, want) {
			t.Errorf("init: %+v\nwant %q", r, want)
		}
	}
	connectedAll(t, m, join(t, "beta"))
	r = answered(t, m, page(t, ""), "revoke", "beta")
	if want := "Action               Remove “beta” from fleet\nMachine              beta\n"; r.code != 0 || !strings.Contains(r.out, want) {
		t.Errorf("revoke: %+v\nwant %q", r, want)
	}
}

// docs/06: revoke's notifying peers comes once the revocation is verified
// and applied, before the wait on peers' acknowledgements, not after it.
func TestRevokeNotifiesPeers(t *testing.T) {
	a := initFleet(t, "alpha")
	b := join(t, "beta")
	c := join(t, "gamma")
	connectedAll(t, a, b, c)
	reached, release := pauseAt(t, b, "storing", c.id()) // beta holds the revocation, unacknowledged
	pr, pw := io.Pipe()
	lines := make(chan string, 64)
	go func() {
		sc := bufio.NewScanner(pr)
		for sc.Scan() {
			lines <- sc.Text()
		}
		close(lines)
	}()
	code := make(chan int, 1)
	go func() { code <- a.run(strings.NewReader(""), pw, io.Discard, "revoke", "gamma"); pw.Close() }()
	await(t, reached, "beta's receipt of the revocation")
	deadline := time.After(3 * time.Second) // well within the 5 s alpha waits for beta
	for waiting := true; waiting; {
		select {
		case line := <-lines:
			if line == "publishing" {
				t.Fatal("publishing before notifying peers")
			}
			waiting = line != "notifying peers"
		case <-deadline:
			t.Fatal("no notifying peers while beta's acknowledgement is outstanding")
		}
	}
	release()
	for range lines {
	}
	if n := <-code; n != 0 {
		t.Errorf("revoke: %d", n)
	}
}
