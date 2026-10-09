//go:build beamtest

package cli_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/notaharness/beam/internal/cli"
	"github.com/notaharness/beam/internal/control"
	"github.com/notaharness/beam/internal/identity"
	"github.com/notaharness/beam/internal/stream"
	"github.com/notaharness/beam/internal/transport"
	"tailscale.com/types/logger"
)

// push forwards signed records to m over sync, from a member of its own
// (a forwarder's word counts for nothing; each record carries the passkey's),
// and returns once m has applied them.
func push(t *testing.T, m *machine, recs ...identity.Record) {
	t.Helper()
	sc := forward(t, m, recs...)
	defer sc.Close()
	// A ping answered means everything before it was applied.
	sc.WriteJSON(stream.Control, stream.Ctl{Kind: "ping", T: 1})
	if _, _, err := sc.ReadFrame(); err != nil {
		t.Fatal(err)
	}
}

// forward sends signed records to m over sync as push does, without waiting.
func forward(t *testing.T, m *machine, recs ...identity.Record) *stream.Conn {
	t.Helper()
	tun, _ := rawTunnel(t, m)
	sc := rawOpen(t, tun, stream.Header{V: 1, Kind: stream.KindSync})
	var f struct {
		Records []identity.Record `json:"records"`
	}
	f.Records = recs
	if err := sc.WriteJSON(stream.Data, f); err != nil {
		t.Fatal(err)
	}
	return sc
}

// rawTunnel dials m from a member that runs no daemon, only a node that
// admits no one, and returns the tunnel and that member.
func rawTunnel(t *testing.T, m *machine) (*transport.Tunnel, *machine) {
	t.Helper()
	raw := newMachine(t, "raw")
	entry, _ := json.Marshal(raw.entry)
	n, err := transport.Start(transport.Config{Key: raw.key, Entry: entry,
		Admit:  func(json.RawMessage) (string, [32]byte, func(), string) { return "", [32]byte{}, nil, "bad-entry" },
		Handle: func(_ string, _ stream.Header, c *stream.Conn) { c.Close() }})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { n.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	tun, err := n.Dial(ctx, m.entry.Address)
	if err != nil {
		t.Fatal(err)
	}
	return tun, raw
}

func rawOpen(t *testing.T, tun *transport.Tunnel, h stream.Header) *stream.Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	sc, err := tun.Open(ctx, h)
	if err != nil {
		t.Fatal(err)
	}
	return sc
}

func revocation(m *machine) identity.Record {
	r := identity.Record{V: 1, Kind: identity.Revoke, PeerID: m.id(), IssuedAt: time.Now().UnixMilli()}
	owner.SignRecord(&r)
	return r
}

// docs/10 "third joins while second is offline; second returns": here the
// second learns the third only through the first's sync, dials it, and the
// third admits it on first contact. One row each.
func TestSyncLearnsMember(t *testing.T) {
	a, b, c := newMachine(t, "alpha"), newMachine(t, "beta"), newMachine(t, "gamma")
	a.knows(t, b, c)
	b.knows(t, a)
	c.knows(t, a)
	for _, m := range []*machine{a, b, c} {
		m.start(t)
	}
	waitState(t, b, c, "connected")
	waitState(t, c, b, "connected")
	for _, m := range []*machine{a, b, c} {
		if n := len(m.peers(t)); n != 2 {
			t.Errorf("%s has %d peers, want 2", m.entry.Label, n)
		}
	}
}

// docs/10 "supersession": a re-join relabels; the older entry is ignored.
func TestSupersession(t *testing.T) {
	ms := fleet(t, "alpha", "beta")
	a, b := ms[0], ms[1]
	waitState(t, b, a, "connected")
	old := a.entry
	a.stop()
	a.entry.Label, a.entry.IssuedAt = "alpha2", old.IssuedAt+1
	owner.SignRecord(&a.entry)
	a.writeFleet(t)
	a.start(t)
	waitFor(t, 30*time.Second, "the new label", func() bool { return b.peers(t)[a.id()].Label == "alpha2" })
	push(t, b, old)
	if l := b.peers(t)[a.id()].Label; l != "alpha2" {
		t.Errorf("after the older entry: %s, want alpha2", l)
	}
}

