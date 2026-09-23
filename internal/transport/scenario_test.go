package transport

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/notaharness/beam/internal/devderp"
	"github.com/notaharness/beam/internal/stream"
	"github.com/tailscale/tailcat"
	"tailscale.com/types/logger"
)

var relay *devderp.Relay

func TestMain(m *testing.M) {
	if footprintChild() {
		return
	}
	devderp.Isolate()
	devderp.ForceRelay()
	var err error
	if relay, err = devderp.Start(logger.Discard); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	code := m.Run()
	relay.Close()
	os.Exit(code)
}

// fakeEntry stands in for the signed membership entry until identity exists:
// it names the machine's node key, and admitFake believes it.
type fakeEntry struct {
	PeerID     string `json:"peerId"`
	NodePublic string `json:"nodePublic"`
}

func admitFake(entry json.RawMessage) (string, [32]byte, func(), string) {
	var e fakeEntry
	if err := json.Unmarshal(entry, &e); err != nil {
		return "", [32]byte{}, nil, "bad-entry"
	}
	raw, err := base64.RawURLEncoding.DecodeString(e.NodePublic)
	if err != nil || len(raw) != 32 {
		return "", [32]byte{}, nil, "bad-entry"
	}
	return e.PeerID, [32]byte(raw), func() {}, ""
}

func entryFor(k *Key) (string, json.RawMessage) {
	pub := k.NodePublic()
	sum := sha256.Sum256(pub[:])
	id := hex.EncodeToString(sum[:16])
	b, _ := json.Marshal(fakeEntry{id, base64.RawURLEncoding.EncodeToString(pub[:])})
	return id, b
}

// echoCaller answers any stream with the peer id admission bound to it,
// followed by the first frame it received.
func echoCaller(peerID string, _ stream.Header, c *stream.Conn) {
	defer c.Close()
	if c.WriteLine(stream.Response{OK: true}) != nil {
		return
	}
	if _, p, err := c.ReadFrame(); err == nil {
		c.WriteFrame(stream.Data, []byte(peerID+" "+string(p)))
	}
}

type machine struct {
	key    *Key
	node   *Node
	peerID string
	entry  json.RawMessage
}

func startMachine(t testing.TB) *machine {
	t.Helper()
	return startMachineWith(t, admitFake)
}

func startMachineWith(t testing.TB, admit AdmitFunc) *machine {
	t.Helper()
	k := NewKey(relay.Region)
	id, entry := entryFor(k)
	n, err := Start(Config{Key: k, Entry: entry, Admit: admit, Handle: echoCaller})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { n.Close() })
	if got := string(n.srv.TailcatAddr()); got != k.Address() {
		t.Fatalf("server address %s, key address %s", got, k.Address())
	}
	return &machine{k, n, id, entry}
}

// roundTrip opens a stream on tun and checks that the far side attributes it
// to caller.
func roundTrip(ctx context.Context, tun *Tunnel, caller string) error {
	c, err := tun.Open(ctx, stream.Header{V: 1, Kind: "sync"})
	if err != nil {
		return err
	}
	defer c.Close()
	if err := c.WriteFrame(stream.Data, []byte("ping")); err != nil {
		return err
	}
	_, p, err := c.ReadFrame()
	if err != nil {
		return err
	}
	if want := caller + " ping"; string(p) != want {
		return fmt.Errorf("echo %q, want %q", p, want)
	}
	return nil
}

// The milestone gate (docs/11): one Server per machine with its node key, one
// Client per peer with an ephemeral key, all through one relay with UDP
// blocked. Every tunnel is dialed before any is used, so a DERP collision
// between a machine's stacks would lose traffic in the second phase.
func TestTopology(t *testing.T) {
	ms := []*machine{startMachine(t), startMachine(t), startMachine(t)}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	type pair struct{ from, to *machine }
	tunnels := map[pair]*Tunnel{}
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, a := range ms {
		for _, b := range ms {
			if a == b {
				continue
			}
			wg.Go(func() {
				tun, err := a.node.Dial(ctx, b.key.Address())
				if err != nil {
					t.Errorf("%s dial %s: %v", a.peerID[:4], b.peerID[:4], err)
					return
				}
				mu.Lock()
				tunnels[pair{a, b}] = tun
				mu.Unlock()
			})
		}
	}
	wg.Wait()
	if t.Failed() {
		return
	}
	for p, tun := range tunnels {
		wg.Go(func() {
			if err := roundTrip(ctx, tun, p.from.peerID); err != nil {
				t.Errorf("%s → %s: %v", p.from.peerID[:4], p.to.peerID[:4], err)
			}
		})
	}
	wg.Wait()
}

