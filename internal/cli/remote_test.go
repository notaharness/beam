//go:build beamtest && unix

package cli_test

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/notaharness/beam/internal/devderp"
	"github.com/notaharness/beam/internal/stream"
	"github.com/notaharness/beam/internal/transport"
	"tailscale.com/types/logger"
)

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
	if r := a.beam("", "msg", "send", "beta", "under msg"); r.out != "delivered to beta\n" {
		t.Errorf("mail under msg: %+v", r)
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
	if r := a.beam("", "msg", "send", "beta", "under none"); r.out != "stored for beta; delivery pending (beta's grant refuses mail from this machine). beam will deliver it once beta allows it. Do not send it again.\n" {
		t.Errorf("mail under none: %+v, want stored under the grant", r) // the open msg stream was closed too
	}
	if r := b.beam("", "peer", "grant", "alpha", "root"); r.code != 1 || !strings.HasPrefix(r.err, "params") {
		t.Errorf("bad grant: %+v, want params", r)
	}
}

// docs/10 "supersession": a re-join at a new address moves the peers' tunnels
// there even while the old address still answers; alias and grant stay.
func TestNewAddressReplacesTunnel(t *testing.T) {
	ms := fleet(t, "alpha", "beta")
	a, b := ms[0], ms[1]
	waitState(t, a, b, "connected")
	for _, args := range [][]string{{"peer", "alias", "beta", "bee"}, {"peer", "grant", "bee", "msg"}} {
		if r := a.beam("", args...); r.code != 0 {
			t.Fatalf("%v: %+v", args, r)
		}
	}
	relay2, err := devderp.Start(logger.Discard)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(relay2.Close)
	moved := &machine{dir: beamDir(t), key: b.key.OnRegion(relay2.Region), entry: b.entry}
	moved.entry.Address, moved.entry.IssuedAt = moved.key.Address(), b.entry.IssuedAt+1
	owner.SignRecord(&moved.entry)
	moved.writeFleet(t)
	writeJSON(t, filepath.Join(moved.dir, "key.json"), moved.key)
	moved.start(t) // beta re-joined through another relay; the old beta runs on
	push(t, a, moved.entry)
	waitFor(t, 30*time.Second, "alpha to reach beta's new address", func() bool {
		r := a.beam("", "exec", "bee", "--", "sh", "-c", "echo $BEAM_DIR")
		return r.code == 0 && strings.TrimSpace(r.out) == moved.dir
	})
	if v := a.peers(t)[b.id()]; v.Alias == nil || *v.Alias != "bee" || v.Grant != "msg" {
		t.Errorf("after the move: %+v, want alias bee and grant msg", v)
	}
}

// docs/02: a revocation applies before a pending open proceeds. An exec
// checked while a revocation is being applied, or that passed its checks just
// before one lands, never starts.
func TestRevocationBeatsPendingOpen(t *testing.T) {
	for _, tc := range []struct{ kind, point string }{
		{stream.KindExec, "revoking"}, {stream.KindExec, "admitted"}, {stream.KindPTY, "admitted"},
	} {
		point := tc.point
		t.Run(tc.kind+"/"+point, func(t *testing.T) {
			b := fleet(t, "beta")[0]
			tun, raw := rawTunnel(t, b)
			bin, started := watchExec(t)
			reached, release := pauseAt(t, b, point, raw.id())
			opened := make(chan error, 1)
			open := func() {
				go func() {
					ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
					defer cancel()
					c, err := tun.Open(ctx, stream.Header{V: 1, Kind: tc.kind, Argv: []string{bin}, Cols: 80, Rows: 24})
					if err == nil {
						c.Close()
					}
					opened <- err
				}()
			}
			if point == "revoking" { // committed, not yet applied
				defer forward(t, b, revocation(raw)).Close()
				await(t, reached, "the revocation")
				open()
				var ref *transport.Refused
				if err := <-opened; !errors.As(err, &ref) || ref.Reason != "revoked" {
					t.Errorf("open during the revocation: %v, want revoked", err)
				}
				release()
			} else { // checked, not yet started
				open()
				await(t, reached, "the open")
				push(t, b, revocation(raw))
				release()
				if err := <-opened; err == nil {
					t.Error("the open succeeded")
				}
			}
			time.Sleep(time.Second) // time enough to start it, had it been let
			if started() {
				t.Fatal("the exec started")
			}
		})
	}
}

