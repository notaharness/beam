package stream

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// Limits on a pty or exec header (docs/04).
const (
	maxArgv    = 1024
	maxArg     = 64 << 10
	maxEnv     = 256
	minWinSize = 2
	maxWinSize = 500
	killGrace  = 5 * time.Second // SIGHUP to SIGKILL, and close to reset
)

// Spawn is how the acceptor runs a stream's process.
type Spawn struct {
	Env    []string          // the daemon's environment
	Inject map[string]string // BEAM_* variables, set last
	Home   string            // what ~ means
}

// refusal is why a header is refused before anything runs.
type refusal struct{ reason, detail string }

// refuse answers the header with r and closes the stream.
func refuse(c *Conn, r *refusal) {
	_ = c.WriteLine(Response{Reason: r.reason, Detail: r.detail}) // closing either way
	c.Close()
}

func checkHeader(h Header) *refusal {
	if len(h.Argv) > maxArgv || len(h.Env) > maxEnv {
		return &refusal{"params", "too many arguments or variables"}
	}
	for _, a := range h.Argv {
		if len(a) > maxArg {
			return &refusal{"params", "argument over 64 KiB"}
		}
	}
	return nil
}

// command builds the process for h. An empty argv is the login shell.
func (sp Spawn) command(h Header) (*exec.Cmd, *refusal) {
	dir, ok := resolveCwd(h.Cwd, sp.Home)
	if !ok {
		return nil, &refusal{"params", "cwd must be absolute or ~/-relative"}
	}
	env := mergeEnv(sp.Env, h.Env, sp.Inject)
	var cmd *exec.Cmd
	if len(h.Argv) == 0 {
		shell := loginShell(env)
		cmd = &exec.Cmd{Path: shell, Args: []string{"-" + filepath.Base(shell)}}
	} else {
		path, err := exec.LookPath(h.Argv[0])
		if err != nil {
			return nil, &refusal{"spawn", err.Error()}
		}
		cmd = &exec.Cmd{Path: path, Args: h.Argv}
	}
	cmd.Dir, cmd.Env = dir, env
	return cmd, nil
}

func resolveCwd(cwd, home string) (string, bool) {
	switch {
	case cwd == "" || cwd == "~":
		return home, true
	case strings.HasPrefix(cwd, "~/"):
		return filepath.Join(home, cwd[2:]), true
	case filepath.IsAbs(cwd):
		return cwd, true
	}
	return "", false
}

// loginShell is $SHELL if it names an executable, else /bin/sh.
func loginShell(env []string) string {
	for i := len(env) - 1; i >= 0; i-- {
		if v, ok := strings.CutPrefix(env[i], "SHELL="); ok {
			if st, err := os.Stat(v); err == nil && filepath.IsAbs(v) && st.Mode().IsRegular() && st.Mode()&0o111 != 0 {
				return v
			}
			break
		}
	}
	return "/bin/sh"
}

// mergeEnv is base with over applied, then inject.
func mergeEnv(base []string, over, inject map[string]string) []string {
	m := map[string]string{}
	var order []string
	set := func(k, v string) {
		if _, ok := m[k]; !ok {
			order = append(order, k)
		}
		m[k] = v
	}
	for _, kv := range base {
		if k, v, ok := strings.Cut(kv, "="); ok {
			set(k, v)
		}
	}
	for k, v := range over {
		set(k, v)
	}
	for k, v := range inject {
		set(k, v)
	}
	env := make([]string, len(order))
	for i, k := range order {
		env[i] = k + "=" + m[k]
	}
	return env
}

// exitMsg is the close frame for a finished process. A process killed by a
// signal reports 128+signal as its exit code, as a shell does.
func exitMsg(ws syscall.WaitStatus) CloseMsg {
	code := ws.ExitStatus()
	msg := CloseMsg{Reason: "exit", ExitCode: &code}
	if ws.Signaled() {
		code = 128 + int(ws.Signal())
		name := unix.SignalName(ws.Signal())
		msg.Signal = &name
	}
	return msg
}

// group is a stream's process group. Its leader is reaped only after the
// group's teardown: until then the leader, even exited, holds the group id,
// so a signal to the group never reaches a process that reused it. Its exit
// status is read without reaping it.
type group struct {
	cmd    *exec.Cmd
	exited chan struct{}      // the leader has exited
	status syscall.WaitStatus // its status, once exited

	mu     sync.Mutex
	reaped bool
}

func newGroup(cmd *exec.Cmd) *group {
	g := &group{cmd: cmd, exited: make(chan struct{})}
	go func() {
		ws, err := awaitExit(cmd.Process.Pid)
		if err != nil { // it cannot be watched unreaped: reap it for its status, and signal it no more
			g.reap()
			ws = cmd.ProcessState.Sys().(syscall.WaitStatus)
		}
		g.status = ws
		close(g.exited)
	}()
	return g
}

func (g *group) signal(sig syscall.Signal) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if !g.reaped {
		_ = syscall.Kill(-g.cmd.Process.Pid, sig) // the group may be empty already
	}
}

// reap collects the leader, waiting for it to exit, under the lock signals
// take: no signal to its group follows the reap that frees the group id.
func (g *group) reap() {
	g.mu.Lock()
	defer g.mu.Unlock()
	if !g.reaped {
		_ = g.cmd.Wait() // its status is exitMsg's, read before
		g.reaped = true
	}
}

// inputAhead is how many of the opener's frames are read ahead of a process
// slow to take them; beyond that the opener is held back.
const inputAhead = 4

// feed reads the opener's frames until its side ends, it sends close, or the
// stream is closed here, which a feed held back by a process that is not
// reading still notices. frame turns each into input for q, a nil input
// ending it. feed closes q when the opener is gone.
func feed(c *Conn, q chan<- []byte, frame func(Type, []byte) (in []byte, ok bool)) {
	defer close(q)
	for {
		t, p, err := c.ReadFrame()
		if err != nil || t == Close {
			return
		}
		in, ok := frame(t, p)
		if !ok {
			continue
		}
		select {
		case q <- in:
		case <-c.Done():
			return
		}
	}
}

// deliver writes queued input to w until q closes; nil closes w. A process
// that stopped reading and exited, or whose input was closed on the
// opener's departure, fails the writes and the rest is dropped.
func deliver(q <-chan []byte, w io.WriteCloser) {
	for p := range q {
		if p == nil {
			_ = w.Close() // the process sees end of input
			continue
		}
		_, _ = w.Write(p)
	}
}

// writer serialises frames from several goroutines.
type writer struct {
	mu sync.Mutex
	c  *Conn
}

func (w *writer) frame(t Type, p []byte) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.c.WriteFrame(t, p)
}

// finish sends the close frame, half-closes, and waits for the opener to close
// its side (or for done) before closing, so the close frame is not lost to a
// reset.
func (w *writer) finish(msg CloseMsg, openerDone <-chan struct{}) {
	w.mu.Lock()
	_ = w.c.WriteJSON(Close, msg) // closing either way
	w.mu.Unlock()
	if cw, ok := w.c.Conn.(interface{ CloseWrite() error }); ok {
		_ = cw.CloseWrite() // closing either way
	}
	select {
	case <-openerDone:
	case <-time.After(killGrace):
	}
	w.c.Close()
}
