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
	cmd, f, ref := startPTY(c, h, sp)
	if ref != nil {
		refuse(c, ref)
		return
	}
	defer f.Close()
	g := newGroup(cmd)
	w := &writer{c: c}
	_ = c.WriteLine(Response{OK: true}) // a lost opener ends feed, which hangs up the session
	output := make(chan struct{})
	go func() {
		defer close(output)
		pumpRaw(f, w)
	}()
	q := make(chan []byte, inputAhead)
	go deliver(q, f)
	killed := make(chan struct{}) // the group got its SIGKILL
	openerDone := make(chan struct{})
	go func() {
		defer close(openerDone)
		feed(c, q, ptyFrame(f))
		g.signal(syscall.SIGHUP)
		f.Close() // frees a write the session was not reading
		time.AfterFunc(killGrace, func() { g.signal(syscall.SIGKILL); close(killed) })
	}()
	<-g.exited
	select { // the terminal drains once every holder of it has closed
	case <-output:
	case <-time.After(time.Second):
	}
	select {
	case <-openerDone: // the opener left first: the group is killed before the leader is reaped
		<-killed
	default:
	}
	g.reap()
	w.finish(exitMsg(cmd.ProcessState), openerDone)
}

func startPTY(c *Conn, h Header, sp Spawn) (*exec.Cmd, *os.File, *refusal) {
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
	var f *os.File
	err := c.unlessClosed(func() (err error) {
		f, err = pty.StartWithAttrs(cmd, size, &syscall.SysProcAttr{Setsid: true, Setctty: true})
		return err
	})
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

// ptyFrame is terminal input from the opener's frames; a resize is applied
// as it arrives.
func ptyFrame(f *os.File) func(Type, []byte) ([]byte, bool) {
	return func(t Type, p []byte) ([]byte, bool) {
		var ctl Ctl
		switch {
		case t == Data:
			return p, true
		case t == Control && json.Unmarshal(p, &ctl) == nil && ctl.Kind == "resize" && validSize(ctl.Cols, ctl.Rows):
			_ = pty.Setsize(f, &pty.Winsize{Cols: uint16(ctl.Cols), Rows: uint16(ctl.Rows)}) // fails only once the session ended
		}
		return nil, false
	}
}

func validSize(cols, rows int) bool {
	return cols >= minWinSize && cols <= maxWinSize && rows >= minWinSize && rows <= maxWinSize
}
