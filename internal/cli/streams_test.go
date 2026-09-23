//go:build beamtest

package cli_test

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
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
	tun, _ := rawTunnel(t, b)
	sync := rawOpen(t, tun, stream.Header{V: 1, Kind: stream.KindSync})
	pidFile := filepath.Join(t.TempDir(), "pid")
	ex := rawOpen(t, tun, stream.Header{V: 1, Kind: stream.KindExec,
		Argv: []string{"sh", "-c", "echo $$ > " + pidFile + "; exec sleep 300"}})
	defer ex.Close()
	pid := waitPid(t, pidFile)
	sync.Close()
	waitGone(t, pid)
}

// waitPid waits for the process a remote shell wrote to file and returns it.
func waitPid(t *testing.T, file string) int {
	t.Helper()
	var pid int
	waitFor(t, 10*time.Second, "the remote process", func() bool {
		b, err := os.ReadFile(file)
		pid, _ = strconv.Atoi(strings.TrimSpace(string(b)))
		return err == nil && pid > 0
	})
	return pid
}

func waitGone(t *testing.T, pid int) {
	t.Helper()
	waitFor(t, 10*time.Second, "the remote process to end", func() bool { return ended(pid) })
}

// ended reports whether pid runs no more: it is gone, or a zombie not yet
// reaped (the acceptor keeps an exited leader until its group is torn down).
func ended(pid int) bool {
	if syscall.Kill(pid, 0) != nil {
		return true
	}
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	i := bytes.LastIndexByte(b, ')') // the state follows the command's name
	return err == nil && i > 0 && i+2 < len(b) && b[i+2] == 'Z'
}

// docs/04: the opener's end kills a process that never reads its input,
// whether the stream ends behind input the process has not taken, or the
// tunnel is retired while that input is held back.
func TestInputNeverRead(t *testing.T) {
	b := fleet(t, "beta")[0]
	for _, kind := range []string{stream.KindExec, stream.KindPTY} {
		for _, tc := range []struct {
			name   string
			frames int
			end    func(s, sync *stream.Conn)
		}{
			{"stream closed behind its input", 1, func(s, _ *stream.Conn) { s.Close() }},
			{"tunnel retired while input is held back", 16, func(_, sync *stream.Conn) { sync.Close() }},
		} {
			t.Run(kind+"/"+tc.name, func(t *testing.T) {
				tun, _ := rawTunnel(t, b)
				sync := rawOpen(t, tun, stream.Header{V: 1, Kind: stream.KindSync})
				pidFile := filepath.Join(t.TempDir(), "pid")
				s := rawOpen(t, tun, stream.Header{V: 1, Kind: kind, Cols: 80, Rows: 24,
					Argv: []string{"sh", "-c", "echo $$ > " + pidFile + "; exec sleep 300"}})
				pid := waitPid(t, pidFile)
				frame := make([]byte, stream.MaxPayload) // for exec, channel 0: stdin
				sent := make(chan struct{})
				go func() {
					defer close(sent)
					for range tc.frames {
						if s.WriteFrame(stream.Data, frame) != nil {
							return
						}
					}
				}()
				if tc.frames == 1 {
					<-sent
				} else {
					time.Sleep(time.Second) // until the acceptor holds input back
				}
				tc.end(s, sync)
				waitGone(t, pid)
			})
		}
	}
}

// docs/04: a pty whose opener leaves gets SIGHUP and then, 5 s later,
// SIGKILL for its whole process group, even once the leader has exited.
func TestHangupThenKill(t *testing.T) {
	b := fleet(t, "beta")[0]
	tun, _ := rawTunnel(t, b)
	dir := t.TempDir()
	// The child ignores SIGHUP before it says who it is.
	os.WriteFile(dir+"/child.sh", []byte("trap '' HUP; echo $$ > "+dir+"/child; exec sleep 300\n"), 0o600)
	s := rawOpen(t, tun, stream.Header{V: 1, Kind: stream.KindPTY, Cols: 80, Rows: 24, Argv: []string{"sh", "-c",
		"sh " + dir + "/child.sh >/dev/null 2>&1 </dev/null & echo $$ > " + dir + "/leader; exec sleep 300"}})
	leader, child := waitPid(t, dir+"/leader"), waitPid(t, dir+"/child")
	s.Close()
	waitGone(t, leader)
	if ended(child) {
		t.Fatal("the child that ignores SIGHUP ended with its leader")
	}
	waitGone(t, child)
}