// docs/10 "revoke while all tunnels are up": peers refuse within the sync
// delta, no reconnect needed.
func TestRevocationOnLiveSync(t *testing.T) {
	ms := fleet(t, "alpha", "beta", "gamma")
	a, b, c := ms[0], ms[1], ms[2]
	for _, x := range ms {
		for _, y := range ms {
			if x != y {
				waitState(t, x, y, "connected")
			}
		}
	}
	push(t, a, revocation(c))
	waitState(t, a, c, "revoked")
	waitState(t, b, c, "revoked")
	if r := a.beam("", "exec", "gamma", "--", "true"); r.code != 1 || !strings.HasPrefix(r.err, "revoked-peer") {
		t.Errorf("exec to a revoked machine: %+v, want revoked-peer", r)
	}

	// The revoked machine hears it from each peer's hello refusal: it stops
	// dialing them and says so.
	waitState(t, c, a, "revoked-by-fleet")
	waitState(t, c, b, "revoked-by-fleet")
	if r := c.beam("", "status"); !strings.Contains(r.out, "this machine is revoked: 2 peers refuse it") {
		t.Errorf("the revoked machine's status: %+v", r)
	}
	start := time.Now()
	if r := c.beam("", "exec", "beta", "--", "true"); r.code != 1 || !strings.Contains(r.err, "revoked") || time.Since(start) > 5*time.Second {
		t.Errorf("revoked machine's exec: %+v after %v, want revoked at once", r, time.Since(start))
	}
	if r := c.beam("", "msg", "send", "beta", "x"); r.code != 1 || r.err != "rejected: revoked-peer\n" {
		t.Errorf("revoked machine's msg send: %+v, want rejected: revoked-peer", r)
	}

	// docs/03 admission step 3: the hello itself is refused, before any
	// stream, for the revoked machine's own key and entry.
	c.stop()
	entry, _ := json.Marshal(c.entry)
	n, err := transport.Start(transport.Config{Key: c.key, Entry: entry,
		Admit:  func(json.RawMessage) (string, [32]byte, func(), string) { return "", [32]byte{}, nil, "bad-entry" },
		Handle: func(_ string, _ stream.Header, c *stream.Conn) { c.Close() }})
	if err != nil {
		t.Fatal(err)
	}
	defer n.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, err = n.Dial(ctx, b.entry.Address)
	var ref *transport.Refused
	if !errors.As(err, &ref) || ref.Reason != "revoked" {
		t.Errorf("the revoked machine's hello: %v, want revoked", err)
	}
}

// docs/02 Sync: a record learned while a dump is on its way reaches the peer
// as a delta; none falls between the dump's snapshot and the deltas. More
// than the delta queue holds fails the tunnel, and the next dump has them.
func TestRecordLearnedDuringDump(t *testing.T) {
	for _, before := range []int{0, 300} {
		t.Run(fmt.Sprintf("%d records before", before), func(t *testing.T) {
			a, b, c := newMachine(t, "alpha"), newMachine(t, "beta"), newMachine(t, "gamma")
			a.knows(t, b, c)
			b.knows(t, a, c)
			reached, release := pauseAt(t, a, "dumped", b.id())
			a.start(t)
			b.start(t)
			await(t, reached, "alpha's dump to beta")
			var recs []identity.Record
			for i := range before {
				r := identity.Record{V: 1, Kind: identity.Revoke, PeerID: fmt.Sprintf("%032x", i+1), IssuedAt: time.Now().UnixMilli()}
				owner.SignRecord(&r)
				recs = append(recs, r)
			}
			for len(recs) > 0 {
				n := min(len(recs), 200) // a sync frame's records
				push(t, a, recs[:n]...)
				recs = recs[n:]
			}
			push(t, a, revocation(c))
			release()
			waitState(t, b, c, "revoked")
		})
	}
}

// docs/03: a hello that fails possession changes nothing. A signed entry
// forwarded by a node that does not hold its key is neither pinned, nor
// dialed, nor pushed on.
func TestPossessionBeforeSideEffects(t *testing.T) {
	b := fleet(t, "beta")[0]
	victim, thief := newMachine(t, "victim"), newMachine(t, "thief")
	entry, _ := json.Marshal(victim.entry)
	n, err := transport.Start(transport.Config{Key: thief.key, Entry: entry,
		Admit:  func(json.RawMessage) (string, [32]byte, func(), string) { return "", [32]byte{}, nil, "bad-entry" },
		Handle: func(_ string, _ stream.Header, c *stream.Conn) { c.Close() }})
	if err != nil {
		t.Fatal(err)
	}
	defer n.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, err = n.Dial(ctx, b.entry.Address)
	var ref *transport.Refused
	if !errors.As(err, &ref) || ref.Reason != "possession" {
		t.Fatalf("hello with another machine's entry: %v, want possession", err)
	}
	if _, ok := b.peers(t)[victim.id()]; ok {
		t.Error("the refused entry was pinned")
	}
}

// docs/10 "junk in the log": a record that does not verify is ignored.
func TestUnverifiedRecordIgnored(t *testing.T) {
	ms := fleet(t, "alpha", "beta")
	a, b := ms[0], ms[1]
	forged := revocation(b)
	forged.IssuedAt++ // the assertion no longer covers the statement
	push(t, a, forged)
	if v := a.peers(t)[b.id()]; v.State == "revoked" {
		t.Fatal("an unverified revocation was applied")
	}
}