// rawClient is an adversary holding an address: its own tailcat stack, no beam.
func rawClient(t *testing.T, address string) *tailcat.Client {
	t.Helper()
	c := &tailcat.Client{Server: tailcat.Addr(address), Logf: logger.Discard}
	t.Cleanup(func() { c.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if _, err := c.Ping(ctx); err != nil {
		t.Fatal(err)
	}
	return c
}

func rawStream(t *testing.T, c *tailcat.Client) *stream.Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	conn, err := c.DialTCPPort(ctx, Port)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	sc := stream.NewConn(conn)
	sc.SetDeadline(time.Now().Add(15 * time.Second))
	return sc
}

// sendHello runs the dialer's half of hello by hand and returns the result.
func sendHello(t *testing.T, c *tailcat.Client, f helloFrame) stream.Response {
	t.Helper()
	sc := rawStream(t, c)
	var r stream.Response
	if err := sc.WriteLine(stream.Header{V: 1, Kind: "hello"}); err != nil {
		t.Fatal(err)
	}
	if err := sc.ReadLine(&r); err != nil || !r.OK {
		t.Fatalf("hello header: %+v, %v", r, err)
	}
	b, _ := json.Marshal(f)
	if err := sc.WriteFrame(stream.Data, b); err != nil {
		t.Fatal(err)
	}
	_, p, err := sc.ReadFrame()
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(p, &r); err != nil {
		t.Fatal(err)
	}
	return r
}

func TestPossession(t *testing.T) {
	b := startMachine(t)
	a := NewKey(relay.Region) // a member's real key, held by whoever runs these clients
	_, entry := entryFor(a)
	c1, c2, c3 := rawClient(t, b.key.Address()), rawClient(t, b.key.Address()), rawClient(t, b.key.Address())

	bad := newHello(a, raw(c1.PublicKey()), b.key.NodePublic(), entry)
	bad.MAC[0] ^= 1
	if r := sendHello(t, c1, bad); r.OK || r.Reason != "possession" {
		t.Errorf("wrong MAC: %+v, want refused possession", r)
	}

	good := newHello(a, raw(c3.PublicKey()), b.key.NodePublic(), entry)
	if r := sendHello(t, c2, good); r.OK || r.Reason != "possession" {
		t.Errorf("hello replayed from another tunnel: %+v, want refused possession", r)
	}
	if r := sendHello(t, c3, good); !r.OK {
		t.Errorf("the same hello on its own tunnel: %+v, want ok", r)
	}
}

func TestBadEntry(t *testing.T) {
	b := startMachine(t)
	c := rawClient(t, b.key.Address())
	if r := sendHello(t, c, helloFrame{Entry: json.RawMessage(`"junk"`)}); r.OK || r.Reason != "bad-entry" {
		t.Errorf("junk entry: %+v, want refused bad-entry", r)
	}

	c = rawClient(t, b.key.Address())
	sc := rawStream(t, c)
	sc.WriteLine(stream.Header{V: 1, Kind: "hello"})
	var r stream.Response
	if err := sc.ReadLine(&r); err != nil || !r.OK {
		t.Fatalf("hello header: %+v, %v", r, err)
	}
	a := NewKey(relay.Region)
	_, entry := entryFor(a)
	f, _ := json.Marshal(newHello(a, raw(c.PublicKey()), b.key.NodePublic(), entry))
	sc.WriteFrame(stream.Control, f)
	if _, p, err := sc.ReadFrame(); err != nil || json.Unmarshal(p, &r) != nil || r.OK || r.Reason != "bad-entry" {
		t.Errorf("a valid hello in a control frame: %q, %v; want refused bad-entry", p, err)
	}
}

// expectClosedUnread checks that the far side closed the stream: a clean EOF,
// or a reset when it closed with our bytes unread.
// expectOpen checks that sc is neither closed nor sent anything.
func expectOpen(t *testing.T, sc *stream.Conn) {
	t.Helper()
	sc.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
	var ne net.Error
	if _, err := sc.Read(make([]byte, 1)); !errors.As(err, &ne) || !ne.Timeout() {
		t.Errorf("read: %v, want the stream still open", err)
	}
}

// expectRefused reads a refusal for reason, then the stream's close.
func expectRefused(t *testing.T, sc *stream.Conn, reason string) {
	t.Helper()
	var r stream.Response
	if err := sc.ReadLine(&r); err != nil || r.OK || r.Reason != reason {
		t.Fatalf("got %+v, %v; want refused %s", r, err, reason)
	}
	expectClosedUnread(t, sc)
}

func expectClosedUnread(t *testing.T, sc *stream.Conn) {
	t.Helper()
	var ne net.Error
	_, err := sc.Read(make([]byte, 1))
	if err == nil || errors.As(err, &ne) && ne.Timeout() {
		t.Errorf("read: %v, want the stream closed", err)
	}
}

func TestUnboundStreams(t *testing.T) {
	b := startMachine(t)

	t.Run("first stream is not hello", func(t *testing.T) {
		c := rawClient(t, b.key.Address())
		sc := rawStream(t, c)
		sc.WriteLine(stream.Header{V: 1, Kind: "sync"})
		expectRefused(t, sc, "unauthenticated")

		a := NewKey(relay.Region) // the tunnel is still unbound, so hello may follow
		_, entry := entryFor(a)
		if r := sendHello(t, c, newHello(a, raw(c.PublicKey()), b.key.NodePublic(), entry)); !r.OK {
			t.Fatalf("hello after the refusal: %+v", r)
		}
	})

	t.Run("wrong version", func(t *testing.T) {
		sc := rawStream(t, rawClient(t, b.key.Address()))
		sc.WriteLine(stream.Header{V: 2, Kind: "hello"})
		var r stream.Response
		if err := sc.ReadLine(&r); err != nil || r.OK || r.Reason != "version" {
			t.Fatalf("got %+v, %v; want refused version", r, err)
		}
	})

	t.Run("after a failed hello", func(t *testing.T) {
		c := rawClient(t, b.key.Address())
		sendHello(t, c, helloFrame{Entry: json.RawMessage(`{}`)})
		sc := rawStream(t, c)
		sc.WriteLine(stream.Header{V: 1, Kind: "hello"})
		expectClosedUnread(t, sc)
	})
}

// The admission budgets as docs/03 states them, independent of the constants
// that implement them.
const (
	specHelloTimeout = 5 * time.Second
	specMaxUnbound   = 16
)

// A leaked address: the handshake completes but hello never comes.
func TestHelloDeadline(t *testing.T) {
	b := startMachine(t)
	c := rawClient(t, b.key.Address())
	sc := rawStream(t, c)
	start := time.Now()
	sc.WriteLine(stream.Header{V: 1, Kind: "hello"})
	var r stream.Response
	if err := sc.ReadLine(&r); err != nil || !r.OK {
		t.Fatalf("hello header: %+v, %v", r, err)
	}
	expectClosedUnread(t, sc) // the frame never comes
	if d := time.Since(start); d < specHelloTimeout-500*time.Millisecond || d > specHelloTimeout+time.Second {
		t.Errorf("closed after %v, want 5 s", d)
	}

	a := NewKey(relay.Region) // no verdict, so the tunnel is unbound again
	_, entry := entryFor(a)
	if r := sendHello(t, c, newHello(a, raw(c.PublicKey()), b.key.NodePublic(), entry)); !r.OK {
		t.Fatalf("hello after the deadline: %+v", r)
	}
}

func TestHelloBudget(t *testing.T) {
	b := startMachine(t)
	clients := make([]*tailcat.Client, specMaxUnbound+1)
	var wg sync.WaitGroup
	for i := range clients {
		wg.Go(func() { clients[i] = rawClient(t, b.key.Address()) })
	}
	wg.Wait()
	if t.Failed() {
		return
	}
	// Each open is acknowledged before the next, so the order is known.
	open := make([]*stream.Conn, len(clients))
	for i, c := range clients {
		open[i] = rawStream(t, c)
		open[i].WriteLine(stream.Header{V: 1, Kind: "hello"})
		var r stream.Response
		if err := open[i].ReadLine(&r); err != nil || !r.OK {
			t.Fatalf("client %d: %+v, %v", i, r, err)
		}
	}
	open[0].SetReadDeadline(time.Now().Add(2 * time.Second))
	expectClosedUnread(t, open[0])
	for _, sc := range open[1:] {
		expectOpen(t, sc)
	}
}

// blockingAdmit holds every hello in admission until the test ends, telling
// entered when one arrives.
func blockingAdmit(t *testing.T) (AdmitFunc, chan struct{}) {
	entered, release := make(chan struct{}, 8), make(chan struct{})
	t.Cleanup(func() { close(release) })
	return func(e json.RawMessage) (string, [32]byte, func(), string) {
		entered <- struct{}{}
		<-release
		return admitFake(e)
	}, entered
}

// dialWithin runs Dial and fails the test if it is still blocked after limit.
func dialWithin(t *testing.T, ctx context.Context, n *Node, address string, limit time.Duration) (*Tunnel, error) {
	t.Helper()
	type result struct {
		tun *Tunnel
		err error
	}
	done := make(chan result, 1)
	go func() {
		tun, err := n.Dial(ctx, address)
		done <- result{tun, err}
	}()
	select {
	case r := <-done:
		return r.tun, r.err
	case <-time.After(limit):
		t.Fatalf("Dial still blocked after %v", limit)
		return nil, nil
	}
}

// A far side that accepts the hello header and never answers must not hold
// Dial past its deadline or its caller's cancellation.
func TestHelloWaitIsBounded(t *testing.T) {
	a := startMachine(t)

	t.Run("deadline", func(t *testing.T) {
		admit, entered := blockingAdmit(t)
		b := startMachineWith(t, admit)
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		start := time.Now()
		_, err := dialWithin(t, ctx, a.node, b.key.Address(), 15*time.Second)
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("Dial: %v, want the deadline", err)
		}
		select {
		case <-entered:
		default:
			t.Fatal("Dial failed before hello reached admission; the scenario did not run")
		}
		if d := time.Since(start); d > 10*time.Second {
			t.Errorf("Dial returned after %v, past its 8 s deadline", d)
		}
	})

	t.Run("cancellation", func(t *testing.T) {
		admit, entered := blockingAdmit(t)
		b := startMachineWith(t, admit)
		ctx, cancel := context.WithCancel(context.Background())
		go func() { <-entered; cancel() }()
		_, err := dialWithin(t, ctx, a.node, b.key.Address(), 30*time.Second)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Dial: %v, want the cancellation", err)
		}
	})
}

