//go:build beamtest

package cli_test

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/notaharness/beam/internal/control"
)

// held keeps the parent's ends of its daemon's pipes from their finalizers.
var held []any

// parent is this test binary standing in for the desktop app (docs/08): it
// spawns `beam daemon --exit-with-parent args...` with a stdin pipe it holds
// and a stderr pipe it never reads, prints the daemon's pid, and waits to be
// killed.
func parent(args []string) {
	d := exec.Command(os.Args[0], append([]string{"daemon", "--exit-with-parent"}, args...)...)
	stdin, err := d.StdinPipe()
	if err != nil {
		panic(err)
	}
	stderr, err := d.StderrPipe()
	if err != nil {
		panic(err)
	}
	held = append(held, stdin, stderr)
	if err := d.Start(); err != nil {
		panic(err)
	}
	fmt.Println(d.Process.Pid)
	select {}
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
// kernel closes its end of the daemon's stdin, and the daemon shuts down
// cleanly, removing its socket.
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
	t.Cleanup(func() {
		if !ended(pid) {
			syscall.Kill(pid, syscall.SIGKILL)
		}
	})
	waitFor(t, 10*time.Second, "the daemon's socket", func() bool { return answering(m) })
	p.Process.Kill()
	p.Wait()
	waitFor(t, 10*time.Second, "the daemon to exit", func() bool { return ended(pid) })
	if _, err := os.Stat(m.paths().Socket); !os.IsNotExist(err) {
		t.Errorf("the socket file is left behind: the daemon did not shut down cleanly (%v)", err)
	}
}

// docs/07 The daemon: a parent gone takes its end of the daemon's stderr
// too; the daemon still shuts down and exits 0, its last log line dropped.
func TestExitWithParentStderrGone(t *testing.T) {
	m := blank(t, "fresh")
	d := exec.Command(os.Args[0], "daemon", "--exit-with-parent", "--derp-map", relay.MapURL)
	d.Env = append(os.Environ(), "BEAM_CONFIG_DIR="+m.dir)
	stdin, _ := d.StdinPipe()
	stderr, _ := d.StderrPipe()
	if err := d.Start(); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 10*time.Second, "the daemon's socket", func() bool { return answering(m) })
	stderr.Close()
	stdin.Close()
	exited := make(chan error, 1)
	go func() { exited <- d.Wait() }()
	select {
	case err := <-exited:
		if err != nil {
			t.Errorf("the daemon ended %v, want exit 0", err)
		}
	case <-time.After(10 * time.Second):
		d.Process.Kill()
		t.Fatal("the daemon outlived its stdin")
	}
}

// docs/07 The daemon: --exit-with-parent needs a stdin the parent holds, and
// does not go with --detach.
func TestExitWithParentUsage(t *testing.T) {
	m := blank(t, "fresh")
	devNull, _ := os.Open(os.DevNull)
	defer devNull.Close()
	file, err := os.CreateTemp(t.TempDir(), "stdin")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	r, w, _ := os.Pipe()
	defer r.Close()
	defer w.Close()
	for name, tc := range map[string]struct {
		stdin *os.File
		args  []string
	}{
		"/dev/null":   {devNull, nil},
		"a file":      {file, nil},
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
