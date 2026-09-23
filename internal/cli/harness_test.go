//go:build beamtest

package cli_test

import (
	"bytes"
	"cmp"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/notaharness/beam/internal/cli"
	"github.com/notaharness/beam/internal/control"
	"github.com/notaharness/beam/internal/devderp"
	"github.com/notaharness/beam/internal/fakeworker"
	"github.com/notaharness/beam/internal/identity"
	"github.com/notaharness/beam/internal/store"
	"github.com/notaharness/beam/internal/transport"
	"tailscale.com/types/logger"
)

var (
	relay  *devderp.Relay
	owner  *identity.Authenticator // the fleet's passkey
	worker *fakeworker.Worker      // the directory
	dirURL string                  // where it listens
)

func TestMain(m *testing.M) {
	// Connect-or-spawn runs this binary as `beam daemon --detach`.
	if len(os.Args) > 1 && os.Args[1] == "daemon" {
		os.Exit(cli.Main(os.Args[1:], os.Environ(), os.Stdin, os.Stdout, os.Stderr))
	}
	devderp.Isolate()
	devderp.ForceRelay()
	var err error
	if relay, err = devderp.Start(logger.Discard); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	owner = identity.NewAuthenticator()
	authenticate(owner)
	worker = fakeworker.New()
	srv := httptest.NewServer(worker)
	dirURL = srv.URL
	code := m.Run()
	srv.Close()
	relay.Close()
	os.Exit(code)
}

// authenticate makes a the passkey every ceremony in this process uses
// (docs/10, the test authenticator).
func authenticate(a *identity.Authenticator) {
	j, _ := json.Marshal(a)
	os.Setenv("BEAM_TEST_AUTHENTICATOR", string(j))
}

// blank is a $BEAM_DIR with nothing in it yet: a machine for init or join.
func blank(t *testing.T, label string) *machine {
	t.Helper()
	return &machine{dir: beamDir(t), entry: identity.Record{Label: label}}
}

// enrolled reads m's fleet.json once a ceremony has written it.
func (m *machine) enrolled(t *testing.T) *machine {
	t.Helper()
	var f identity.Fleet
	b, err := os.ReadFile(filepath.Join(m.dir, "fleet.json"))
	if err != nil || json.Unmarshal(b, &f) != nil {
		t.Fatalf("fleet.json: %v", err)
	}
	m.entry = f.Entry
	return m
}

// machine is one enrolled $BEAM_DIR and, once started, its daemon.
type machine struct {
	dir     string
	key     *transport.Key
	entry   identity.Record
	derpMap string // the daemon's DERP map; the dev relay's when empty
	dirURL  string // the daemon's directory; the fake worker's when empty
	stop    func()
}

func (m *machine) id() string { return m.entry.PeerID }

// beamDir is an empty $BEAM_DIR for the test. It is not t.TempDir(), whose
// path on macOS leaves the daemon's socket path over the 104-byte limit.
func beamDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "beam")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

// newMachine enrols a $BEAM_DIR as the ceremonies would, without them: a node
// key on the dev relay, an entry signed by the owner's passkey, fleet.json.
func newMachine(t *testing.T, label string) *machine {
	t.Helper()
	dir := beamDir(t)
	k := transport.NewKey(relay.Region)
	pub := k.NodePublic()
	e := identity.Record{V: 1, Kind: identity.Member, PeerID: identity.PeerID(pub),
		NodePublic: base64.RawURLEncoding.EncodeToString(pub[:]), Address: k.Address(),
		Label: label, IssuedAt: time.Now().UnixMilli()}
	owner.SignRecord(&e)
	m := &machine{dir: dir, key: k, entry: e}
	m.writeFleet(t)
	writeJSON(t, filepath.Join(dir, "key.json"), k)
	return m
}

func (m *machine) writeFleet(t *testing.T) {
	cred := owner.Credential()
	kDir, tRead := identity.DirectoryKeys(owner.PRF([]byte(identity.PRFSalt)))
	enc := base64.RawURLEncoding.EncodeToString
	writeJSON(t, filepath.Join(m.dir, "fleet.json"), identity.Fleet{V: 1, FleetID: cred.FleetID(),
		CredentialID: cred.ID, CredentialPublicKey: enc(cred.PublicKey), KDir: enc(kDir), TRead: enc(tRead), Entry: m.entry})
}

