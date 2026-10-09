package stream

import (
	"net"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Spawn is how the acceptor runs a stream's process.
type Spawn struct {
	Env    []string          // the daemon's environment, of which a login's part is passed on
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

// Limits on a pty or exec header (docs/04).
const (
	maxArgv    = 1024
	maxArg     = 64 << 10
	maxEnv     = 256
	minWinSize = 2
	maxWinSize = 500
	killGrace  = 5 * time.Second // SIGHUP to SIGKILL, and close to reset
)

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

// command builds the process for h's argv, which is not empty.
func (sp Spawn) command(h Header) (*exec.Cmd, *refusal) {
	return sp.in(h, func([]string) (*exec.Cmd, *refusal) {
		path, err := exec.LookPath(h.Argv[0])
		if err != nil {
			return nil, &refusal{"spawn", err.Error()}
		}
		return &exec.Cmd{Path: path, Args: h.Argv}, nil
	})
}

// in builds the process program makes, given its environment, in h's
// directory and with that environment.
func (sp Spawn) in(h Header, program func(env []string) (*exec.Cmd, *refusal)) (*exec.Cmd, *refusal) {
	dir, ok := resolveCwd(h.Cwd, sp.Home)
	if !ok {
		return nil, &refusal{"params", "cwd must be absolute or ~/-relative"}
	}
	env := mergeEnv(loginEnv(sp.Env), h.Env, sp.Inject)
	cmd, ref := program(env)
	if ref != nil {
		return nil, ref
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

// loginEnv is the part of env a login would give (docs/04): who and where
// the user is, the path, the terminal type, the time zone, the locale and
// the user's runtime directory, which the user's services are reached by. The
// rest belongs to whatever started the daemon (a tmux pane, an SSH session, a
// script holding a token) and stays with it.
func loginEnv(env []string) []string {
	var out []string
	for _, kv := range env {
		if k, _, _ := strings.Cut(kv, "="); loginVar(k) {
			out = append(out, kv)
		}
	}
	return out
}

// mergeEnv is base with over applied, then inject, a variable named as its
// last setting names it; names are compared as envKey has them.
func mergeEnv(base []string, over, inject map[string]string) []string {
	m := map[string][2]string{}
	var order []string
	set := func(k, v string) {
		key := envKey(k)
		if _, ok := m[key]; !ok {
			order = append(order, key)
		}
		m[key] = [2]string{k, v}
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
	for i, key := range order {
		env[i] = m[key][0] + "=" + m[key][1]
	}
	return env
}

// ended is the close of a stream whose leader exited with st: "window" if
// the opener overran its window, which ended it, else the exit.
func ended(st exitStatus, overrun *atomic.Bool) CloseMsg {
	if overrun.Load() {
		return CloseMsg{Reason: "window"}
	}
	return exitMsg(st)
}

// writer serialises frames from several goroutines.
type writer struct {
	mu sync.Mutex
	c  *Conn
}

// taken writes a taken frame, calling release under the lock first.
func (w *writer) taken(release func()) error {
	b, _ := Marshal(Ctl{Kind: "taken"})
	w.mu.Lock()
	defer w.mu.Unlock()
	release()
	return w.c.WriteFrame(Control, b)
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

// unlessClosed runs start unless the stream has been closed here, holding
// Close off until it returns: a stream closed before its process starts never
// starts it.
func (c *Conn) unlessClosed(start func() error) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return net.ErrClosed
	}
	return start()
}
