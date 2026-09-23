package stream

import (
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"syscall"
	"time"

	"github.com/creack/pty"
)

// PTY serves a pty stream on the acceptor: it answers h, runs argv (or the
// login shell) on a new terminal in its own session, and pumps raw bytes both
// ways. The opener resizes with control frames. When the process exits the
// acceptor sends close "exit"; when the opener closes or goes away the session
// gets SIGHUP, then SIGKILL after 5 s.
func PTY(c *Conn, h Header, sp Spawn) {
	cmd, f, ref := startPTY(h, sp)
	if ref != nil {
		refuse(c, ref)
		return
	}
	defer f.Close()
	g := &group{pid: cmd.Process.Pid}
	w := &writer{c: c}
	_ = c.WriteLine(Response{OK: true}) // a lost opener ends ptyInput, which hangs up the session
	output := make(chan struct{})
	go func() {
		defer close(output)
		pumpRaw(f, w)
	}()
	openerDone := make(chan struct{})
	go func() {
		defer close(openerDone)
		ptyInput(c, f)
		g.signal(syscall.SIGHUP)
		time.AfterFunc(killGrace, func() { g.signal(syscall.SIGKILL) })
	}()
	g.wait(cmd)
	select { // the terminal drains once every holder of it has closed
	case <-output:
	case <-time.After(time.Second):
	}
	w.finish(exitMsg(cmd.ProcessState), openerDone)
}

func startPTY(h Header, sp Spawn) (*exec.Cmd, *os.File, *refusal) {
	if ref := checkHeader(h); ref != nil {
		return nil, nil, ref
	}
	if !validSize(h.Cols, h.Rows) {
		return nil, nil, &refusal{"params", "cols and rows must be 2–500"}
	}
	cmd, ref := sp.command(h)
	if ref != nil {
		return nil, nil, ref
	}
	size := &pty.Winsize{Cols: uint16(h.Cols), Rows: uint16(h.Rows)}
	f, err := pty.StartWithAttrs(cmd, size, &syscall.SysProcAttr{Setsid: true, Setctty: true})
	if err != nil {
		return nil, nil, &refusal{"spawn", err.Error()}
	}
	return cmd, f, nil
}

func pumpRaw(r io.Reader, w *writer) {
	buf := make([]byte, 32<<10)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			_ = w.frame(Data, buf[:n]) // drain on regardless until the session ends
		}
		if err != nil {
			return
		}
	}
}

func ptyInput(c *Conn, f *os.File) {
	for {
		t, p, err := c.ReadFrame()
		if err != nil || t == Close {
			return
		}
		switch t {
		case Data:
			_, _ = f.Write(p) // a session that stopped reading drops its input
		case Control:
			var ctl Ctl
			if json.Unmarshal(p, &ctl) == nil && ctl.Kind == "resize" && validSize(ctl.Cols, ctl.Rows) {
				_ = pty.Setsize(f, &pty.Winsize{Cols: uint16(ctl.Cols), Rows: uint16(ctl.Rows)}) // fails only once the session ended
			}
		}
	}
}

func validSize(cols, rows int) bool {
	return cols >= minWinSize && cols <= maxWinSize && rows >= minWinSize && rows <= maxWinSize
}
