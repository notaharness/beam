//go:build beamtest

package cli_test

import (
	"bytes"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/creack/pty"
	"github.com/notaharness/beam/internal/ceremony"
	"github.com/notaharness/beam/internal/control"
	"github.com/notaharness/beam/internal/identity"
)

// unanswered makes ceremonies wait for the test to answer them, until the
// test ends.
func unanswered(t *testing.T) {
	os.Unsetenv("BEAM_TEST_AUTHENTICATOR")
	t.Cleanup(func() { authenticate(owner) })
}

// startCeremony runs op.start on m and returns its client and the URL.
func startCeremony(t *testing.T, m *machine, op string, params map[string]any) (*control.Client, string) {
	t.Helper()
	c, err := control.Connect(m.paths(), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	var res struct {
		CeremonyURL string `json:"ceremonyUrl"`
	}
	if err := c.Call(op+".start", params, &res); err != nil {
		t.Fatal(err)
	}
	return c, res.CeremonyURL
}

// docs/10 "onlooker answers first": someone who saw the QR code seals their
// own passkey's answer to the slot before the owner. On an enrolled machine
// it verifies under nothing it holds: revoke ends wrong-passkey and commits
// nothing, and the owner's page is told the slot was answered.
func TestOnlookerAnswersFirst(t *testing.T) {
	a := initFleet(t, "alpha")
	c := join(t, "gamma")
	connectedAll(t, a, c)
	unanswered(t)
	cl, u := startCeremony(t, a, "revoke", map[string]any{"peer": "gamma"})
	if code, err := ceremony.Answer(u, dirURL, identity.NewAuthenticator()); err != nil || code != http.StatusCreated {
		t.Fatalf("the onlooker's answer: %d, %v", code, err)
	}
	if code, err := ceremony.Answer(u, dirURL, owner); err != nil || code != http.StatusConflict {
		t.Fatalf("the owner's answer: %d, %v, want 409", code, err)
	}
	if err := cl.Call("revoke.wait", nil, nil); code(err) != "wrong-passkey" {
		t.Errorf("revoke.wait: %v, want wrong-passkey", err)
	}
	if p := a.peers(t)[c.id()]; p.RevokedAt != nil || p.State == "revoked" {
		t.Errorf("gamma revoked by the onlooker's answer: %+v", p)
	}
}

// docs/10 "junk in a slot": a slot answered with what does not open under
// the ceremony's key ends it ceremony-state, and nothing is enrolled.
func TestJunkInSlot(t *testing.T) {
	unanswered(t)
	m := blank(t, "fresh")
	m.start(t)
	cl, u := startCeremony(t, m, "join", map[string]any{"label": "fresh"})
	if code, err := ceremony.Write(u, dirURL, bytes.Repeat([]byte{1}, 120)); err != nil || code != http.StatusCreated {
		t.Fatalf("write: %d, %v", code, err)
	}
	if err := cl.Call("join.wait", nil, nil); code(err) != "ceremony-state" {
		t.Errorf("join.wait: %v, want ceremony-state", err)
	}
	if _, err := os.Stat(filepath.Join(m.dir, "fleet.json")); !os.IsNotExist(err) {
		t.Errorf("fleet.json: %v", err)
	}
}

// docs/10 "worker down during a ceremony": the answer cannot reach the slot,
// so the ceremony waits on it until its timeout, and nothing is enrolled.
func TestWorkerDownDuringCeremony(t *testing.T) {
	defer ceremony.SetTimeout(2 * time.Second)()
	worker.SetDown(true)
	defer worker.SetDown(false)
	m := blank(t, "fresh")
	m.start(t)
	if r := m.beam("", "join"); r.code != 1 || !strings.Contains(r.err, "ceremony-timeout") {
		t.Errorf("join: %+v", r)
	}
	if _, err := os.Stat(filepath.Join(m.dir, "fleet.json")); !os.IsNotExist(err) {
		t.Errorf("fleet.json: %v", err)
	}
}

// docs/07 Enrolment: on a terminal a ceremony draws its URL as a QR code in
// braille, white on black, and prints the URL under it; on a pipe it prints
// the URL alone.
func TestCeremonyDrawsQR(t *testing.T) {
	initFleet(t, "alpha")
	m := blank(t, "fresh")
	m.start(t)
	ptmx, tty, err := pty.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer ptmx.Close()
	var screen bytes.Buffer
	read := make(chan struct{})
	go func() { io.Copy(&screen, ptmx); close(read) }()
	var errb bytes.Buffer
	if code := m.run(strings.NewReader(""), tty, &errb, "join"); code != 0 {
		t.Fatalf("join: %d %s", code, errb.String())
	}
	tty.Close()
	<-read
	out := strings.ReplaceAll(screen.String(), "\r\n", "\n")
	_, shown, ok := strings.Cut(out, "waiting for your passkey (sign)\n")
	qr, rest, _ := strings.Cut(shown, "\n"+ceremony.Page+"#")
	if !ok || !strings.HasPrefix(qr, "\x1b[97;40m⣿") || strings.Count(qr, "\n") < 13 || !strings.Contains(rest, "o=a") {
		t.Errorf("terminal output:\n%s", out)
	}
	if r := m.beam("", "join"); r.code != 0 || strings.ContainsAny(r.out, "\x1b⣿") || !strings.Contains(r.out, "\n"+ceremony.Page+"#") {
		t.Errorf("pipe output: %+v", r)
	}
}

// docs/02: a fleet name is held to a label's 1–64 characters.
func TestFleetNameBound(t *testing.T) {
	m := blank(t, "fresh")
	m.start(t)
	if r := m.beam("", "init", "--fleet-name", strings.Repeat("f", 65)); r.code != 1 || !strings.Contains(r.err, "params") {
		t.Errorf("init with a 65-character fleet name: %+v", r)
	}
	if _, err := os.Stat(filepath.Join(m.dir, "fleet.json")); !os.IsNotExist(err) {
		t.Errorf("fleet.json: %v", err)
	}
}
