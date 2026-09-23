package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"

	"github.com/notaharness/beam/internal/control"
	"github.com/notaharness/beam/internal/stream"
	"golang.org/x/term"
)

// envFlags collects repeated --env K=V.
type envFlags map[string]string

func (f envFlags) String() string { return "" }

func (f envFlags) Set(kv string) error {
	k, v, ok := strings.Cut(kv, "=")
	if !ok || k == "" {
		return fmt.Errorf("--env wants K=V, got %q", kv)
	}
	f[k] = v
	return nil
}

// streamArgs parses `<peer> [flags] [-- argv...]`.
func (e *env) streamArgs(name string, withEnv bool) (peer, cwd string, env envFlags, argv []string, ok bool) {
	if len(e.args) == 0 || strings.HasPrefix(e.args[0], "-") {
		return "", "", nil, nil, false
	}
	fs := e.flags(name)
	fs.StringVar(&cwd, "cwd", "", "")
	env = envFlags{}
	if withEnv {
		fs.Var(env, "env", "")
	}
	if fs.Parse(e.args[1:]) != nil {
		return "", "", nil, nil, false
	}
	return e.args[0], cwd, env, fs.Args(), true
}

// frames is an attach connection with serialised writes.
type frames struct {
	mu sync.Mutex
	c  *stream.Conn
}

func (f *frames) write(t stream.Type, p []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.c.WriteFrame(t, p)
}

func (f *frames) control(ctl stream.Ctl) error {
	b, _ := json.Marshal(ctl)
	return f.write(stream.Control, b)
}

// open reserves a stream with op and attaches to it.
func (e *env) open(op string, params map[string]any) (*frames, error) {
	var res struct {
		StreamID string `json:"streamId"`
	}
	if err := e.call(op, params, &res); err != nil {
		return nil, err
	}
	p, err := e.paths()
	if err != nil {
		return nil, err
	}
	c, err := control.Attach(p, res.StreamID)
	if err != nil {
		return nil, err
	}
	return &frames{c: c}, nil
}

// runExec is `beam exec`: stdin to the remote process, its stdout and stderr
// here, and its exit status as ours.
func runExec(e *env) int {
	peer, cwd, env, argv, ok := e.streamArgs("exec", true)
	if !ok || len(argv) == 0 {
		return e.fail(errUsage)
	}
	f, err := e.open("exec.open", map[string]any{"peer": peer, "argv": argv, "cwd": cwd, "env": env})
	if err != nil {
		return e.fail(err)
	}
	defer f.c.Close()
	go func() {
		pumpStdin(e.stdin, f, []byte{0})
		_ = f.control(stream.Ctl{Kind: "stdin-eof"}) // a lost stream shows in relay
	}()
	return e.relay(f, false, func(p []byte) {
		switch {
		case len(p) == 0:
		case p[0] == 1:
			_, _ = e.stdout.Write(p[1:]) // nowhere to report it
		case p[0] == 2:
			_, _ = e.stderr.Write(p[1:]) // nowhere to report it
		}
	})
}

// relay hands the remote side's data frames to out until the stream closes,
// and turns the close into an exit code.
func (e *env) relay(f *frames, shell bool, out func([]byte)) int {
	for {
		t, p, err := f.c.ReadFrame()
		switch {
		case err != nil:
			return e.closed(stream.CloseMsg{Reason: "connection-lost"}, shell)
		case t == stream.Data:
			out(p)
		case t == stream.Close:
			return e.closed(stream.ParseClose(p), shell)
		}
	}
}

// pumpStdin sends stdin as data frames, each payload after prefix.
func pumpStdin(r io.Reader, f *frames, prefix []byte) {
	buf := make([]byte, 32<<10)
	for {
		n, err := r.Read(buf)
		if n > 0 && f.write(stream.Data, append(append([]byte{}, prefix...), buf[:n]...)) != nil {
			return
		}
		if err != nil {
			return
		}
	}
}

// closed turns a close frame into an exit code, saying what happened on
// stderr: for a shell always, for exec only when it was not an exit.
func (e *env) closed(msg stream.CloseMsg, shell bool) int {
	switch {
	case msg.Reason == "exit" && msg.Signal != nil:
		fmt.Fprintf(e.stderr, "killed by %s\n", *msg.Signal)
		return *msg.ExitCode
	case msg.Reason == "exit":
		if shell {
			fmt.Fprintf(e.stderr, "remote shell exited (%d)\n", *msg.ExitCode)
		}
		return *msg.ExitCode
	case msg.Reason == "connection-lost":
		fmt.Fprintln(e.stderr, "connection lost")
	case msg.Detail != "":
		fmt.Fprintf(e.stderr, "%s: %s\n", msg.Reason, msg.Detail)
	default:
		fmt.Fprintln(e.stderr, msg.Reason)
	}
	return 1
}

// runConnect is `beam connect`: a remote terminal, raw when ours is one.
func runConnect(e *env) int {
	peer, cwd, _, argv, ok := e.streamArgs("connect", false)
	if !ok {
		return e.fail(errUsage)
	}
	cols, rows := 80, 24
	tty, isTTY := e.stdin.(*os.File)
	isTTY = isTTY && term.IsTerminal(int(tty.Fd()))
	if isTTY {
		if w, h, err := term.GetSize(int(tty.Fd())); err == nil {
			cols, rows = w, h
		}
	}
	fmt.Fprintf(e.stderr, "connecting to %s…\n", peer)
	f, err := e.open("pty.open", map[string]any{"peer": peer, "argv": argv, "cwd": cwd, "cols": cols, "rows": rows})
	if err != nil {
		return e.fail(err)
	}
	defer f.c.Close()
	if isTTY {
		if old, err := term.MakeRaw(int(tty.Fd())); err == nil {
			defer func() { _ = term.Restore(int(tty.Fd()), old) }() // nowhere to report it
		}
		stop := forwardResize(tty, f)
		defer stop()
	}
	go pumpStdin(e.stdin, f, nil)
	return e.relay(f, true, func(p []byte) {
		_, _ = e.stdout.Write(p) // nowhere to report it
	})
}

// forwardResize sends the terminal's size on every SIGWINCH.
func forwardResize(tty *os.File, f *frames) func() {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGWINCH)
	go func() {
		for range ch {
			if w, h, err := term.GetSize(int(tty.Fd())); err == nil {
				_ = f.control(stream.Ctl{Kind: "resize", Cols: w, Rows: h}) // a lost stream shows in relay
			}
		}
	}()
	return func() { signal.Stop(ch); close(ch) }
}