// docs/10 "daemon lock": a second daemon on one $BEAM_DIR exits 1.
func TestDaemonLock(t *testing.T) {
	a := fleet(t, "alpha")[0]
	err := control.Run(context.Background(), control.Options{Paths: a.paths(), Logf: logger.Discard})
	if !errors.Is(err, control.ErrLocked) {
		t.Errorf("second Run: %v, want ErrLocked", err)
	}
	if r := a.beam("", "daemon"); r.code != 1 || !strings.Contains(r.err, "another daemon holds $BEAM_DIR") {
		t.Errorf("beam daemon: %+v", r)
	}
}

func TestUnenrolled(t *testing.T) {
	m := newMachine(t, "fresh")
	os.Remove(filepath.Join(m.dir, "fleet.json"))
	m.start(t)
	r := m.beam("", "status", "--json")
	var s struct{ Enrolled, Ready bool }
	if r.code != 0 || json.Unmarshal([]byte(r.out), &s) != nil || s.Enrolled || s.Ready {
		t.Errorf("status: %+v", r)
	}
	if r := m.beam("", "exec", "anyone", "--", "true"); r.code != 1 || !strings.HasPrefix(r.err, "not-enrolled") {
		t.Errorf("exec: %+v, want not-enrolled", r)
	}
}

// docs/06: the socket is up before the transport. While the daemon starts,
// status answers at once and not enrolled; every other op waits for the start
// rather than finding the daemon unenrolled.
func TestOpWhileStarting(t *testing.T) {
	a := fleet(t, "alpha", "beta")[0]
	a.stop()
	arrived, released := make(chan struct{}), make(chan struct{})
	var once, done sync.Once
	release := func() { done.Do(func() { close(released) }) }
	control.SetHook(func(_, p, _ string) {
		if p == "starting" {
			once.Do(func() { close(arrived); <-released })
		}
	})
	t.Cleanup(func() { control.SetHook(nil) })
	a.start(t)
	t.Cleanup(release) // before the daemon's stop, which waits for its start
	await(t, arrived, "the daemon to start")
	var s struct{ Enrolled, Ready bool }
	if r := a.beam("", "status", "--json"); r.code != 0 || json.Unmarshal([]byte(r.out), &s) != nil || s.Enrolled || s.Ready {
		t.Errorf("status while starting: %+v", r)
	}
	answered := make(chan result, 1)
	go func() { answered <- a.beam("", "peers") }()
	select {
	case r := <-answered:
		t.Fatalf("peers answered before the daemon started: %+v", r)
	case <-time.After(300 * time.Millisecond):
	}
	release()
	if r := <-answered; r.code != 0 || !strings.Contains(r.out, "beta") {
		t.Errorf("peers: %+v", r)
	}
}

// docs/10 "daemon lock": connect-or-spawn with no daemon starts one; racing
// spawns leave one daemon and every client connected to it.
func TestConnectOrSpawn(t *testing.T) {
	m := newMachine(t, "fresh")
	os.Remove(filepath.Join(m.dir, "fleet.json"))
	var wg sync.WaitGroup
	results := make([]result, 3)
	for i := range results {
		wg.Go(func() { results[i] = m.beam("", "status") })
	}
	wg.Wait()
	t.Cleanup(func() {
		if c, err := control.Connect(m.paths(), nil); err == nil {
			c.Call("daemon.shutdown", nil, nil)
			c.Close()
		}
	})
	for i, r := range results {
		if r.code != 0 || !strings.Contains(r.out, "not enrolled") {
			t.Errorf("client %d: %+v", i, r)
		}
	}
	if _, err := os.Stat(filepath.Join(m.dir, "daemon.log")); err != nil {
		t.Errorf("no daemon.log: %v", err)
	}
}

