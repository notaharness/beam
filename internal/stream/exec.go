package stream

import (
	"encoding/json"
	"io"
	"os/exec"
	"sync"
	"syscall"
)

// Exec channel bytes (docs/04).
const (
	chanStdin  = 0
	chanStdout = 1
	chanStderr = 2
)

// Exec serves an exec stream on the acceptor: it answers h, runs argv in its
// own process group, and pumps stdin, stdout and stderr as channel-tagged data
// frames until the process exits (close "exit" once output drains) or the
// opener goes away (its input is closed and the group killed).
func Exec(c *Conn, h Header, sp Spawn) {
	cmd, stdin, stdout, stderr, ref := startExec(h, sp)
	if ref != nil {
		refuse(c, ref)
		return
	}
	g := newGroup(cmd)
	w := &writer{c: c}
	_ = c.WriteLine(Response{OK: true}) // a lost opener ends feed, which kills the group
	var drained sync.WaitGroup
	drained.Go(func() { pumpOutput(stdout, chanStdout, w) })
	drained.Go(func() { pumpOutput(stderr, chanStderr, w) })
	q := make(chan []byte, inputAhead)
	go deliver(q, stdin)
	openerDone := make(chan struct{})
	go func() {
		defer close(openerDone)
		feed(c, q, execFrame)
		_ = stdin.Close() // frees a write the process was not reading
		g.signal(syscall.SIGKILL)
	}()
	drained.Wait()
	g.reap()
	w.finish(exitMsg(cmd.ProcessState), openerDone)
}

func startExec(h Header, sp Spawn) (*exec.Cmd, io.WriteCloser, io.Reader, io.Reader, *refusal) {
	if ref := checkHeader(h); ref != nil {
		return nil, nil, nil, nil, ref
	}
	if len(h.Argv) == 0 {
		return nil, nil, nil, nil, &refusal{"params", "argv is required"}
	}
	cmd, ref := sp.command(h)
	if ref != nil {
		return nil, nil, nil, nil, ref
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stdin, _ := cmd.StdinPipe()
	stdout, _ := cmd.StdoutPipe()
	stderr, _ := cmd.StderrPipe()
	if err := cmd.Start(); err != nil {
		return nil, nil, nil, nil, &refusal{"spawn", err.Error()}
	}
	return cmd, stdin, stdout, stderr, nil
}

func pumpOutput(r io.Reader, ch byte, w *writer) {
	buf := make([]byte, 32<<10)
	for {
		n, err := r.Read(buf[1:])
		if n > 0 {
			buf[0] = ch
			_ = w.frame(Data, buf[:1+n]) // drain on regardless until the group dies
		}
		if err != nil {
			return
		}
	}
}

// execFrame is stdin from the opener's frames: channel 0 data, and nil for
// stdin-eof.
func execFrame(t Type, p []byte) ([]byte, bool) {
	switch {
	case t == Data && len(p) > 0 && p[0] == chanStdin:
		return p[1:], true
	case t == Control:
		var ctl Ctl
		return nil, json.Unmarshal(p, &ctl) == nil && ctl.Kind == "stdin-eof"
	}
	return nil, false
}
