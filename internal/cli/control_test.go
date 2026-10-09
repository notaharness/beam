//go:build beamtest

package cli_test

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"
)

// stdioProxy is `beam control --stdio` running on a machine.
type stdioProxy struct {
	t     *testing.T
	stdin *io.PipeWriter
	lines chan string
	errb  strings.Builder
	done  chan int
}

func newProxy(t *testing.T, m *machine) *stdioProxy {
	t.Helper()
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	p := &stdioProxy{t: t, stdin: inW, lines: make(chan string), done: make(chan int, 1)}
	go func() {
		p.done <- m.run(inR, outW, &p.errb, "control", "--stdio")
		outW.Close()
	}()
	go func() {
		defer close(p.lines)
		r := bufio.NewReader(outR)
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				return
			}
			p.lines <- line
		}
	}()
	t.Cleanup(func() { inW.Close(); outR.Close() })
	return p
}

// send writes a line to the proxy's stdin.
func (p *stdioProxy) send(line string) {
	p.t.Helper()
	if _, err := fmt.Fprintln(p.stdin, line); err != nil {
		p.t.Fatalf("stdin: %v", err)
	}
}

// next reads a line of its stdout.
func (p *stdioProxy) next() map[string]any {
	p.t.Helper()
	select {
	case line, ok := <-p.lines:
		var v map[string]any
		if !ok || json.Unmarshal([]byte(line), &v) != nil {
			p.t.Fatalf("stdout ended or not JSON: %q", line)
		}
		return v
	case <-time.After(30 * time.Second):
		p.t.Fatal("no line on stdout")
	}
	return nil
}

// exit waits for the proxy's exit code.
func (p *stdioProxy) exit() int {
	p.t.Helper()
	select {
	case code := <-p.done:
		return code
	case <-time.After(10 * time.Second):
		p.t.Fatal("the proxy did not exit")
	}
	return -1
}

// docs/07 beam control: the control connection on stdin and stdout, bytes as
// they are. Mail it carries is the client's to settle: the proxy acks
// nothing, so an envelope stays in inbound until the client's own msg.ack.
// The end of stdin ends it, exit 0.
func TestControlStdio(t *testing.T) {
	ms := fleet(t, "alpha", "beta")
	a, b := ms[0], ms[1]
	waitState(t, a, b, "connected")
	p := newProxy(t, b)
	p.send(`{"id":1,"op":"msg.subscribe","topic":"orchestra"}`)
	if r := p.next(); r["id"] != 1.0 || r["ok"] != true {
		t.Fatalf("subscribe: %v", r)
	}
	if r := a.beam("", "msg", "send", "beta", "--topic", "orchestra", "report"); r.out != "delivered to beta\n" {
		t.Fatalf("send: %+v", r)
	}
	ev := p.next()
	mail, _ := ev["data"].(map[string]any)
	if ev["event"] != "mail" || mail["payload"] != "report" {
		t.Fatalf("got %v, want the mail event", ev)
	}
	time.Sleep(300 * time.Millisecond) // for an ack, were the proxy to send one
	if q := queue(t, b, "--which", "inbound"); len(q) != 1 {
		t.Fatalf("inbound before the client's ack: %q, want the envelope", q)
	}
	p.send(fmt.Sprintf(`{"id":2,"op":"msg.ack","envelopeId":%q}`, mail["id"]))
	if r := p.next(); r["id"] != 2.0 || r["ok"] != true {
		t.Fatalf("ack: %v", r)
	}
	if q := queue(t, b, "--which", "inbound"); len(q) != 0 {
		t.Errorf("inbound after the ack: %q", q)
	}
	p.stdin.Close()
	if code := p.exit(); code != 0 {
		t.Errorf("exit %d at the end of stdin, want 0", code)
	}
}

// docs/07 beam control: the daemon closing the connection ends the proxy,
// exit 1; and the command takes --stdio and nothing else.
func TestControlStdioDaemonGone(t *testing.T) {
	m := fleet(t, "alpha")[0]
	p := newProxy(t, m)
	p.send(`{"id":1,"op":"status"}`)
	if r := p.next(); r["ok"] != true {
		t.Fatalf("status: %v", r)
	}
	m.stop()
	if code := p.exit(); code != 1 || !strings.Contains(p.errb.String(), "the daemon closed the connection") {
		t.Errorf("exit %d, %q; want 1, the daemon closed the connection", code, p.errb.String())
	}
	for _, args := range [][]string{{"control"}, {"control", "--stdio", "extra"}} {
		if r := m.beam("", args...); r.code != 2 {
			t.Errorf("%v: exit %d, want 2", args, r.code)
		}
	}
}
