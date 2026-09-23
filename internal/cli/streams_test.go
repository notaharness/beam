//go:build beamtest

package cli_test

import (
	"bufio"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/notaharness/beam/internal/control"
	"github.com/notaharness/beam/internal/stream"
)

// docs/10 "exec, pty": argv, stdin, stdout and stderr, exit status, cwd and
// the injected environment, across two daemons.
func TestExec(t *testing.T) {
	ms := fleet(t, "alpha", "beta")
	a, b := ms[0], ms[1]
	waitState(t, a, b, "connected")

	script := `cat; printf '%s %s %s %s %s\n' "$K" "$BEAM_CALLER_ID" "$BEAM_PEER_ID" "$BEAM_CALLER_LABEL" "$(pwd)"; echo oops >&2; exit 7`
	r := a.beam("from stdin\n", "exec", "beta", "--cwd", "/tmp", "--env", "K=V", "--", "sh", "-c", script)
	want := "from stdin\nV " + a.id() + " " + b.id() + " alpha /tmp\n"
	if r.code != 7 || r.out != want || r.err != "oops\n" {
		t.Fatalf("got code %d\nstdout %q\nstderr %q\nwant code 7, stdout %q, stderr \"oops\\n\"", r.code, r.out, r.err, want)
	}

	if r := a.beam("", "exec", b.id()[:8], "--", "true"); r.code != 0 {
		t.Errorf("exec by id prefix: %+v", r)
	}
	if r := a.beam("", "exec", "beta", "--", "no-such-command-anywhere"); r.code != 1 || !strings.HasPrefix(r.err, "spawn") {
		t.Errorf("unknown command: %+v, want spawn", r)
	}
	if r := a.beam("", "exec", "beta", "--cwd", "relative", "--", "true"); r.code != 1 || !strings.HasPrefix(r.err, "params") {
		t.Errorf("relative cwd: %+v, want params", r)
	}
	if r := a.beam("", "exec", "gamma", "--", "true"); r.code != 1 || !strings.HasPrefix(r.err, "unknown-peer") {
		t.Errorf("unknown peer: %+v, want unknown-peer", r)
	}
	if r := a.beam("", "exec", "beta"); r.code != 2 {
		t.Errorf("exec without argv: %+v, want usage", r)
	}
}

func TestExecKilledBySignal(t *testing.T) {
	ms := fleet(t, "alpha", "beta")
	waitState(t, ms[0], ms[1], "connected")
	r := ms[0].beam("", "exec", "beta", "--", "sh", "-c", "kill -KILL $$")
	if r.code != 128+9 || r.err != "killed by SIGKILL\n" {
		t.Fatalf("got %+v, want 137 and killed by SIGKILL", r)
	}
}

func TestConnect(t *testing.T) {
	ms := fleet(t, "alpha", "beta")
	a := ms[0]
	waitState(t, a, ms[1], "connected")
	r := a.beam("", "connect", "beta", "--", "sh", "-c", "stty size; tty >/dev/null && echo on-a-tty; exit 3")
	if r.code != 3 || !strings.Contains(r.out, "24 80") || !strings.Contains(r.out, "on-a-tty") {
		t.Fatalf("got %+v, want exit 3 on a 24x80 terminal", r)
	}
	if !strings.Contains(r.err, "connecting to beta…") || !strings.Contains(r.err, "remote shell exited (3)") {
		t.Errorf("stderr %q", r.err)
	}
}

// Losing the opener's machine hangs up the remote session once its silence
// passes the 30 s liveness bound (docs/03).
// docs/04 pty: an empty argv runs $SHELL as a login shell when it names an
// executable, else /bin/sh.
func TestLoginShell(t *testing.T) {
	ms := fleet(t, "alpha", "beta")
	a := ms[0]
	waitState(t, a, ms[1], "connected")
	dir := t.TempDir()
	shell, plain := filepath.Join(dir, "mysh"), filepath.Join(dir, "plain")
	os.Symlink("/bin/sh", shell) // a script would show its interpreter's argv[0]
	os.WriteFile(plain, []byte("#!/bin/sh\n"), 0o644)
	for _, tc := range []struct{ shell, want string }{
		{shell, "as -mysh"},
		{filepath.Join(dir, "missing"), "as -sh"},
		{dir, "as -sh"},
		{plain, "as -sh"},
	} {
		t.Setenv("SHELL", tc.shell) // the daemons run in this process
		r := a.beam("echo \"as $0\"; exit 4\n", "connect", "beta")
		if r.code != 4 || !strings.Contains(r.out, tc.want) {
			t.Errorf("SHELL=%s: got %+v, want %q and exit 4", tc.shell, r, tc.want)
		}
	}
}

