package stream

import (
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
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

// InputWindow is how many of the opener's input frames on pty and exec, data
// or control, may be outstanding: sent and not yet answered taken (docs/04,
// Input).
const InputWindow = 4

// window counts an opener's outstanding input frames.
type window struct {
	w  *writer
	mu sync.Mutex
	n  int
}

// take admits an input frame; false is an overrun.
func (win *window) take() bool {
	win.mu.Lock()
	defer win.mu.Unlock()
	if win.n == InputWindow {
		return false
	}
	win.n++
	return true
}

// taken gives a frame's place back, telling the opener.
func (win *window) taken() {
	win.mu.Lock()
	win.n--
	win.mu.Unlock()
	_ = win.w.control(Ctl{Kind: "taken"}) // a lost opener ends feed
}

// feed reads the opener's frames until its side ends or sends close, or it
// overruns its window, which feed reports. Each data or control frame takes
// a place in win. frame turns it into input for q (nil ending the input), or
// handles it at once, and its place is given back then. q holds a window, so
// feed never waits on the process: it always reads on. It closes q.
func feed(c *Conn, q chan<- []byte, win *window, frame func(Type, []byte) (in []byte, queue bool)) (overrun bool) {
	defer close(q)
	for {
		t, p, err := c.ReadFrame()
		switch {
		case err != nil || t == Close:
			return false
		case !win.take():
			return true
		}
		if in, queue := frame(t, p); queue {
			q <- in
		} else {
			win.taken()
		}
	}
}

// discard drops an opener's frames after its overrun until its side ends, so
// the close it is sent is not lost to a reset over input left unread.
func discard(c *Conn) {
	for {
		if _, _, err := c.ReadFrame(); err != nil {
			return
		}
	}
}

// deliver writes queued input to w until q closes, nil closing w, and calls
// taken after each. A process that exited, or whose input was closed on the
// opener's departure, fails the writes and the rest is dropped.
func deliver(q <-chan []byte, w io.WriteCloser, taken func()) {
	for p := range q {
		if p == nil {
			_ = w.Close() // the process sees end of input
		} else {
			_, _ = w.Write(p)
		}
		taken()
	}
}

// ended is the close of a stream whose leader exited with ws: "window" if the
// opener overran its window, which ended it, else the exit.
func ended(ws syscall.WaitStatus, overrun *atomic.Bool) CloseMsg {
	if overrun.Load() {
		return CloseMsg{Reason: "window"}
	}
	return exitMsg(ws)
}

// writer serialises frames from several goroutines.
type writer struct {
	mu sync.Mutex
	c  *Conn
}

func (w *writer) control(ctl Ctl) error {
	b, _ := json.Marshal(ctl)
	return w.frame(Control, b)
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