func TestDialAfterClose(t *testing.T) {
	b := startMachine(t)

	t.Run("after", func(t *testing.T) {
		a := startMachine(t)
		a.node.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if tun, err := a.node.Dial(ctx, b.key.Address()); err == nil {
			tun.Close()
			t.Fatal("Dial after Close returned a live tunnel")
		}
	})

	t.Run("during", func(t *testing.T) {
		admit, entered := blockingAdmit(t)
		slow := startMachineWith(t, admit)
		a := startMachine(t)
		go func() { <-entered; a.node.Close() }()
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if tun, err := dialWithin(t, ctx, a.node, slow.key.Address(), 15*time.Second); err == nil {
			tun.Close()
			t.Fatal("a Dial in flight when the node closed returned a live tunnel")
		}
	})
}

// Close between a successful hello and registration: Dial reports ErrClosed
// and the node holds no tunnel.
func TestCloseAtRegistration(t *testing.T) {
	b := startMachine(t)
	a := startMachine(t)
	a.node.beforeRegister = func() { a.node.Close() }
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	tun, err := a.node.Dial(ctx, b.key.Address())
	if !errors.Is(err, ErrClosed) || tun != nil {
		t.Fatalf("Dial: %v, %v; want ErrClosed and no tunnel", tun, err)
	}
	a.node.mu.Lock()
	defer a.node.mu.Unlock()
	if len(a.node.tunnels) != 0 {
		t.Fatalf("%d tunnels registered after Close", len(a.node.tunnels))
	}
}