// A dialer's sync stream lasts as long as its tunnel, so when it ends the
// acceptor ends every stream the tunnel carried, even one whose own close
// never arrives.
func TestSyncEndRetiresTunnel(t *testing.T) {
	b := fleet(t, "beta")[0]
	tun := rawTunnel(t, b)
	sync := rawOpen(t, tun, stream.Header{V: 1, Kind: stream.KindSync})
	pidFile := filepath.Join(t.TempDir(), "pid")
	ex := rawOpen(t, tun, stream.Header{V: 1, Kind: stream.KindExec,
		Argv: []string{"sh", "-c", "echo $$ > " + pidFile + "; exec sleep 300"}})
	defer ex.Close()
	var pid int
	waitFor(t, 10*time.Second, "the remote process", func() bool {
		b, err := os.ReadFile(pidFile)
		pid, _ = strconv.Atoi(strings.TrimSpace(string(b)))
		return err == nil && pid > 0
	})
	sync.Close()
	waitFor(t, 10*time.Second, "the remote process to end", func() bool {
		return syscall.Kill(pid, 0) != nil
	})
}

func TestConnectLossKillsSession(t *testing.T) {
	ms := fleet(t, "alpha", "beta")
	a := ms[0]
	waitState(t, a, ms[1], "connected")
	pidFile := filepath.Join(t.TempDir(), "pid")
	done := make(chan result, 1)
	go func() {
		done <- a.beam("", "connect", "beta", "--", "sh", "-c", "echo $$ > "+pidFile+"; exec sleep 300")
	}()
	var pid int
	waitFor(t, 20*time.Second, "the remote shell", func() bool {
		b, err := os.ReadFile(pidFile)
		pid, _ = strconv.Atoi(strings.TrimSpace(string(b)))
		return err == nil && pid > 0
	})
	ms[0].stop() // the opener's daemon goes away mid-session
	waitFor(t, 45*time.Second, "the remote session to end", func() bool {
		return syscall.Kill(pid, 0) != nil
	})
	if r := <-done; r.code != 1 || !strings.Contains(r.err, "connection lost") {
		t.Errorf("opener: %+v, want connection lost", r)
	}
}

// openPTY opens a pty on peer through m's socket and attaches to it.
// openPTY opens a pty on peer through m and attaches to it.
func openPTY(t *testing.T, m *machine, peer string, argv ...string) (*stream.Conn, string) {
	t.Helper()
	c, err := control.Connect(m.paths(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	var res struct {
		StreamID string `json:"streamId"`
	}
	if err := c.Call("pty.open", map[string]any{"peer": peer, "argv": argv, "cols": 80, "rows": 24}, &res); err != nil {
		t.Fatal(err)
	}
	ac, err := control.Attach(m.paths(), res.StreamID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ac.Close() })
	return ac, res.StreamID
}

// readUntil reads a pty's output until it contains want.
func readUntil(t *testing.T, ac *stream.Conn, want string) {
	t.Helper()
	ac.SetReadDeadline(time.Now().Add(20 * time.Second))
	var out strings.Builder
	for !strings.Contains(out.String(), want) {
		typ, p, err := ac.ReadFrame()
		if err != nil || typ == stream.Close {
			t.Fatalf("pty ended before %q; output %q, %v", want, out.String(), err)
		}
		out.Write(p)
	}
}

// The opener detaching hangs up the remote session at once.
func TestDetachHangsUp(t *testing.T) {
	ms := fleet(t, "alpha", "beta")
	waitState(t, ms[0], ms[1], "connected")
	next := events(t, ms[0])
	ac, id := openPTY(t, ms[0], "beta", "sh", "-c", "echo pid=$$; exec sleep 300")
	var out strings.Builder
	ac.SetReadDeadline(time.Now().Add(20 * time.Second))
	for !strings.Contains(out.String(), "\n") {
		_, p, err := ac.ReadFrame()
		if err != nil {
			t.Fatal(err)
		}
		out.Write(p)
	}
	pid, _ := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(strings.SplitN(out.String(), "\n", 2)[0], "pid=")))
	if pid == 0 {
		t.Fatalf("no pid in %q", out.String())
	}
	ac.Close()
	waitFor(t, 3*time.Second, "the remote session to end", func() bool {
		return syscall.Kill(pid, 0) != nil
	})
	if ev := nextClosed(t, next); ev.StreamID != id || ev.Reason != "detached" {
		t.Errorf("stream.closed %+v, want %s detached", ev, id)
	}
}

