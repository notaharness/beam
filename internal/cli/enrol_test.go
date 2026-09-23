//go:build beamtest

package cli_test

import (
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/notaharness/beam/internal/control"
	"github.com/notaharness/beam/internal/devderp"
	"github.com/notaharness/beam/internal/directory"
	"github.com/notaharness/beam/internal/identity"
	"tailscale.com/types/logger"
)

// initFleet enrols a fresh machine with beam init, on a passkey of the
// test's own: the owner until the test ends.
func initFleet(t *testing.T, label string) *machine {
	t.Helper()
	prev := owner
	owner = identity.NewAuthenticator()
	authenticate(owner)
	t.Cleanup(func() { owner = prev; authenticate(prev) })
	m := blank(t, label)
	m.start(t)
	r := m.beam("", "init", "--label", label)
	if r.code != 0 || !strings.Contains(r.out, "publishing\ncreated fleet ") || !strings.Contains(r.out, "published to directory") {
		t.Fatalf("init: %+v", r)
	}
	return m.enrolled(t)
}

// join enrols a fresh machine with beam join.
func join(t *testing.T, label string) *machine {
	t.Helper()
	m := blank(t, label)
	m.start(t)
	if r := m.beam("", "join", "--label", label); r.code != 0 || !strings.Contains(r.out, "reading directory\npublishing\njoined fleet ") {
		t.Fatalf("join: %+v", r)
	}
	return m.enrolled(t)
}

func (m *machine) status(t *testing.T) (st struct {
	Enrolled bool   `json:"enrolled"`
	PeerID   string `json:"peerId"`
	FleetID  string `json:"fleetId"`
}) {
	t.Helper()
	if r := m.beam("", "status", "--json"); r.code != 0 || json.Unmarshal([]byte(r.out), &st) != nil {
		t.Fatalf("status: %+v", r)
	}
	return st
}

func connectedAll(t *testing.T, ms ...*machine) {
	t.Helper()
	for _, x := range ms {
		for _, y := range ms {
			if x != y {
				waitState(t, x, y, "connected")
			}
		}
	}
}

// docs/10 "init, join": the first machine creates the fleet, the second joins
// from the directory, and each admits the other on first contact.
func TestInitJoin(t *testing.T) {
	a := initFleet(t, "alpha")
	if _, err := os.Stat(filepath.Join(a.dir, "key.json")); err != nil {
		t.Fatal(err)
	}
	if r := a.beam("", "init"); r.code != 1 || !strings.HasPrefix(r.err, "already-enrolled") {
		t.Errorf("a second init: %+v", r)
	}
	b := join(t, "beta")
	connectedAll(t, a, b)
	if st := b.status(t); st.FleetID != owner.Credential().FleetID() || st.PeerID != b.id() {
		t.Errorf("status %+v", st)
	}
}

// docs/10 "third joins while second is offline; second returns": the second
// learns the third and they connect; one row.
func TestThirdJoinsWhileSecondOffline(t *testing.T) {
	a := initFleet(t, "alpha")
	b := join(t, "beta")
	connectedAll(t, a, b)
	b.stop()
	c := join(t, "gamma")
	waitState(t, c, a, "connected")
	b.start(t)
	connectedAll(t, a, b, c)
	if ps := b.peers(t); len(ps) != 2 {
		t.Errorf("beta's peers: %v", ps)
	}
}

// docs/10 "second joins while worker withholds first's revocation of third":
// the second admits the third until the revocation arrives by sync.
func TestJoinWhileRevocationWithheld(t *testing.T) {
	a := initFleet(t, "alpha")
	c := join(t, "gamma")
	connectedAll(t, a, c)
	if r := a.beam("", "revoke", "gamma"); r.code != 0 {
		t.Fatalf("revoke: %+v", r)
	}
	worker.WithholdLast(owner.Credential().FleetID())
	a.stop()
	b := join(t, "beta")
	waitState(t, b, c, "connected")
	a.start(t)
	waitState(t, b, c, "revoked")
}

