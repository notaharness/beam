//go:build unix

package stream

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"

	"golang.org/x/sys/unix"
)

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
	return watchGroup(cmd, awaitExit)
}

// watchGroup is newGroup with the leader's exit learned from watch.
func watchGroup(cmd *exec.Cmd, watch func(pid int) (syscall.WaitStatus, error)) *group {
	g := &group{cmd: cmd, exited: make(chan struct{})}
	go func() {
		ws, err := watch(cmd.Process.Pid)
		if err != nil {
			ws = g.killAndReap()
		}
		g.status = ws
		close(g.exited)
	}()
	return g
}

// killAndReap ends a group whose leader cannot be watched unreaped: the group
// gets SIGKILL while the leader still holds its id, no signal follows, and
// the leader is reaped for its status, outside the lock, since it may not
// have died yet.
func (g *group) killAndReap() syscall.WaitStatus {
	g.mu.Lock()
	if !g.reaped {
		_ = syscall.Kill(-g.cmd.Process.Pid, syscall.SIGKILL)
		g.reaped = true
	}
	g.mu.Unlock()
	_ = g.cmd.Wait()
	return g.cmd.ProcessState.Sys().(syscall.WaitStatus)
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

// exitStatus is how a process ended: its wait status.
type exitStatus = syscall.WaitStatus

// loginVar reports whether a login sets the variable named k (docs/04).
func loginVar(k string) bool { return loginVars[k] || strings.HasPrefix(k, "LC_") }

var loginVars = map[string]bool{"HOME": true, "USER": true, "LOGNAME": true, "SHELL": true, "PATH": true,
	"TERM": true, "TZ": true, "TMPDIR": true, "LANG": true, "XDG_RUNTIME_DIR": true}

// envKey is a variable's name as the system compares it: as it is.
func envKey(k string) string { return k }

// kill ends the group at once.
func (g *group) kill() { g.signal(syscall.SIGKILL) }

// startGroup starts cmd in a process group of its own, with pipes for its
// standard streams.
func startGroup(cmd *exec.Cmd) (g *group, stdin io.WriteCloser, stdout, stderr io.Reader, err error) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stdin, _ = cmd.StdinPipe()
	stdout, _ = cmd.StdoutPipe()
	stderr, _ = cmd.StderrPipe()
	if err := cmd.Start(); err != nil {
		return nil, nil, nil, nil, err
	}
	return newGroup(cmd), stdin, stdout, stderr, nil
}

// login builds the login shell for h, whose argv is empty, invoked as a
// login (docs/04).
func (sp Spawn) login(h Header) (*exec.Cmd, *refusal) {
	return sp.in(h, func(env []string) (*exec.Cmd, *refusal) {
		shell := loginShell(env)
		return &exec.Cmd{Path: shell, Args: []string{"-" + filepath.Base(shell)}}, nil
	})
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