// docs/04 msg: one msg stream per tunnel, so envelopes from one sender are
// stored in order. A second is refused limit while the first is open, and
// one opened after it closed is admitted; exec and pty streams count apart.
func TestMsgOneStreamPerTunnel(t *testing.T) {
	b := fleet(t, "beta")[0]
	tun, _ := rawTunnel(t, b)
	defer rawOpen(t, tun, stream.Header{V: 1, Kind: stream.KindExec, Argv: []string{"sleep", "30"}}).Close()
	h := stream.Header{V: 1, Kind: stream.KindMsg}
	first := rawOpen(t, tun, h)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var ref *transport.Refused
	if sc, err := tun.Open(ctx, h); !errors.As(err, &ref) || ref.Reason != "limit" {
		if sc != nil {
			sc.Close()
		}
		t.Fatalf("a second msg stream: %v, want limit", err)
	}
	first.Close()
	waitFor(t, 5*time.Second, "a msg stream after the first closed", func() bool {
		sc, err := tun.Open(ctx, h)
		if err == nil {
			sc.Close()
		}
		return err == nil
	})
}

// pingEvery is a little over the time for one exchange on sync.
func pingEvery() time.Duration { return time.Second }

// docs/07 Peer arguments: after a re-join into the same fleet the label names
// both identities until the old one is revoked, and then only the new one;
// the revoked one is still reached by its id.
func TestRejoinLabel(t *testing.T) {
	a := initFleet(t, "alpha")
	b := join(t, "beta")
	connectedAll(t, a, b)
	old := b.id()
	if r := b.beam("reset\n", "fleet", "reset"); r.code != 0 || !strings.Contains(r.out, "beam revoke "+old) {
		t.Fatalf("reset: %+v, want the old identity's revoke", r)
	}
	if r := b.beam("", "join", "--label", "beta"); r.code != 0 {
		t.Fatalf("join: %+v", r)
	}
	nb := b.status(t).PeerID
	waitFor(t, 30*time.Second, "alpha to connect to the new beta", func() bool { return a.peers(t)[nb].State == "connected" })
	if r := a.beam("", "exec", "beta", "--", "true"); r.code != 1 || !strings.HasPrefix(r.err, "ambiguous-peer") {
		t.Fatalf("exec beta, both identities current: %+v", r)
	}
	if r := a.beam("", "revoke", old); r.code != 0 {
		t.Fatalf("revoke the old identity: %+v", r)
	}
	if r := a.beam("", "exec", "beta", "--", "true"); r.code != 0 {
		t.Errorf("exec beta after revoking the old identity: %+v", r)
	}
	if r := a.beam("", "exec", old, "--", "true"); r.code != 1 || !strings.HasPrefix(r.err, "revoked-peer") {
		t.Errorf("exec the old identity: %+v, want revoked-peer", r)
	}
}

// docs/03: a dialer hears its hello passed only once its entry is pinned, so
// a first contact's opens find it at once.
func TestPinnedBeforeOK(t *testing.T) {
	b := fleet(t, "beta")[0]
	raw := newMachine(t, "raw")
	entry, _ := json.Marshal(raw.entry)
	n, err := transport.Start(transport.Config{Key: raw.key, Entry: entry,
		Admit:  func(json.RawMessage) (string, [32]byte, func(), string) { return "", [32]byte{}, nil, "bad-entry" },
		Handle: func(_ string, _ stream.Header, c *stream.Conn) { c.Close() }})
	if err != nil {
		t.Fatal(err)
	}
	defer n.Close()
	reached, release := pauseAt(t, b, "admitting", raw.id())
	dialed := make(chan *transport.Tunnel, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		tun, _ := n.Dial(ctx, b.entry.Address)
		dialed <- tun
	}()
	await(t, reached, "the hello")
	select {
	case <-dialed:
		t.Fatal("the dialer heard ok before its entry was pinned")
	case <-time.After(time.Second):
	}
	release()
	tun := <-dialed
	if tun == nil {
		t.Fatal("the hello failed")
	}
	rawOpen(t, tun, stream.Header{V: 1, Kind: stream.KindExec, Argv: []string{"true"}}).Close()
}
