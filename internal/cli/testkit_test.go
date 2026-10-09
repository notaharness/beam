//go:build beamtest

package cli_test

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/notaharness/beam/internal/cli"
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
