//go:build beamtest

package cli_test

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/notaharness/beam/internal/control"
	"github.com/notaharness/beam/internal/identity"
	"github.com/notaharness/beam/internal/stream"
	"github.com/notaharness/beam/internal/transport"
	"tailscale.com/types/logger"
)

// push forwards signed records to m over sync, from a member of its own
// (a forwarder's word counts for nothing; each record carries the passkey's).
func push(t *testing.T, m *machine, recs ...identity.Record) {
	t.Helper()
	tun, _ := rawTunnel(t, m)
	sc := rawOpen(t, tun, stream.Header{V: 1, Kind: stream.KindSync})
	defer sc.Close()
	var f struct {
		Records []identity.Record `json:"records"`
	}
	f.Records = recs
	if err := sc.WriteJSON(stream.Data, f); err != nil {
		t.Fatal(err)
	}
	// A ping answered means everything before it was applied.
	sc.WriteJSON(stream.Control, stream.Ctl{Kind: "ping", T: 1})
	if _, _, err := sc.ReadFrame(); err != nil {
		t.Fatal(err)
	}
}

// rawTunnel dials m from a member that runs no daemon, only a node that
// admits no one, and returns the tunnel and that member.
func rawTunnel(t *testing.T, m *machine) (*transport.Tunnel, *machine) {
	t.Helper()
	raw := newMachine(t, "raw")
	entry, _ := json.Marshal(raw.entry)
	n, err := transport.Start(transport.Config{Key: raw.key, Entry: entry,
		Admit:  func(json.RawMessage) (string, [32]byte, string) { return "", [32]byte{}, "bad-entry" },
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

// docs/10 "grants": msg refuses pty and exec on a live tunnel and ends the
// shells already open; none still syncs.
func TestGrants(t *testing.T) {
	ms := fleet(t, "alpha", "beta")
	a, b := ms[0], ms[1]
	waitState(t, a, b, "connected")
	ac, _ := openPTY(t, a, "beta", "sh", "-c", "echo up; exec sleep 300")
	readUntil(t, ac, "up")

	if r := b.beam("", "peer", "grant", "alpha", "msg"); r.code != 0 {
		t.Fatalf("grant: %+v", r)
	}
	ac.SetReadDeadline(time.Now().Add(10 * time.Second))
	for {
		typ, _, err := ac.ReadFrame()
		if err != nil {
			t.Fatalf("the open shell was not ended: %v", err)
		}
		if typ == stream.Close {
			break
		}
	}
	if r := a.beam("", "exec", "beta", "--", "true"); r.code != 1 || !strings.HasPrefix(r.err, "grant") {
		t.Errorf("exec under msg: %+v, want grant", r)
	}
	if r := b.beam("", "peer", "grant", "alpha", "none"); r.code != 0 {
		t.Fatalf("grant: %+v", r)
	}
	time.Sleep(2 * pingEvery())
	if v := a.peers(t)[b.id()]; v.State != "connected" {
		t.Errorf("under none: %s, want still connected", v.State)
	}
	if v := b.peers(t)[a.id()]; !v.Inbound || v.Grant != "none" {
		t.Errorf("beta's view under none: %+v", v)
	}
	if r := b.beam("", "peer", "grant", "alpha", "root"); r.code != 1 || !strings.HasPrefix(r.err, "params") {
		t.Errorf("bad grant: %+v, want params", r)
	}
}

// pingEvery is a little over the time for one exchange on sync.
func pingEvery() time.Duration { return time.Second }

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
	if r := c.beam("", "exec", "beta", "--", "true"); r.code != 1 {
		t.Errorf("revoked machine's exec: %+v, want refused", r)
	}
	if r := a.beam("", "exec", "gamma", "--", "true"); r.code != 1 || !strings.HasPrefix(r.err, "revoked-peer") {
		t.Errorf("exec to a revoked machine: %+v, want revoked-peer", r)
	}
}

// docs/02 Sync: a record learned while a dump is on its way reaches the peer
// as a delta; none falls between the dump's snapshot and the deltas.
func TestRecordLearnedDuringDump(t *testing.T) {
	a, b, c := newMachine(t, "alpha"), newMachine(t, "beta"), newMachine(t, "gamma")
	a.knows(t, b, c)
	b.knows(t, a, c)
	reached, release := pauseAt(t, a, "dumped", b)
	a.start(t)
	b.start(t)
	await(t, reached, "alpha's dump to beta")
	push(t, a, revocation(c))
	release()
	waitState(t, b, c, "revoked")
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

// docs/07: beam daemon --detach returns at once and leaves a daemon running,
// logging to daemon.log.
func TestDaemonDetach(t *testing.T) {
	m := newMachine(t, "alpha")
	if r := m.beam("", "daemon", "--detach"); r.code != 0 {
		t.Fatalf("detach: %+v", r)
	}
	waitFor(t, 5*time.Second, "the daemon's socket", func() bool {
		c, err := net.Dial("unix", m.paths().Socket) // not Connect, which would spawn one
		if err == nil {
			c.Close()
		}
		return err == nil
	})
	c, err := control.Connect(m.paths(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := c.Call("status", nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := c.Call("daemon.shutdown", nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(m.dir, "daemon.log")); err != nil {
		t.Errorf("no daemon.log: %v", err)
	}
}
