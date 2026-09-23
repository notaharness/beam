package transport

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/notaharness/beam/internal/stream"
)

// pipeHello starts a hello for client key c on adm over a pipe: header, then
// the frame. It returns the dialer's side, ready to read the result.
func pipeHello(t *testing.T, adm *admission, c [32]byte, f helloFrame) *stream.Conn {
	t.Helper()
	sc := pipeStream(t, adm, c)
	sc.WriteLine(stream.Header{V: 1, Kind: "hello"})
	var r stream.Response
	if err := sc.ReadLine(&r); err != nil || !r.OK {
		t.Fatalf("hello header: %+v, %v", r, err)
	}
	b, _ := json.Marshal(f)
	go sc.WriteFrame(stream.Data, b)
	return sc
}

func pipeStream(t *testing.T, adm *admission, c [32]byte) *stream.Conn {
	t.Helper()
	dialer, acceptor := net.Pipe()
	t.Cleanup(func() { dialer.Close() })
	go adm.serve(acceptor, c)
	sc := stream.NewConn(dialer)
	sc.SetDeadline(time.Now().Add(5 * time.Second))
	return sc
}

func helloResult(sc *stream.Conn) (stream.Response, error) {
	var r stream.Response
	_, p, err := sc.ReadFrame()
	if err == nil {
		err = json.Unmarshal(p, &r)
	}
	return r, err
}

func randomKey() (k [32]byte) {
	rand.Read(k[:])
	return k
}

// A hello that verified but lost its slot before binding must not bind: once
// a client key has failed it never carries a stream again. Two hellos for one
// key never run at once.
func TestOverlappingHellos(t *testing.T) {
	srv, member := NewKey(relay.Region), NewKey(relay.Region)
	_, entry := entryFor(member)
	adm := newAdmission(srv, admitFake, echoCaller)
	c1 := randomKey()
	paused, release, decided := make(chan struct{}), make(chan struct{}), make(chan struct{})
	adm.hook = func(point string, c [32]byte) {
		switch {
		case c != c1:
		case point == "admitted":
			close(paused)
			<-release
		case point == "bound":
			close(decided)
		}
	}

	first := pipeHello(t, adm, c1, newHello(member, c1, srv.NodePublic(), entry))
	<-paused

	second := pipeStream(t, adm, c1)
	second.WriteLine(stream.Header{V: 1, Kind: "hello"})
	expectClosedUnread(t, second)

	for range specMaxUnbound { // fill the budget: the paused hello is the oldest
		sc := pipeStream(t, adm, randomKey())
		sc.WriteLine(stream.Header{V: 1, Kind: "hello"})
		var r stream.Response
		if err := sc.ReadLine(&r); err != nil || !r.OK {
			t.Fatalf("filler: %+v, %v", r, err)
		}
	}
	close(release)
	<-decided
	if r, err := helloResult(first); err == nil && r.OK {
		t.Fatal("an evicted hello was admitted")
	}
	late := pipeStream(t, adm, c1)
	late.WriteLine(stream.Header{V: 1, Kind: "sync"})
	expectClosedUnread(t, late)
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
