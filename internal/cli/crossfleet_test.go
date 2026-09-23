//go:build beamtest

package cli_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/notaharness/beam/internal/control"
	"github.com/notaharness/beam/internal/identity"
)

// An exec attached before a reset, and not yet opened on its peer, runs
// nowhere after it: not in the fleet the reset ended, and not in the one
// enrolled after it, where the same peer is under the same id.
func TestAttachEndsWithReset(t *testing.T) {
	a := initFleet(t, "alpha")
	b := join(t, "beta")
	connectedAll(t, a, b)
	marker := filepath.Join(t.TempDir(), "ran")
	c, err := control.Connect(b.paths(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	var res struct {
		StreamID string `json:"streamId"`
	}
	if err := c.Call("exec.open", map[string]any{"peer": "alpha", "argv": []string{"touch", marker}}, &res); err != nil {
		t.Fatal(err)
	}
	reached, release := pauseAt(t, b, "attached", a.id())
	ac, err := control.Attach(b.paths(), res.StreamID)
	if err != nil {
		t.Fatal(err)
	}
	defer ac.Close()
	await(t, reached, "the attach")
	for _, m := range []*machine{a, b} {
		if r := m.beam("reset\n", "fleet", "reset"); r.code != 0 {
			t.Fatalf("reset: %+v", r)
		}
	}
	owner = identity.NewAuthenticator()
	authenticate(owner)
	if r := a.beam("", "init", "--label", "alpha"); r.code != 0 {
		t.Fatalf("init: %+v", r)
	}
	if r := b.beam("", "join", "--label", "beta"); r.code != 0 {
		t.Fatalf("join: %+v", r)
	}
	connectedAll(t, a, b)
	release()
	if msg := readClose(t, ac); msg.Reason != "connection-lost" {
		t.Errorf("close %+v, want connection-lost", msg)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Error("the exec attached in the old fleet ran in the new one")
	}
}
