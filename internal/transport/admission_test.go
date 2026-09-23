package transport

import (
	"crypto/rand"
	"encoding/json"
	"net"
	"testing"
	"time"

	"github.com/notaharness/beam/internal/stream"
)

// pipeHello starts a hello for client key c on adm over a pipe: header, then
// the frame. It returns the dialer's side, ready to read the result.
func pipeHello(t *testing.T, adm *admission, c [32]byte, f helloFrame) *stream.Conn {
	t.Helper()
	sc := openHello(t, adm, c)
	b, _ := json.Marshal(f)
	go sc.WriteFrame(stream.Data, b)
	return sc
}

// openHello starts a hello for client key c on adm and leaves it waiting for
// its frame.
func openHello(t *testing.T, adm *admission, c [32]byte) *stream.Conn {
	t.Helper()
	sc := pipeStream(t, adm, c)
	sc.WriteLine(stream.Header{V: 1, Kind: "hello"})
	var r stream.Response
	if err := sc.ReadLine(&r); err != nil || !r.OK {
		t.Fatalf("hello header: %+v, %v", r, err)
	}
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
		case point == "finished":
			close(decided)
		}
	}

	first := pipeHello(t, adm, c1, newHello(member, c1, srv.NodePublic(), entry))
	<-paused

	second := pipeStream(t, adm, c1)
	second.WriteLine(stream.Header{V: 1, Kind: "hello"})
	expectClosedUnread(t, second)

	for range specMaxUnbound { // fill the budget: the paused hello is the oldest
		openHello(t, adm, randomKey())
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

// A tunnel leaves the hello budget in the step that binds it: filling the
// budget straight after evicts only tunnels still in hello.
func TestBindLeavesBudget(t *testing.T) {
	srv, member := NewKey(relay.Region), NewKey(relay.Region)
	id, entry := entryFor(member)
	adm := newAdmission(srv, admitFake, echoCaller)
	c1 := randomKey()
	paused, release := make(chan struct{}), make(chan struct{})
	adm.hook = func(point string, c [32]byte) {
		if c == c1 && point == "finished" {
			close(paused)
			<-release
		}
	}

	first := pipeHello(t, adm, c1, newHello(member, c1, srv.NodePublic(), entry))
	<-paused
	fillers := make([]*stream.Conn, specMaxUnbound)
	for i := range fillers {
		fillers[i] = openHello(t, adm, randomKey())
	}
	close(release)
	if r, err := helloResult(first); err != nil || !r.OK {
		t.Fatalf("hello: %+v, %v", r, err)
	}

	expectEcho(t, adm, c1, id)
	expectOpen(t, fillers[0])
}

// Only hellos under way count against the budget: not one that ended
// without a verdict, one that failed, or one that bound. Past the budget
// each new hello evicts the oldest under way.
func TestBudgetCountsHellosUnderWay(t *testing.T) {
	srv, member := NewKey(relay.Region), NewKey(relay.Region)
	id, entry := entryFor(member)
	adm := newAdmission(srv, admitFake, echoCaller)

	quit := pipeStream(t, adm, randomKey())
	quit.WriteLine(stream.Header{V: 1, Kind: "sync"})
	expectRefused(t, quit, "unauthenticated")
	if r, _ := helloResult(pipeHello(t, adm, randomKey(), helloFrame{})); r.OK {
		t.Fatal("an empty hello was admitted")
	}
	c := randomKey()
	if r, err := helloResult(pipeHello(t, adm, c, newHello(member, c, srv.NodePublic(), entry))); err != nil || !r.OK {
		t.Fatalf("hello: %+v, %v", r, err)
	}

	open := make([]*stream.Conn, specMaxUnbound+2)
	for i := range open {
		open[i] = openHello(t, adm, randomKey())
	}
	expectClosedUnread(t, open[0])
	expectClosedUnread(t, open[1])
	for _, sc := range open[2:] {
		expectOpen(t, sc)
	}
	expectEcho(t, adm, c, id)
}

// expectEcho opens a stream on the bound tunnel c and exchanges one frame.
func expectEcho(t *testing.T, adm *admission, c [32]byte, peer string) {
	t.Helper()
	sc := pipeStream(t, adm, c)
	sc.WriteLine(stream.Header{V: 1, Kind: "sync"})
	var r stream.Response
	if err := sc.ReadLine(&r); err != nil || !r.OK {
		t.Fatalf("the bound tunnel: %+v, %v", r, err)
	}
	go sc.WriteFrame(stream.Data, []byte("ping"))
	if _, p, err := sc.ReadFrame(); err != nil || string(p) != peer+" ping" {
		t.Fatalf("the bound tunnel: %q, %v", p, err)
	}
}
