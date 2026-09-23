//go:build beamtest

package cli_test

import (
	"database/sql"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/notaharness/beam/internal/control"
	"github.com/notaharness/beam/internal/identity"
	"github.com/notaharness/beam/internal/store"
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

// docs/02: an enrolment on disk has its entry's publication queued. When
// state.db refuses the queued write, init fails and installs nothing: no
// fleet.json, no enrolment, now or after a restart.
func TestInitQueueRefused(t *testing.T) {
	m := blank(t, "fresh")
	st, err := store.Open(filepath.Join(m.dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	st.Close()
	sqlite(t, m, func(tx *sql.Tx) error {
		_, err := tx.Exec(`CREATE TRIGGER refuse BEFORE INSERT ON pending BEGIN SELECT RAISE(ABORT, 'refused'); END`)
		return err
	})
	m.start(t)
	if r := m.beam("", "init", "--label", "fresh"); r.code != 1 {
		t.Fatalf("init: %+v, want it to fail", r)
	}
	if _, err := os.Stat(filepath.Join(m.dir, "fleet.json")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("fleet.json: %v, want none", err)
	}
	m.stop()
	m.start(t)
	var s struct{ Enrolled bool }
	if r := m.beam("", "status", "--json"); r.code != 0 || json.Unmarshal([]byte(r.out), &s) != nil || s.Enrolled {
		t.Errorf("status after a restart: %+v, want not enrolled", r)
	}
}

// A record an ended enrolment verified reaches nothing after it: a
// revocation that beta verified under the old root, and would store only
// once a reset had emptied state.db, stays out of it.
func TestResetStoresNothingOld(t *testing.T) {
	a := initFleet(t, "alpha")
	b := join(t, "beta")
	c := join(t, "gamma")
	connectedAll(t, a, b, c)
	storing, emptied := make(chan struct{}), make(chan struct{})
	stored, closed := make(chan struct{}), make(chan struct{})
	var atStoring, atEmptied, storeOn, closeOn sync.Once
	letStore := func() { storeOn.Do(func() { close(stored) }) }
	letClose := func() { closeOn.Do(func() { close(closed) }) }
	control.SetHook(func(self, point, peer string) {
		switch {
		case self == b.id() && point == "storing" && peer == c.id():
			atStoring.Do(func() { close(storing); <-stored })
		case self == "" && point == "emptied":
			atEmptied.Do(func() { close(emptied); <-closed })
		}
	})
	t.Cleanup(func() { letStore(); letClose(); control.SetHook(nil) })
	a.beam("", "revoke", "gamma")
	await(t, storing, "beta to verify the revocation")
	done := make(chan result, 1)
	go func() { done <- b.beam("reset\n", "fleet", "reset") }()
	await(t, emptied, "the reset to empty state.db")
	letStore()
	time.Sleep(200 * time.Millisecond) // the old enrolment's write, had it one
	letClose()
	if r := <-done; r.code != 0 {
		t.Fatalf("reset: %+v", r)
	}
	b.stop()
	st, err := store.Open(filepath.Join(b.dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if revoked, err := st.IsRevoked(c.id()); err != nil || revoked {
		t.Errorf("state.db after the reset: gamma revoked %v, %v", revoked, err)
	}
}
