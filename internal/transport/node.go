package transport

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/notaharness/beam/internal/stream"
	"github.com/tailscale/tailcat"
	"tailscale.com/types/key"
)

// dialTimeout bounds bringing up a tunnel and its hello (docs/03, Lifecycle).
const dialTimeout = 20 * time.Second

// Config configures a Node.
type Config struct {
	Key    *Key
	Entry  json.RawMessage // this machine's signed entry, sent in every hello
	Admit  AdmitFunc
	Handle HandleFunc
	Logf   func(format string, args ...any)
}

// Node is a machine's transport: its Server and the Clients it dialed.
type Node struct {
	cfg Config
	srv *tailcat.Server
	adm *admission

	mu      sync.Mutex
	tunnels map[*Tunnel]bool
}

// Start starts the Server on the machine's node key and accepts streams.
func Start(cfg Config) (*Node, error) {
	srv := &tailcat.Server{
		Key:          cfg.Key.pk.Private,
		PresharedKey: cfg.Key.pk.Public.PresharedKey,
		Region:       cfg.Key.pk.Public.Region[0],
		Logf:         cfg.Logf,
	}
	ln, err := srv.Listen(context.Background(), "tcp", fmt.Sprintf(":%d", Port))
	if err != nil {
		srv.Close()
		return nil, err
	}
	n := &Node{cfg: cfg, srv: srv, adm: newAdmission(cfg.Key, cfg.Admit, cfg.Handle), tunnels: map[*Tunnel]bool{}}
	go n.accept(ln)
	return n, nil
}

func (n *Node) accept(ln net.Listener) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		go n.serve(conn)
	}
}

// serve admits a stream by the key of the tunnel it arrived on, failing closed
// when tailcat cannot name it.
func (n *Node) serve(conn net.Conn) {
	for _, kv := range n.srv.PeerEnv(conn.LocalAddr(), conn.RemoteAddr()) {
		v, ok := strings.CutPrefix(kv, "TAILCAT_PEER_KEY=")
		var c key.NodePublic
		if ok && c.UnmarshalText([]byte(v)) == nil {
			n.adm.serve(conn, raw(c))
			return
		}
	}
	conn.Close()
}

// Close closes every tunnel this machine dialed and its Server.
func (n *Node) Close() error {
	n.mu.Lock()
	for t := range n.tunnels {
		t.client.Close()
	}
	n.mu.Unlock()
	return n.srv.Close()
}

// Tunnel is a tunnel this machine dialed, on a Client with a key of its own,
// past hello. Streams on it are opened by this machine only.
type Tunnel struct {
	node   *Node
	client *tailcat.Client
}

// Refused is the far side's refusal of a hello or a stream, by reason token.
type Refused struct {
	Reason, Detail string
}

func (r *Refused) Error() string {
	if r.Detail != "" {
		return "refused: " + r.Reason + ": " + r.Detail
	}
	return "refused: " + r.Reason
}

// Dial brings up a tunnel to address and completes hello on it.
func (n *Node) Dial(ctx context.Context, address string) (*Tunnel, error) {
	ci, err := tailcat.ParseAddr(tailcat.Addr(address))
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, dialTimeout)
	defer cancel()
	t := &Tunnel{node: n, client: &tailcat.Client{Server: tailcat.Addr(address), Logf: n.cfg.Logf}}
	if err := t.hello(ctx, raw(ci.ServerPublic.NodePublic)); err != nil {
		t.client.Close()
		return nil, err
	}
	n.mu.Lock()
	n.tunnels[t] = true
	n.mu.Unlock()
	return t, nil
}

func (t *Tunnel) hello(ctx context.Context, r [32]byte) error {
	sc, err := t.Open(ctx, stream.Header{V: 1, Kind: "hello"})
	if err != nil {
		return err
	}
	defer sc.Close()
	b, _ := json.Marshal(newHello(t.node.cfg.Key, raw(t.client.PublicKey()), r, t.node.cfg.Entry))
	if err := sc.WriteFrame(stream.Data, b); err != nil {
		return err
	}
	_, p, err := sc.ReadFrame()
	if err != nil {
		return err
	}
	var res stream.Response
	if err := json.Unmarshal(p, &res); err != nil {
		return err
	}
	if !res.OK {
		return &Refused{res.Reason, res.Detail}
	}
	return nil
}

// Open opens a stream: it sends h and returns the connection once the far side
// accepts it.
func (t *Tunnel) Open(ctx context.Context, h stream.Header) (*stream.Conn, error) {
	conn, err := t.client.DialTCPPort(ctx, Port)
	if err != nil {
		return nil, err
	}
	if d, ok := ctx.Deadline(); ok {
		conn.SetDeadline(d)
	}
	sc := stream.NewConn(conn)
	var res stream.Response
	if err := sc.WriteLine(h); err != nil {
		conn.Close()
		return nil, err
	}
	if err := sc.ReadLine(&res); err != nil {
		conn.Close()
		return nil, err
	}
	if !res.OK {
		conn.Close()
		return nil, &Refused{res.Reason, res.Detail}
	}
	conn.SetDeadline(time.Time{})
	return sc, nil
}

// Close closes the tunnel's Client.
func (t *Tunnel) Close() error {
	t.node.mu.Lock()
	delete(t.node.tunnels, t)
	t.node.mu.Unlock()
	return t.client.Close()
}
