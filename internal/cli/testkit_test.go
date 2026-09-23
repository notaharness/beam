//go:build beamtest

package cli_test

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/notaharness/beam/internal/cli"
	"github.com/notaharness/beam/internal/identity"
)

// docs/10 Test kit: beam testkit serves the dev relay and the fake worker to
// daemons, which enrol through the test authenticator and connect; the kit
// ends with its stdin, a slot read still waiting on it included.
func TestTestkit(t *testing.T) {
	stdin, parent, _ := os.Pipe()
	out, w, _ := os.Pipe()
	t.Cleanup(func() { stdin.Close(); parent.Close(); out.Close(); w.Close() })
	exited := make(chan int, 1)
	go func() {
		code := cli.Main([]string{"testkit", "--exit-with-parent"}, nil, stdin, w, os.Stderr)
		w.Close() // a kit that failed to start ends the read below
		exited <- code
	}()
	var kit struct {
		DERPMap   string `json:"derpMap"`
		Directory string `json:"directory"`
	}
	if err := json.NewDecoder(out).Decode(&kit); err != nil || kit.DERPMap == "" || kit.Directory == "" {
		t.Fatalf("the kit printed %+v, %v", kit, err)
	}

	a, b := blank(t, "alpha"), blank(t, "beta")
	for _, m := range []*machine{a, b} {
		m.derpMap, m.dirURL = kit.DERPMap, kit.Directory
		m.start(t)
	}
	if r := a.beam("", "init", "--label", "alpha"); r.code != 0 {
		t.Fatalf("init: %+v", r)
	}
	if r := b.beam("", "join", "--label", "beta"); r.code != 0 {
		t.Fatalf("join: %+v", r)
	}
	waitState(t, a.enrolled(t), b.enrolled(t), "connected")

	key := make([]byte, 32)
	rand.Read(key)
	h := sha256.Sum256(key)
	read, _ := http.NewRequest("GET", kit.Directory+"/v1/slots/"+base64.RawURLEncoding.EncodeToString(h[:16]), nil)
	read.Header.Set("Authorization", "Bearer "+base64.RawURLEncoding.EncodeToString(key))
	go http.DefaultClient.Do(read) // a ceremony nobody answers
	time.Sleep(500 * time.Millisecond)
	parent.Close()
	select {
	case code := <-exited:
		if code != 0 {
			t.Errorf("the kit exited %d", code)
		}
	case <-time.After(5 * time.Second):
		t.Error("the kit outlived its stdin")
	}
}

// docs/10 Test kit: --exit-with-parent needs a stdin the parent holds.
func TestTestkitUsage(t *testing.T) {
	var errb strings.Builder
	if code := cli.Main([]string{"testkit", "--exit-with-parent"}, nil, strings.NewReader(""), os.Stdout, &errb); code != 2 ||
		!strings.Contains(errb.String(), "--exit-with-parent needs stdin") {
		t.Errorf("exit %d, %q", code, errb.String())
	}
}

// docs/10 Test authenticator: a CLI given BEAM_TEST_AUTHENTICATOR opens no
// browser for a ceremony, which answers itself; without it, on a machine
// with a display, one opens.
func TestTestAuthenticatorOpensNoBrowser(t *testing.T) {
	bin := t.TempDir()
	opened := filepath.Join(bin, "opened")
	for _, name := range []string{"open", "xdg-open"} {
		os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\ntouch "+opened+"\n"), 0o755)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	prev := owner
	t.Cleanup(func() { owner = prev; authenticate(prev) })
	for _, given := range []bool{false, true} {
		owner = identity.NewAuthenticator() // a fleet of its own, the daemon's passkey
		authenticate(owner)
		m := blank(t, "alpha")
		m.start(t)
		vars := []string{"BEAM_CONFIG_DIR=" + m.dir, "HOME=" + os.Getenv("HOME"), "PATH=" + os.Getenv("PATH"), "DISPLAY=:0"}
		if given {
			vars = append(vars, "BEAM_TEST_AUTHENTICATOR="+os.Getenv("BEAM_TEST_AUTHENTICATOR"))
		}
		var out, errb strings.Builder
		if code := cli.Main([]string{"init", "--label", "alpha"}, vars, strings.NewReader(""), &out, &errb); code != 0 {
			t.Fatalf("init: %d %q", code, errb.String())
		}
		time.Sleep(500 * time.Millisecond) // the browser starts without being waited for
		if _, err := os.Stat(opened); (err == nil) == given {
			t.Errorf("BEAM_TEST_AUTHENTICATOR given %v: a browser opened %v", given, err == nil)
		}
		os.Remove(opened)
	}
}
