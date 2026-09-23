//go:build beamtest

package cli_test

import (
	"bufio"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/creack/pty"
	"github.com/notaharness/beam/internal/control"
)

// parent is this test binary standing in for the desktop app (docs/08): it
// spawns `beam daemon --exit-with-parent args...` with a stdin pipe it holds,
// prints the daemon's pid, and waits to be killed.
func parent(args []string) {
	d := exec.Command(os.Args[0], append([]string{"daemon", "--exit-with-parent"}, args...)...)
	d.Stderr = os.Stderr
	if _, err := d.StdinPipe(); err != nil {
		panic(err)
	}
	if err := d.Start(); err != nil {
		panic(err)
	}
	fmt.Println(d.Process.Pid)
	select {}
}

// answering reports whether m's daemon answers on its socket.
func answering(m *machine) bool {
	c, err := net.Dial("unix", m.paths().Socket)
	if err == nil {
		c.Close()
	}
	return err == nil
}

// docs/07 The daemon: a daemon started with --exit-with-parent shuts down
// when its stdin ends, here closed by its parent in this process.
func TestExitWithParentStdinEnds(t *testing.T) {
	m := blank(t, "fresh")
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	exited := make(chan int, 1)
	go func() {
		exited <- m.run(r, os.Stderr, os.Stderr, "daemon", "--exit-with-parent", "--derp-map", relay.MapURL)
	}()
	waitFor(t, 10*time.Second, "the daemon's socket", func() bool { return answering(m) })
	fmt.Fprintln(w, "ignored") // what the parent writes is discarded
	time.Sleep(200 * time.Millisecond)
	if !answering(m) {
		t.Fatal("the daemon stopped on input rather than at its end")
	}
	w.Close()
	select {
	case code := <-exited:
		if code != 0 {
			t.Errorf("exit %d, want 0", code)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the daemon outlived its stdin")
	}
	if answering(m) {
		t.Error("the socket still answers")
	}
}

// docs/10 "exit with parent": the daemon's parent is killed outright; the
// kernel closes its end of the daemon's stdin, and the daemon shuts down,
// leaving the lock free for the next.
func TestExitWithParentKilled(t *testing.T) {
	m := blank(t, "fresh")
	p := exec.Command(os.Args[0], "parent", "--derp-map", relay.MapURL)
	p.Env = append(os.Environ(), "BEAM_CONFIG_DIR="+m.dir)
	out, _ := p.StdoutPipe()
	p.Stderr = os.Stderr
	if err := p.Start(); err != nil {
		t.Fatal(err)
	}
	defer p.Process.Kill()
	line, err := bufio.NewReader(out).ReadString('\n')
	pid, perr := strconv.Atoi(strings.TrimSpace(line))
	if err != nil || perr != nil {
		t.Fatalf("the parent printed %q: %v %v", line, err, perr)
	}
	defer syscall.Kill(pid, syscall.SIGKILL)
	waitFor(t, 10*time.Second, "the daemon's socket", func() bool { return answering(m) })
	p.Process.Kill()
	p.Wait()
	waitFor(t, 10*time.Second, "the daemon to exit", func() bool {
		return errors.Is(syscall.Kill(pid, 0), syscall.ESRCH)
	})
	if answering(m) {
		t.Error("the socket still answers")
	}
	m.start(t) // the lock is free
}

// docs/07 The daemon: --exit-with-parent needs a stdin the parent holds, and
// does not go with --detach.
func TestExitWithParentUsage(t *testing.T) {
	m := blank(t, "fresh")
	devNull, _ := os.Open(os.DevNull)
	defer devNull.Close()
	ptmx, tty, err := pty.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer ptmx.Close()
	defer tty.Close()
	r, w, _ := os.Pipe()
	defer r.Close()
	defer w.Close()
	for name, tc := range map[string]struct {
		stdin *os.File
		args  []string
	}{
		"/dev/null":   {devNull, nil},
		"a terminal":  {tty, nil},
		"with detach": {r, []string{"--detach"}},
	} {
		var errb strings.Builder
		args := append([]string{"daemon", "--exit-with-parent", "--derp-map", relay.MapURL}, tc.args...)
		exited := make(chan int, 1)
		go func() { exited <- m.run(tc.stdin, os.Stderr, &errb, args...) }()
		select {
		case code := <-exited:
			if code != 2 || !strings.Contains(errb.String(), "--exit-with-parent") {
				t.Errorf("%s: exit %d, %q", name, code, errb.String())
			}
		case <-time.After(3 * time.Second):
			t.Errorf("%s: the daemon started", name)
			if c, err := control.Dial(m.paths()); err == nil {
				c.Call("daemon.shutdown", nil, nil)
				c.Close()
			}
			<-exited
		}
	}
}
