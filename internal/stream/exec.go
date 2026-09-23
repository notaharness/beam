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
// opener goes away (the group is killed).
func Exec(c *Conn, h Header, sp Spawn) {
	cmd, stdin, stdout, stderr, ref := startExec(h, sp)
	if ref != nil {
		refuse(c, ref)
		return
	}
	g := &group{pid: cmd.Process.Pid}
	w := &writer{c: c}
	_ = c.WriteLine(Response{OK: true}) // a lost opener ends execInput, which kills the group
	var drained sync.WaitGroup
	drained.Go(func() { pumpOutput(stdout, chanStdout, w) })
	drained.Go(func() { pumpOutput(stderr, chanStderr, w) })
	openerDone := make(chan struct{})
	go func() {
		defer close(openerDone)
		execInput(c, stdin)
		g.signal(syscall.SIGKILL)
	}()
	drained.Wait()
	g.wait(cmd)
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

// execInput feeds the opener's frames to stdin until the opener closes the
// stream, sends close, or the connection is lost.
func execInput(c *Conn, stdin io.WriteCloser) {
	defer stdin.Close()
	for {
		t, p, err := c.ReadFrame()
		if err != nil || t == Close {
			return
		}
		switch {
		case t == Data && len(p) > 0 && p[0] == chanStdin:
			_, _ = stdin.Write(p[1:]) // a process that stopped reading drops its input
		case t == Control:
			var ctl Ctl
			if json.Unmarshal(p, &ctl) == nil && ctl.Kind == "stdin-eof" {
				stdin.Close()
			}
		}
	}
}