// openEcho opens a stream on a raw client and returns it once accepted.
func openEcho(t *testing.T, c *tailcat.Client) *stream.Conn {
	t.Helper()
	sc := rawStream(t, c)
	sc.WriteLine(stream.Header{V: 1, Kind: "sync"})
	var r stream.Response
	if err := sc.ReadLine(&r); err != nil || !r.OK {
		t.Fatalf("open: %+v, %v", r, err)
	}
	return sc
}

// A second hello for the same member retires the first tunnel: its open
// streams close, and neither its saved hello nor any new stream is admitted.
func TestSupersededTunnel(t *testing.T) {
	b := startMachine(t)
	a := NewKey(relay.Region)
	id, entry := entryFor(a)
	c1, c2 := rawClient(t, b.key.Address()), rawClient(t, b.key.Address())

	h1 := newHello(a, raw(c1.PublicKey()), b.key.NodePublic(), entry)
	if r := sendHello(t, c1, h1); !r.OK {
		t.Fatalf("first hello: %+v", r)
	}
	old := openEcho(t, c1)
	if r := sendHello(t, c2, newHello(a, raw(c2.PublicKey()), b.key.NodePublic(), entry)); !r.OK {
		t.Fatalf("second hello: %+v", r)
	}
	expectClosedUnread(t, old)

	replay := rawStream(t, c1)
	replay.WriteLine(stream.Header{V: 1, Kind: "hello"})
	expectClosedUnread(t, replay)
	late := rawStream(t, c1)
	late.WriteLine(stream.Header{V: 1, Kind: "sync"})
	expectClosedUnread(t, late)

	cur := openEcho(t, c2)
	cur.WriteFrame(stream.Data, []byte("ping"))
	if _, p, err := cur.ReadFrame(); err != nil || string(p) != id+" ping" {
		t.Fatalf("the current tunnel: %q, %v", p, err)
	}
}