// docs/10 "revoke while all tunnels are up": the others refuse the revoked
// machine within the sync delta, which they acknowledge, so revoke does not
// wait out the 5 s. The revoked machine cannot join again (docs/02, join
// step 3).
func TestRevokeLive(t *testing.T) {
	a := initFleet(t, "alpha")
	b := join(t, "beta")
	c := join(t, "gamma")
	connectedAll(t, a, b, c)
	start := time.Now()
	r := a.beam("", "revoke", "gamma")
	want := "publishing\nrevoked \"gamma\" on this machine\npublished to directory\nacknowledged by 1 of 1 peers"
	if r.code != 0 || !strings.Contains(r.out, want) {
		t.Fatalf("revoke: %+v", r)
	}
	if d := time.Since(start); d > 4*time.Second {
		t.Errorf("revoke took %v with every peer acknowledging", d)
	}
	waitFor(t, 5*time.Second, "beta to refuse gamma", func() bool { return b.peers(t)[c.id()].RevokedAt != nil })
	if r := c.beam("", "join"); r.code != 1 || !strings.Contains(r.err, "revoked-peer") {
		t.Errorf("gamma's join: %+v", r)
	}
}

// docs/10 "revoke with worker down": the revocation takes effect here at
// once, is reported pending, and lands once the worker returns.
func TestRevokeWorkerDown(t *testing.T) {
	a := initFleet(t, "alpha")
	c := join(t, "gamma")
	connectedAll(t, a, c)
	events, err := control.Connect(a.paths(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer events.Close()
	events.Call("events.subscribe", nil, nil)
	worker.SetDown(true)
	defer worker.SetDown(false)
	if r := a.beam("", "revoke", "gamma"); r.code != 0 || !strings.Contains(r.out, "publication pending; will retry") {
		t.Fatalf("revoke: %+v", r)
	}
	waitState(t, a, c, "revoked")
	before := worker.Len(owner.Credential().FleetID())
	worker.SetDown(false)
	for {
		ev, err := events.Next()
		if err != nil {
			t.Fatal(err)
		}
		if ev.Name == "directory.published" && strings.Contains(string(ev.Data), c.id()) {
			break
		}
	}
	if n := worker.Len(owner.Credential().FleetID()); n != before+1 {
		t.Errorf("%d entries, want %d", n, before+1)
	}
	var queued int
	sqlite(t, a, func(tx *sql.Tx) error { return tx.QueryRow(`SELECT count(*) FROM pending`).Scan(&queued) })
	if queued != 0 {
		t.Errorf("%d writes still pending", queued)
	}
}

// docs/10 "wrong passkey": a join with another credential is wrong-passkey,
// whether it has a fleet of its own or not, and so is a revoke signed by one
// or by the fleet's credential with a PRF that no longer yields K_dir.
func TestWrongPasskey(t *testing.T) {
	a := initFleet(t, "alpha")
	connectedAll(t, a, join(t, "gamma"))
	authenticate(identity.NewAuthenticator())
	defer authenticate(owner)
	b := blank(t, "beta")
	b.start(t)
	if r := b.beam("", "join"); r.code != 1 || !strings.Contains(r.err, "wrong-passkey") {
		t.Errorf("join: %+v", r)
	}
	if r := b.beam("", "init"); r.code != 0 {
		t.Fatalf("the other passkey's fleet: %+v", r)
	}
	if r := a.beam("", "join"); r.code != 1 || !strings.Contains(r.err, "wrong-passkey") {
		t.Errorf("re-join with another fleet's passkey: %+v", r)
	}
	if r := a.beam("", "revoke", "gamma"); r.code != 1 || !strings.Contains(r.err, "wrong-passkey") {
		t.Errorf("revoke: %+v", r)
	}
	forked := *owner
	forked.PRFSecret = []byte("another secret")
	authenticate(&forked)
	if r := a.beam("", "revoke", "gamma"); r.code != 1 || !strings.Contains(r.err, "wrong-passkey") {
		t.Errorf("revoke with another PRF: %+v", r)
	}
}

// docs/10 "junk in the log": a validly signed entry holding another
// statement is stored by the worker and ignored by readers.
func TestJunkInLog(t *testing.T) {
	a := initFleet(t, "alpha")
	kDir, _ := identity.DirectoryKeys(owner.PRF([]byte(identity.PRFSalt)))
	fleetID := owner.Credential().FleetID()
	signed, other := newMachine(t, "signed").entry, newMachine(t, "other").entry
	e := directory.NewEntry(kDir, fleetID, signed)
	e.Blob = directory.NewEntry(kDir, fleetID, other).Blob
	if _, err := (directory.Client{URL: dirURL}).Append(context.Background(), fleetID, e); err != nil {
		t.Fatal(err)
	}
	b := blank(t, "beta")
	b.start(t)
	if r := b.beam("", "join"); r.code != 0 || !strings.Contains(r.out, "1 other machines known") {
		t.Fatalf("join: %+v", r)
	}
	if ps := b.peers(t); len(ps) != 1 || ps[a.id()].PeerID == "" {
		t.Errorf("beta's peers: %v", ps)
	}
}

// docs/10 "reset": tunnels closed, fleet state gone, key kept, re-join works.
func TestFleetReset(t *testing.T) {
	a := initFleet(t, "alpha")
	b := join(t, "beta")
	connectedAll(t, a, b)
	if r := b.beam("nope\n", "fleet", "reset"); r.code != 1 {
		t.Fatalf("an unconfirmed reset: %+v", r)
	}
	c, err := control.Connect(b.paths(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var oe *control.OpError
	if err := c.Call("fleet.reset", nil, nil); !errors.As(err, &oe) || oe.Code != "params" {
		t.Fatalf("fleet.reset without confirm: %v", err)
	}
	c.Close()
	if r := b.beam("reset\n", "fleet", "reset"); r.code != 0 {
		t.Fatalf("reset: %+v", r)
	}
	if st := b.status(t); st.Enrolled {
		t.Fatalf("status after reset: %+v", st)
	}
	if _, err := os.Stat(filepath.Join(b.dir, "fleet.json")); !os.IsNotExist(err) {
		t.Errorf("fleet.json: %v", err)
	}
	waitState(t, a, b, "offline")
	id := b.id()
	join2 := b.beam("", "join", "--label", "beta")
	if join2.code != 0 || b.enrolled(t).id() != id {
		t.Fatalf("re-join: %+v, id %s want %s", join2, b.id(), id)
	}
	connectedAll(t, a, b)
}

// docs/03 "Addresses": a machine that moves to another DERP map re-joins
// with the same key on a new address; its peers switch to it, and the older
// entry still in the directory stays superseded.
func TestRejoinOnAnotherDERPMap(t *testing.T) {
	a := initFleet(t, "alpha")
	b := join(t, "beta")
	connectedAll(t, a, b)
	moved, err := devderp.Start(logger.Discard)
	if err != nil {
		t.Fatal(err)
	}
	defer moved.Close()
	was := b.entry
	b.stop()
	b.derpMap = moved.MapURL
	b.start(t)
	if r := b.beam("", "join"); r.code != 0 {
		t.Fatalf("re-join: %+v", r)
	}
	b.enrolled(t)
	if b.id() != was.PeerID || b.entry.Address == was.Address {
		t.Fatalf("re-joined as %s at %s, was %s at %s", b.id(), b.entry.Address, was.PeerID, was.Address)
	}
	waitFor(t, 30*time.Second, "alpha to take beta's new entry", func() bool { return pinned(t, a, b.id()) == b.entry.Address })
	connectedAll(t, a, b)
	a.stop()
	a.start(t)
	connectedAll(t, a, b)
	if got := pinned(t, a, b.id()); got != b.entry.Address {
		t.Errorf("after reading the directory again alpha has %s", got)
	}
}

// pinned is the address m's peer table holds for peerID.
func pinned(t *testing.T, m *machine, peerID string) string {
	t.Helper()
	var e identity.Record
	sqlite(t, m, func(tx *sql.Tx) error {
		var b []byte
		if err := tx.QueryRow(`SELECT entry FROM peers WHERE peer_id = ?`, peerID).Scan(&b); err != nil {
			return err
		}
		return json.Unmarshal(b, &e)
	})
	return e.Address
}

// docs/07 "Enrolment": a ceremony that has to start the daemon says so
// first.
func TestCeremonyStartsDaemon(t *testing.T) {
	m := blank(t, "fresh")
	t.Cleanup(func() {
		if c, err := control.Dial(m.paths()); err == nil {
			c.Call("daemon.shutdown", nil, nil)
			c.Close()
		}
	})
	if r := m.beam("", "join", "--label", "no/slash"); !strings.HasPrefix(r.out, "starting daemon\npreparing network\n") || !strings.HasPrefix(r.err, "params") {
		t.Errorf("join: %+v", r)
	}
}

// docs/06: a queued write the worker refuses outright, here an entry whose
// statement no longer matches its assertion, is dropped rather than retried.
func TestRefusedWriteDropped(t *testing.T) {
	a := initFleet(t, "alpha")
	a.stop()
	forged := a.entry
	forged.IssuedAt++
	b, _ := json.Marshal(forged)
	h := forged.StatementHash()
	sqlite(t, a, func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO pending (statement_hash, record, created_at) VALUES (?, ?, 1)`, hex.EncodeToString(h[:]), b)
		return err
	})
	a.start(t)
	waitFor(t, 10*time.Second, "the refused write to leave the queue", func() bool {
		var queued int
		sqlite(t, a, func(tx *sql.Tx) error { return tx.QueryRow(`SELECT count(*) FROM pending`).Scan(&queued) })
		return queued == 0
	})
}

// docs/02: a daemon reads the directory at start. The second was offline
// when the first revoked the third, and the first is gone when it returns:
// the directory alone tells it.
func TestRevocationReadAtStart(t *testing.T) {
	a := initFleet(t, "alpha")
	b := join(t, "beta")
	c := join(t, "gamma")
	connectedAll(t, a, b, c)
	b.stop()
	if r := a.beam("", "revoke", "gamma"); r.code != 0 || !strings.Contains(r.out, "published to directory") {
		t.Fatalf("revoke: %+v", r)
	}
	a.stop()
	b.start(t)
	waitState(t, b, c, "revoked")
}

// docs/06: one ceremony at a time, a *.wait answers only its own *.start,
// and ceremony.cancel ends the one under way, closing its listener, so the
// next can start.
func TestOneCeremonyAtATime(t *testing.T) {
	m := blank(t, "fresh")
	m.start(t)
	c, err := control.Connect(m.paths(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	code := func(err error) string {
		var oe *control.OpError
		if errors.As(err, &oe) {
			return oe.Code
		}
		return fmt.Sprint(err)
	}
	start := map[string]any{"label": "fresh"}
	var first struct {
		CeremonyURL string `json:"ceremonyUrl"`
	}
	if err := c.Call("join.start", start, &first); err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(first.CeremonyURL)
	frag, _ := url.ParseQuery(u.Fragment)
	for _, step := range []struct{ op, want string }{
		{"join.start", "busy"},
		{"init.wait", "ceremony-state"},
		{"ceremony.cancel", "<nil>"},
		{"join.start", "<nil>"},
	} {
		if got := code(c.Call(step.op, start, nil)); got != step.want {
			t.Errorf("%s: %s, want %s", step.op, got, step.want)
		}
	}
	waitFor(t, 5*time.Second, "the cancelled ceremony's listener to close", func() bool {
		conn, err := net.Dial("tcp", "127.0.0.1:"+frag.Get("port"))
		if err == nil {
			conn.Close()
		}
		return err != nil
	})
}