// docs/04, D30: the group outlives a leader that exits first, and the opener's
// end still tears it down: the exit close comes before the reap.
func TestLeaderExitsFirst(t *testing.T) {
	for _, kind := range []string{stream.KindExec, stream.KindPTY} {
		t.Run(kind, func(t *testing.T) {
			b := fleet(t, "beta")[0]
			tun, _ := rawTunnel(t, b)
			dir := t.TempDir()
			os.WriteFile(dir+"/child.sh", []byte("trap '' HUP; echo $$ > "+dir+"/child; exec sleep 300\n"), 0o600)
			s := rawOpen(t, tun, stream.Header{V: 1, Kind: kind, Cols: 80, Rows: 24, Argv: []string{"sh", "-c",
				"sh " + dir + "/child.sh >/dev/null 2>&1 </dev/null & while [ ! -s " + dir + "/child ]; do sleep 0.1; done; exit 3"}})
			if msg := readClose(t, s); msg.Reason != "exit" || msg.ExitCode == nil || *msg.ExitCode != 3 {
				t.Fatalf("close %+v, want exit 3", msg)
			}
			child := waitPid(t, dir+"/child")
			if ended(child) {
				t.Fatal("the child ended with its leader")
			}
			s.Close()
			waitGone(t, child)
		})
	}
}

// Losing the opener's machine hangs up the remote session once its silence
// passes the 30 s liveness bound (docs/03).
func TestConnectLossKillsSession(t *testing.T) {
	ms := fleet(t, "alpha", "beta")
	a := ms[0]
	waitState(t, a, ms[1], "connected")
	pidFile := filepath.Join(t.TempDir(), "pid")
	done := make(chan result, 1)
	go func() {
		done <- a.beam("", "connect", "beta", "--", "sh", "-c", "echo $$ > "+pidFile+"; exec sleep 300")
	}()
	pid := waitPid(t, pidFile)
	ms[0].stop() // the opener's daemon goes away mid-session
	waitFor(t, 45*time.Second, "the remote session to end", func() bool { return ended(pid) })
	if r := <-done; r.code != 1 || !strings.Contains(r.err, "connection lost") {
		t.Errorf("opener: %+v, want connection lost", r)
	}
}

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
	waitGone(t, pid)
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
	if msg := readClose(t, ac); msg.Reason != "detached" {
		t.Errorf("close %+v, want detached", msg)
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

// readClose reads an attach connection's frames up to its close.
func readClose(t *testing.T, ac *stream.Conn) stream.CloseMsg {
	t.Helper()
	ac.SetReadDeadline(time.Now().Add(20 * time.Second))
	for {
		typ, p, err := ac.ReadFrame()
		if err != nil {
			t.Fatalf("no close frame: %v", err)
		}
		if typ == stream.Close {
			return stream.ParseClose(p)
		}
	}
}

// docs/06: stream.close, or the client leaving, before the remote stream is
// open ends the stream there: nothing is sent, whether the daemon was still
// dialing the peer or about to open on a live tunnel.
func TestDetachBeforeOpen(t *testing.T) {
	for _, tc := range []struct {
		name   string
		close  bool // stream.close, else the client leaves
		peerUp bool
	}{
		{"stream.close while dialing", true, false},
		{"client leaves while dialing", false, false},
		{"stream.close on a live tunnel", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, b := newMachine(t, "alpha"), newMachine(t, "beta")
			a.knows(t, b)
			b.knows(t, a)
			a.start(t)
			if tc.peerUp {
				b.start(t)
				waitState(t, a, b, "connected")
			}
			bin, started := watchExec(t)
			next := events(t, a)
			c, err := control.Connect(a.paths(), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			var res struct {
				StreamID string `json:"streamId"`
			}
			if err := c.Call("exec.open", map[string]any{"peer": "beta", "argv": []string{bin}}, &res); err != nil {
				t.Fatal(err)
			}
			reached, release := pauseAt(t, a, "attached", b)
			ac, err := control.Attach(a.paths(), res.StreamID)
			if err != nil {
				t.Fatal(err)
			}
			defer ac.Close()
			await(t, reached, "the attach")
			if tc.close {
				if err := c.Call("stream.close", map[string]any{"streamId": res.StreamID}, nil); err != nil {
					t.Fatal(err)
				}
			} else {
				ac.Close()
			}
			release()
			if tc.close {
				if msg := readClose(t, ac); msg.Reason != "detached" {
					t.Errorf("close %+v, want detached", msg)
				}
			}
			if ev := nextClosed(t, next); ev.StreamID != res.StreamID || ev.Reason != "detached" {
				t.Errorf("stream.closed %+v, want detached", ev)
			}
			if !tc.peerUp {
				b.start(t)
				waitState(t, a, b, "connected")
			}
			time.Sleep(time.Second) // time enough for an open still under way to arrive
			if started() {
				t.Fatal("the exec started after its stream was closed")
			}
		})
	}
}