func writeJSON(t *testing.T, path string, v any) {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
}

// knows pins other machines' entries in m's state.db, as a join would.
func (m *machine) knows(t *testing.T, others ...*machine) {
	t.Helper()
	st, err := store.Open(filepath.Join(m.dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	for _, o := range others {
		if _, err := st.Pin(o.entry, time.Now().UnixMilli()); err != nil {
			t.Fatal(err)
		}
	}
}

func (m *machine) paths() control.Paths {
	return control.Paths{Dir: m.dir, Socket: filepath.Join(m.dir, "run", "beam.sock")}
}

// start runs m's daemon in this process until the test ends.
func (m *machine) start(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	derpMap := cmp.Or(m.derpMap, relay.MapURL)
	go func() {
		done <- control.Run(ctx, control.Options{Paths: m.paths(), Version: "test", Logf: logger.Discard,
			DERPMap: derpMap, Directory: cmp.Or(m.dirURL, dirURL)})
	}()
	var once sync.Once
	m.stop = func() { once.Do(func() { cancel(); <-done }) }
	t.Cleanup(func() { m.stop() })
	waitFor(t, 5*time.Second, "socket", func() bool {
		c, err := net.Dial("unix", m.paths().Socket)
		if err == nil {
			c.Close()
		}
		return err == nil
	})
}

// fleet makes machines that all know each other and starts them.
func fleet(t *testing.T, labels ...string) []*machine {
	t.Helper()
	var ms []*machine
	for _, l := range labels {
		ms = append(ms, newMachine(t, l))
	}
	for _, m := range ms {
		for _, o := range ms {
			if o != m {
				m.knows(t, o)
			}
		}
	}
	for _, m := range ms {
		m.start(t)
	}
	return ms
}

// result is one CLI invocation's outcome.
type result struct {
	out, err string
	code     int
}

// beam runs the CLI against m's daemon.
func (m *machine) beam(stdin string, args ...string) result {
	var out, errb bytes.Buffer
	code := m.run(strings.NewReader(stdin), &out, &errb, args...)
	return result{out.String(), errb.String(), code}
}

// run is beam on m with the given streams.
func (m *machine) run(stdin io.Reader, stdout, stderr io.Writer, args ...string) int {
	vars := []string{"BEAM_CONFIG_DIR=" + m.dir, "HOME=" + os.Getenv("HOME"), "PATH=" + os.Getenv("PATH")}
	return cli.Main(args, vars, stdin, stdout, stderr)
}

func (m *machine) peers(t *testing.T) map[string]control.PeerView {
	t.Helper()
	r := m.beam("", "peers", "--json")
	var res struct{ Peers []control.PeerView }
	if r.code != 0 || json.Unmarshal([]byte(r.out), &res) != nil {
		t.Fatalf("beam peers: %+v", r)
	}
	views := map[string]control.PeerView{}
	for _, v := range res.Peers {
		views[v.PeerID] = v
	}
	return views
}

// waitState waits until m sees peer in state.
func waitState(t *testing.T, m, peer *machine, state string) {
	t.Helper()
	waitFor(t, 30*time.Second, peer.entry.Label+" "+state, func() bool {
		return m.peers(t)[peer.id()].State == state
	})
}

func waitFor(t *testing.T, limit time.Duration, what string, ok func() bool) {
	t.Helper()
	for deadline := time.Now().Add(limit); !ok(); time.Sleep(50 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("timed out after %v waiting for %s", limit, what)
		}
	}
}

// pauseAt holds m's daemon the first time it reaches point for peer, until the
// test calls release (or ends). reached is closed when it gets there.
func pauseAt(t *testing.T, m *machine, point, peer string) (reached <-chan struct{}, release func()) {
	t.Helper()
	arrived, released := make(chan struct{}), make(chan struct{})
	var first, done sync.Once
	release = func() { done.Do(func() { close(released) }) }
	control.SetHook(func(self, p, pr string) {
		if self == m.id() && p == point && pr == peer {
			first.Do(func() { close(arrived); <-released })
		}
	})
	t.Cleanup(func() { release(); control.SetHook(nil) })
	return arrived, release
}

// await waits for ch to close.
func await(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(30 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
	}
}