func TestPTYResize(t *testing.T) {
	ms := fleet(t, "alpha", "beta")
	waitState(t, ms[0], ms[1], "connected")
	ac, _ := openPTY(t, ms[0], "beta", "sh")
	ac.WriteFrame(stream.Data, []byte("stty size\n"))
	readUntil(t, ac, "24 80")
	b, _ := json.Marshal(stream.Ctl{Kind: "resize", Cols: 132, Rows: 43})
	ac.WriteFrame(stream.Control, b)
	ac.WriteFrame(stream.Data, []byte("stty size\n"))
	readUntil(t, ac, "43 132")
	ac.WriteFrame(stream.Data, []byte("exit 5\n"))
	ac.SetReadDeadline(time.Now().Add(20 * time.Second))
	for {
		typ, p, err := ac.ReadFrame()
		if err != nil {
			t.Fatal(err)
		}
		if typ == stream.Close {
			msg := stream.ParseClose(p)
			if msg.Reason != "exit" || msg.ExitCode == nil || *msg.ExitCode != 5 {
				t.Fatalf("close %+v, want exit 5", msg)
			}
			return
		}
	}
}

// events subscribes to m's events; next returns the next one named name.
func events(t *testing.T, m *machine) (next func(name string) json.RawMessage) {
	t.Helper()
	c, err := net.Dial("unix", m.paths().Socket)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	c.Write([]byte(`{"id":1,"op":"events.subscribe"}` + "\n"))
	r := bufio.NewReader(c)
	next = func(name string) json.RawMessage {
		t.Helper()
		c.SetReadDeadline(time.Now().Add(20 * time.Second))
		for {
			line, err := r.ReadBytes('\n')
			if err != nil {
				t.Fatalf("waiting for %s: %v", name, err)
			}
			var ev struct {
				ID    int             `json:"id"`
				Event string          `json:"event"`
				Data  json.RawMessage `json:"data"`
			}
			json.Unmarshal(line, &ev)
			if ev.Event == name || name == "" && ev.ID == 1 {
				return ev.Data
			}
		}
	}
	next("") // the subscription's reply: events flow from here on
	return next
}

type streamClosed struct {
	StreamID string `json:"streamId"`
	Reason   string `json:"reason"`
	ExitCode *int   `json:"exitCode"`
}

func nextClosed(t *testing.T, next func(string) json.RawMessage) streamClosed {
	t.Helper()
	var ev streamClosed
	json.Unmarshal(next("stream.closed"), &ev)
	return ev
}

// docs/06: stream.close ends an attached stream as detached, a reserved one
// before anything runs, and stream.closed reports each stream's end.
func TestStreamClose(t *testing.T) {
	ms := fleet(t, "alpha", "beta")
	a := ms[0]
	waitState(t, a, ms[1], "connected")
	next := events(t, a)
	c, err := control.Connect(a.paths(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	if r := a.beam("", "exec", "beta", "--", "sh", "-c", "exit 5"); r.code != 5 {
		t.Fatalf("exec: %+v", r)
	}
	if ev := nextClosed(t, next); ev.Reason != "exit" || ev.ExitCode == nil || *ev.ExitCode != 5 {
		t.Errorf("stream.closed %+v, want exit 5", ev)
	}

	ac, id := openPTY(t, a, "beta", "sh", "-c", "echo up; exec sleep 300")
	readUntil(t, ac, "up")
	if err := c.Call("stream.close", map[string]any{"streamId": id}, nil); err != nil {
		t.Fatal(err)
	}
	if ev := nextClosed(t, next); ev.StreamID != id || ev.Reason != "detached" {
		t.Errorf("stream.closed %+v, want %s detached", ev, id)
	}
	if _, _, err := ac.ReadFrame(); err == nil {
		t.Error("the attach connection outlived stream.close")
	}

	var res struct {
		StreamID string `json:"streamId"`
	}
	if err := c.Call("exec.open", map[string]any{"peer": "beta", "argv": []string{"true"}}, &res); err != nil {
		t.Fatal(err)
	}
	if err := c.Call("stream.close", map[string]any{"streamId": res.StreamID}, nil); err != nil {
		t.Fatal(err)
	}
	var oe *control.OpError
	if err := c.Call("stream.close", map[string]any{"streamId": res.StreamID}, nil); !errors.As(err, &oe) || oe.Code != "params" {
		t.Errorf("closing it again: %v, want params", err)
	}
}