// docs/06 peer.resolve: id prefix, alias or label; ambiguity lists candidates.
func TestResolve(t *testing.T) {
	a, b, c, d := newMachine(t, "alpha"), newMachine(t, "beta"), newMachine(t, "twin"), newMachine(t, "twin")
	a.knows(t, b, c, d)
	a.start(t)
	for _, tc := range []struct {
		args []string
		code int
		err  string
	}{
		{[]string{"peer", "grant", "beta", "all"}, 0, ""},
		{[]string{"peer", "grant", b.id()[:8], "all"}, 0, ""},
		{[]string{"peer", "grant", b.id()[:7], "all"}, 1, "unknown-peer"},
		{[]string{"peer", "alias", "beta", "bee"}, 0, ""},
		{[]string{"peer", "grant", "bee", "all"}, 0, ""},
		{[]string{"peer", "alias", "bee", "a/b"}, 1, "params"},
		{[]string{"peer", "grant", "twin", "all"}, 1, "ambiguous-peer: "},
		{[]string{"peer", "grant", c.id(), "all"}, 0, ""},
		{[]string{"peer", "alias", "bee", "-"}, 0, ""},
		{[]string{"peer", "grant", "bee", "all"}, 1, "unknown-peer"},
	} {
		r := a.beam("", tc.args...)
		if r.code != tc.code || !strings.HasPrefix(r.err, tc.err) {
			t.Errorf("%v: %+v", tc.args, r)
		}
		if strings.HasPrefix(tc.err, "ambiguous") && (!strings.Contains(r.err, c.id()) || !strings.Contains(r.err, d.id())) {
			t.Errorf("candidates missing from %q", r.err)
		}
	}

	cc, err := control.Connect(a.paths(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cc.Close()
	for _, peer := range []string{"beta", b.id()[:8], b.id()} {
		var res struct {
			PeerID string `json:"peerId"`
		}
		if err := cc.Call("peer.resolve", map[string]any{"peer": peer}, &res); err != nil || res.PeerID != b.id() {
			t.Errorf("peer.resolve %s: %q, %v", peer, res.PeerID, err)
		}
	}
	var oe *control.OpError
	if err := cc.Call("peer.resolve", map[string]any{"peer": "twin"}, nil); !errors.As(err, &oe) || oe.Code != "ambiguous-peer" {
		t.Errorf("peer.resolve twin: %v, want ambiguous-peer", err)
	}
}

// docs/07: status and peers, for people.
func TestStatusAndPeers(t *testing.T) {
	ms := fleet(t, "alpha", "beta")
	a, b := ms[0], ms[1]
	waitState(t, a, b, "connected")
	fp := func(id string) string { return id[0:4] + " " + id[4:8] + " " + id[8:12] + " " + id[12:16] }

	r := a.beam("", "status")
	for _, want := range []string{"this machine: alpha (" + fp(a.id()) + ") · fleet ", " · relay dev\n",
		"peers: 1 connected, 0 offline, 0 revoked\n"} {
		if r.code != 0 || !strings.Contains(r.out, want) {
			t.Errorf("status: %+v, want %q", r, want)
		}
	}
	r = a.beam("", "peers")
	lines := strings.Split(strings.TrimSpace(r.out), "\n")
	if r.code != 0 || len(lines) != 2 || !strings.HasPrefix(lines[0], "NAME") {
		t.Fatalf("peers: %+v", r)
	}
	for _, want := range []string{"beta", fp(b.id()), "connected", "all", "0/0/0"} {
		if !strings.Contains(lines[1], want) {
			t.Errorf("peers row %q lacks %q", lines[1], want)
		}
	}
}

// docs/06: beam daemon --detach, however spelled, returns at once and leaves
// one daemon running with the options it was given, logging to daemon.log:
// it reads the fake worker's directory, not beam.n10.is.
func TestDaemonDetach(t *testing.T) {
	for _, detach := range []string{"--detach", "--detach=true"} {
		t.Run(detach, func(t *testing.T) {
			initFleet(t, "alpha")
			reads := worker.Reads(owner.Credential().FleetID())
			m := newMachine(t, "beta")
			if r := m.beam("", "daemon", detach, "--derp-map", relay.MapURL, "--directory", dirURL); r.code != 0 {
				t.Fatalf("detach: %+v", r)
			}
			t.Cleanup(func() {
				if c, err := control.Dial(m.paths()); err == nil {
					c.Call("daemon.shutdown", nil, nil)
					c.Close()
				}
			})
			waitFor(t, 5*time.Second, "the daemon's socket", func() bool { return answering(m) })
			waitFor(t, 10*time.Second, "the daemon to read the fake worker", func() bool {
				return worker.Reads(owner.Credential().FleetID()) > reads
			})
			var st struct {
				Enrolled bool `json:"enrolled"`
			}
			if r := m.beam("", "status", "--json"); r.code != 0 || json.Unmarshal([]byte(r.out), &st) != nil || !st.Enrolled {
				t.Fatalf("status: %+v", r)
			}
			if _, err := os.Stat(filepath.Join(m.dir, "daemon.log")); err != nil {
				t.Errorf("no daemon.log: %v", err)
			}
		})
	}
}

// docs/07: beam version prints the build's version and starts no daemon.
func TestVersion(t *testing.T) {
	m := blank(t, "fresh")
	if r := m.beam("", "version"); r.code != 0 || r.out != cli.Version+"\n" {
		t.Errorf("version: %+v", r)
	}
	if _, err := os.Stat(m.paths().Socket); !os.IsNotExist(err) {
		t.Errorf("the daemon's socket: %v", err)
	}
}
