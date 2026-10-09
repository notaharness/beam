//go:build beamtest && unix

package cli_test

import (
	"bytes"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/creack/pty"
	"github.com/notaharness/beam/internal/ceremony"
)

// docs/06: a socket path longer than a Unix socket's address holds is refused
// by name, with the variable that shortens it.
func TestLongSocketPath(t *testing.T) {
	m := &machine{dir: filepath.Join(beamDir(t), strings.Repeat("d", 100))}
	if r := m.beam("", "status"); r.code != 1 || !strings.Contains(r.err, "socket path too long") || !strings.Contains(r.err, "BEAM_SOCKET") {
		t.Errorf("status: %+v", r)
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
	_, shown, ok := strings.Cut(out, "waiting for your passkey (sign)\nAction ")
	_, shown, _ = strings.Cut(shown, "\nMachine fingerprint ")
	_, shown, _ = strings.Cut(shown, "\n")
	qr, rest, _ := strings.Cut(shown, "\n"+ceremony.Page+"#")
	if !ok || !strings.HasPrefix(qr, "\x1b[97;40m⣿") || strings.Count(qr, "\n") < 13 || !strings.Contains(rest, "o=a") {
		t.Errorf("terminal output:\n%s", out)
	}
	if r := m.beam("", "join"); r.code != 0 || strings.ContainsAny(r.out, "\x1b⣿") || !strings.Contains(r.out, "\n"+ceremony.Page+"#") {
		t.Errorf("pipe output: %+v", r)
	}
}
