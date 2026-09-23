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
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/notaharness/beam/internal/ceremony"
	"github.com/notaharness/beam/internal/control"
	"github.com/notaharness/beam/internal/devderp"
	"github.com/notaharness/beam/internal/directory"
	"github.com/notaharness/beam/internal/fakeworker"
	"github.com/notaharness/beam/internal/identity"
	"github.com/notaharness/beam/internal/transport"
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

// docs/02: init has no directory to read, the fleet being new, and logs no
// error for one; a join reads the directory once, itself.
func TestEnrolmentReadsNoDirectoryAgain(t *testing.T) {
	var mu sync.Mutex
	var lines []string
	logf := func(format string, args ...any) {
		mu.Lock()
		lines = append(lines, fmt.Sprintf(format, args...))
		mu.Unlock()
	}
	prev := owner
	owner = identity.NewAuthenticator()
	authenticate(owner)
	t.Cleanup(func() { owner = prev; authenticate(prev) })
	a, b := blank(t, "alpha"), blank(t, "beta")
	a.logf, b.logf = logf, logf
	a.start(t)
	b.start(t)
	fleetID := owner.Credential().FleetID()
	if r := a.beam("", "init", "--label", "alpha"); r.code != 0 {
		t.Fatalf("init: %+v", r)
	}
	reads := worker.Reads(fleetID)
	if r := b.beam("", "join", "--label", "beta"); r.code != 0 {
		t.Fatalf("join: %+v", r)
	}
	connectedAll(t, a.enrolled(t), b.enrolled(t))
	if n := worker.Reads(fleetID) - reads; n != 1 {
		t.Errorf("the join read the directory %d times", n)
	}
	mu.Lock()
	defer mu.Unlock()
	for _, l := range lines {
		if strings.Contains(l, "directory") {
			t.Errorf("logged %q", l)
		}
	}
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
	var queued int
	sqlite(t, a, func(tx *sql.Tx) error { return tx.QueryRow(`SELECT count(*) FROM pending`).Scan(&queued) })
	if queued != 0 {
		t.Errorf("%d writes queued once published", queued)
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

// docs/02 join: on a machine with fleet.json the root is pinned. A page that
// kept the PRF output and signs with a credential of its own, which a hostile
// directory vouches for, cannot re-join the machine into that credential's
// fleet.
func TestRejoinKeepsRoot(t *testing.T) {
	a := initFleet(t, "alpha")
	thief := identity.NewAuthenticator()
	thief.PRFSecret = owner.PRFSecret
	hostile := httptest.NewServer(fakeworker.New())
	defer hostile.Close()
	authenticate(thief)
	defer authenticate(owner)
	x := blank(t, "x")
	x.dirURL = hostile.URL
	x.start(t)
	if r := x.beam("", "init"); r.code != 0 {
		t.Fatalf("the thief's fleet, under the same read token: %+v", r)
	}
	a.stop()
	a.dirURL = hostile.URL
	a.start(t)
	if r := a.beam("", "join"); r.code != 1 || !strings.Contains(r.err, "wrong-passkey") {
		t.Errorf("re-join signed by another credential: %+v", r)
	}
	if st := a.status(t); st.FleetID != owner.Credential().FleetID() {
		t.Errorf("fleet %s, want %s", st.FleetID, owner.Credential().FleetID())
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
// Mail sent after the re-join reaches the peer: a machine's message counter
// belongs to its key, which the reset keeps (docs/05).
func TestFleetReset(t *testing.T) {
	a := initFleet(t, "alpha")
	b := join(t, "beta")
	connectedAll(t, a, b)
	send := func(payload string) {
		t.Helper()
		if r := b.beam("", "msg", "send", "alpha", payload); r.out != "delivered to alpha\n" {
			t.Fatalf("send %s: %+v", payload, r)
		}
	}
	send("before 1")
	send("before 2")
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
	send("after 1")
	send("after 2")
	c, next := subscribeMail(t, a, nil)
	for _, want := range []string{"before 1", "before 2", "after 1", "after 2"} {
		e := next()
		if e.Payload != want {
			t.Errorf("alpha has %q, want %q", e.Payload, want)
		}
		if err := c.Call("msg.ack", map[string]any{"envelopeId": e.ID}, nil); err != nil {
			t.Fatal(err)
		}
	}
}

// docs/02 reset: a reset ends the ceremony under way, and one whose result is
// already being handled commits nothing; one committing finishes first. The
// machine ends reset.
func TestResetEndsCeremony(t *testing.T) {
	a := initFleet(t, "alpha")
	b := join(t, "beta")
	connectedAll(t, a, b)
	rejoin := func(t *testing.T) *control.Client {
		t.Helper()
		if !b.status(t).Enrolled {
			if r := b.beam("", "join", "--label", "beta"); r.code != 0 {
				t.Fatalf("join: %+v", r)
			}
		}
		c, err := control.Connect(b.paths(), nil)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { c.Close() })
		if err := c.Call("join.start", map[string]any{"label": "beta"}, nil); err != nil {
			t.Fatal(err)
		}
		return c
	}
	reset := func(t *testing.T) {
		t.Helper()
		if r := b.beam("reset\n", "fleet", "reset"); r.code != 0 {
			t.Fatalf("reset: %+v", r)
		}
	}
	reenrolled := func(t *testing.T) {
		t.Helper()
		if st := b.status(t); st.Enrolled {
			t.Errorf("enrolled after the reset: %+v", st)
		}
		if _, err := os.Stat(filepath.Join(b.dir, "fleet.json")); !os.IsNotExist(err) {
			t.Errorf("fleet.json: %v", err)
		}
	}
	t.Run("waiting", func(t *testing.T) {
		c := rejoin(t)
		reset(t)
		if err := c.Call("join.wait", nil, nil); code(err) != "ceremony-state" {
			t.Errorf("join.wait after the reset: %v", err)
		}
		reenrolled(t)
	})
	t.Run("handling", func(t *testing.T) {
		c := rejoin(t)
		reached, release := pauseAt(t, b, "enrolling", b.id())
		waited := make(chan error, 1)
		go func() { waited <- c.Call("join.wait", nil, nil) }()
		await(t, reached, "the join's result to be handled")
		reset(t)
		release()
		if err := <-waited; code(err) != "ceremony-cancelled" {
			t.Errorf("join.wait across the reset: %v", err)
		}
		reenrolled(t)
	})
	t.Run("committing", func(t *testing.T) {
		c := rejoin(t)
		reached, release := pauseAt(t, b, "committing", "")
		go c.Call("join.wait", nil, nil)
		await(t, reached, "the join to commit")
		done := make(chan struct{})
		go func() { reset(t); close(done) }()
		select {
		case <-done:
			t.Error("the reset overtook the commit")
		case <-time.After(300 * time.Millisecond):
		}
		release()
		<-done
		reenrolled(t)
	})
}

// An enrolment a reset ended changes nothing in the one after it: a
// revocation the old fleet's sync was applying when the reset came does not
// touch the same peer in the new fleet.
func TestOldFleetRevokesNothing(t *testing.T) {
	a := initFleet(t, "alpha")
	b := join(t, "beta")
	c := join(t, "gamma")
	connectedAll(t, a, b, c)
	reached, release := pauseAt(t, b, "revoking", c.id())
	a.beam("", "revoke", "gamma")
	await(t, reached, "beta to apply the revocation")
	for _, m := range []*machine{b, c} {
		if r := m.beam("reset\n", "fleet", "reset"); r.code != 0 {
			t.Fatalf("reset: %+v", r)
		}
	}
	owner = identity.NewAuthenticator()
	authenticate(owner)
	if r := b.beam("", "init", "--label", "beta"); r.code != 0 {
		t.Fatalf("init: %+v", r)
	}
	if r := c.beam("", "join", "--label", "gamma"); r.code != 0 {
		t.Fatalf("join: %+v", r)
	}
	connectedAll(t, b, c)
	release()
	for deadline := time.Now().Add(time.Second); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		if s := b.peers(t)[c.id()].State; s == "revoked" {
			t.Fatalf("the old fleet's revocation reached the new one: gamma is %s", s)
		}
	}
}

// A stream opened before a reset attaches to nothing after it.
func TestOpenEndsWithReset(t *testing.T) {
	a := initFleet(t, "alpha")
	b := join(t, "beta")
	connectedAll(t, a, b)
	c, err := control.Connect(b.paths(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	var res struct {
		StreamID string `json:"streamId"`
	}
	if err := c.Call("exec.open", map[string]any{"peer": "alpha", "argv": []string{"true"}}, &res); err != nil {
		t.Fatal(err)
	}
	if r := b.beam("reset\n", "fleet", "reset"); r.code != 0 {
		t.Fatalf("reset: %+v", r)
	}
	ac, err := control.Attach(b.paths(), res.StreamID)
	if err != nil {
		t.Fatal(err)
	}
	defer ac.Close()
	if msg := readClose(t, ac); msg.Reason != "params" {
		t.Errorf("close %+v, want params: no such stream", msg)
	}
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

// docs/02: a record this machine signs is queued with the state it commits,
// so a daemon that stops before the worker has it, or before it hears back,
// publishes it once it runs again, and leaves nothing queued.
func TestPublishOutlivesDaemon(t *testing.T) {
	// crash stops m's daemon when it reaches point for peer during beam args,
	// and starts it again.
	crash := func(t *testing.T, m *machine, point, peer string, args ...string) {
		t.Helper()
		reached, release := pauseAt(t, m, point, peer)
		go m.beam("", args...)
		await(t, reached, point)
		m.stop()
		release()
		m.start(t)
	}
	// keyed gives a blank machine its key, so its peer id is known.
	keyed := func(t *testing.T, label string) *machine {
		m := blank(t, label)
		k := transport.NewKey(relay.Region)
		writeJSON(t, filepath.Join(m.dir, "key.json"), k)
		m.entry.PeerID = identity.PeerID(k.NodePublic())
		m.start(t)
		return m
	}
	published := func(t *testing.T, m *machine, want int) {
		t.Helper()
		waitFor(t, 10*time.Second, "the record in the directory, and nothing queued", func() bool {
			var queued int
			sqlite(t, m, func(tx *sql.Tx) error { return tx.QueryRow(`SELECT count(*) FROM pending`).Scan(&queued) })
			return queued == 0 && worker.Len(owner.Credential().FleetID()) == want
		})
	}
	for _, point := range []string{"publishing", "published"} {
		t.Run(point+"/init", func(t *testing.T) {
			prev := owner
			owner = identity.NewAuthenticator()
			authenticate(owner)
			t.Cleanup(func() { owner = prev; authenticate(prev) })
			m := keyed(t, "alpha")
			crash(t, m, point, m.id(), "init", "--label", "alpha")
			published(t, m, 1)
		})
		t.Run(point+"/join", func(t *testing.T) {
			initFleet(t, "alpha")
			m := keyed(t, "beta")
			crash(t, m, point, m.id(), "join", "--label", "beta")
			published(t, m, 2)
		})
		t.Run(point+"/revoke", func(t *testing.T) {
			a := initFleet(t, "alpha")
			c := join(t, "gamma")
			connectedAll(t, a, c)
			crash(t, a, point, c.id(), "revoke", "gamma")
			published(t, a, 3)
		})
	}
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

// docs/02 Ceremonies: a ceremony nobody answers ends ceremony-timeout after
// its five minutes; the *.wait frees the daemon for the next before it
// answers, and one nobody waits on frees it by itself.
func TestCeremonyTimeout(t *testing.T) {
	defer ceremony.SetTimeout(time.Second)()
	os.Unsetenv("BEAM_TEST_AUTHENTICATOR")
	defer authenticate(owner)
	m := blank(t, "fresh")
	m.start(t)
	c, err := control.Connect(m.paths(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	start := map[string]any{"label": "fresh"}
	if err := c.Call("join.start", start, nil); err != nil {
		t.Fatal(err)
	}
	_, release := pauseAt(t, m, "flow-ending", "") // the timeout's own end of the flow
	time.Sleep(300 * time.Millisecond)             // the client prints the URL, then waits
	if err := c.Call("join.wait", nil, nil); code(err) != "ceremony-timeout" {
		t.Errorf("join.wait: %v, want ceremony-timeout", err)
	}
	if err := c.Call("join.start", start, nil); err != nil {
		t.Fatalf("a start right after the wait answered: %v", err)
	}
	release()
	time.Sleep(2 * time.Second)
	if err := c.Call("join.start", start, nil); err != nil {
		t.Errorf("a start after one nobody waited on timed out: %v", err)
	}
	c.Call("ceremony.cancel", nil, nil)
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

// code is an op's error code, or the error.
func code(err error) string {
	var oe *control.OpError
	if errors.As(err, &oe) {
		return oe.Code
	}
	return fmt.Sprint(err)
}
