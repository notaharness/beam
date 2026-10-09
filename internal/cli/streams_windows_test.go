//go:build beamtest

package cli_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/notaharness/beam/internal/control"
	"github.com/notaharness/beam/internal/stream"
)

// childArgv is this binary as a peer's process (child_test.go).
func childArgv(verb string, args ...string) []string {
	return append([]string{os.Args[0], "child", verb}, args...)
}

// docs/04: a Windows acceptor serves no pty: connect is refused kind.
func TestPTYRefusedOnWindows(t *testing.T) {
	ms := fleet(t, "alpha", "beta")
	a, b := ms[0], ms[1]
	waitState(t, a, b, "connected")
	if r := a.beam("", "connect", "beta"); r.code != 1 || !strings.Contains(r.err, "kind: pty is not supported on windows") {
		t.Errorf("connect: %+v, want exit 1 and the refusal", r)
	}
}

// docs/10 "exec, pty" on Windows: argv as sent, stdin, stdout and stderr,
// the exit status, cwd, and the environment: a logon's part of the daemon's,
// the opener's over it whatever the case of its names, the injected last.
func TestExecOnWindows(t *testing.T) {
	t.Setenv("BEAM_TEST_PRIVATE", "the daemon's alone")
	ms := fleet(t, "alpha", "beta")
	a, b := ms[0], ms[1]
	waitState(t, a, b, "connected")
	exec := func(stdin string, args ...string) result {
		t.Helper()
		return a.beam(stdin, append([]string{"exec", "beta"}, args...)...)
	}
	args := []string{"two words", `"quoted"`, `back\slash\`, `trailing\\`, "", "tab\tand ünïcode", `a\"b`}
	if r := exec("", append([]string{"--"}, childArgv("argv", args...)...)...); r.code != 0 || !argvIs(r.out, args) {
		t.Errorf("argv: %+v, want %q", r, args)
	}
	if r := exec("one\ntwo\n", append([]string{"--"}, childArgv("cat")...)...); r.code != 0 || r.out != "one\ntwo\n" {
		t.Errorf("stdin: %+v", r)
	}
	if r := exec("", append([]string{"--"}, childArgv("stderr", "on stderr")...)...); r.code != 0 || r.out != "" || !strings.HasPrefix(r.err, "on stderr") { // a covered binary adds its warning
		t.Errorf("stderr: %+v", r)
	}
	if r := exec("", append([]string{"--"}, childArgv("exit", "7")...)...); r.code != 7 {
		t.Errorf("exit: %+v, want 7", r)
	}
	home, _ := os.UserHomeDir()
	dir := t.TempDir()
	for cwd, want := range map[string]string{"~": home, dir: dir} {
		if r := exec("", append([]string{"--cwd", cwd, "--"}, childArgv("cwd")...)...); r.code != 0 || !strings.EqualFold(strings.TrimSpace(r.out), want) {
			t.Errorf("cwd %s: %+v, want %s", cwd, r, want)
		}
	}
	r := exec("", append([]string{"--env", "OPENER=set", "--env", "path=C:\\opener", "--"},
		childArgv("env", "OPENER", "PATH", "BEAM_TEST_PRIVATE", "BEAM_CALLER_LABEL", "BEAM_PEER_ID")...)...)
	want := "OPENER=set\nPATH=C:\\opener\nBEAM_TEST_PRIVATE=(unset)\nBEAM_CALLER_LABEL=alpha\nBEAM_PEER_ID=" + b.id() + "\n"
	if r.code != 0 || r.out != want {
		t.Errorf("env: %+v, want %q", r, want)
	}
	if r := exec("", append([]string{"--"}, childArgv("env", "SystemRoot")...)...); r.code != 0 || r.out == "SystemRoot=(unset)\n" {
		t.Errorf("a logon's SystemRoot: %+v", r)
	}
}

func argvIs(out string, want []string) bool {
	var got []string
	return json.Unmarshal([]byte(out), &got) == nil && reflect.DeepEqual(got, want)
}

// docs/04 exec on Windows: a batch file is refused before anything runs, as
// cmd.exe would not take its argv as sent, however its name is spelled;
// cmd.exe itself runs.
func TestExecBatchRefused(t *testing.T) {
	ms := fleet(t, "alpha", "beta")
	a, b := ms[0], ms[1]
	waitState(t, a, b, "connected")
	dir := t.TempDir()
	for _, name := range []string{"run.cmd", "run.BAT"} {
		os.WriteFile(filepath.Join(dir, name), []byte("@echo ran\r\n"), 0o600)
	}
	// CreateProcess drops the dots and spaces a name trails: run.cmd. is run.cmd.
	for _, name := range []string{"run.cmd", "run.BAT", "run.cmd.", "run.cmd ", "run.cmd. .", "run.BAT..."} {
		r := a.beam("", "exec", "beta", "--", filepath.Join(dir, name))
		if r.code != 1 || !strings.Contains(r.err, "spawn: a batch file") || strings.Contains(r.out, "ran") {
			t.Errorf("%q: %+v, want spawn", name, r)
		}
	}
	if r := a.beam("", "exec", "beta", "--", "cmd.exe", "/d", "/c", "echo ran"); r.code != 0 || strings.TrimSpace(r.out) != "ran" {
		t.Errorf("cmd.exe: %+v", r)
	}
}

// docs/04 exec on Windows: a process inherits its standard streams and no
// other handle of the daemon's, inheritable or not. The child verb sets an
// event it inherits, as one started with the handle does.
func TestExecInheritsStdioOnly(t *testing.T) {
	ms := fleet(t, "alpha", "beta")
	a, b := ms[0], ms[1]
	waitState(t, a, b, "connected")
	sa := &windows.SecurityAttributes{InheritHandle: 1}
	sa.Length = uint32(unsafe.Sizeof(*sa))
	ev, err := windows.CreateEvent(sa, 1, 0, nil) // manual reset: it stays set
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(ev)
	argv := childArgv("signal", strconv.FormatUint(uint64(ev), 10))
	given := exec.Command(argv[0], argv[1:]...)
	given.SysProcAttr = &syscall.SysProcAttr{AdditionalInheritedHandles: []syscall.Handle{syscall.Handle(ev)}}
	if out, err := given.Output(); err != nil || len(out) != 0 || !set(ev) {
		t.Fatalf("a child given the handle: %q, %v, set %v", out, err, set(ev))
	}
	windows.ResetEvent(ev)
	if r := a.beam("", append([]string{"exec", "beta", "--"}, argv...)...); r.code != 0 || set(ev) {
		t.Errorf("exec: %+v, set %v; want the event unset", r, set(ev))
	}
}

func set(ev windows.Handle) bool {
	s, err := windows.WaitForSingleObject(ev, 0)
	return err == nil && s == windows.WAIT_OBJECT_0
}

// docs/04 exec on Windows: no process leaves its job, so none outlives the
// stream.
func TestExecNoBreakaway(t *testing.T) {
	ms := fleet(t, "alpha", "beta")
	a, b := ms[0], ms[1]
	waitState(t, a, b, "connected")
	if r := a.beam("", append([]string{"exec", "beta", "--"}, childArgv("breakaway")...)...); r.code != 0 || r.out != "refused\n" {
		t.Errorf("breakaway: %+v, want refused", r)
	}
}

// docs/04 exec on Windows: the opener's departure ends the process and what
// it started, which its job holds.
func TestExecJobEndsWithStream(t *testing.T) {
	ms := fleet(t, "alpha", "beta")
	a, b := ms[0], ms[1]
	waitState(t, a, b, "connected")
	file := filepath.Join(t.TempDir(), "pid")
	ac, _ := openExec(t, a, "beta", childArgv("spawn", file)...)
	grandchild := running(t, file)
	ac.Close()
	waitGone(t, grandchild)
}

// docs/04, D30 on Windows: what a leader started outlives the leader's exit,
// and the opener's end still tears it down.
func TestExecLeaderExitsFirst(t *testing.T) {
	b := fleet(t, "beta")[0]
	tun, _ := rawTunnel(t, b)
	file := filepath.Join(t.TempDir(), "pid")
	s := rawOpen(t, tun, stream.Header{V: 1, Kind: stream.KindExec, Argv: childArgv("spawn-exit", file)})
	if msg := readClose(t, s); msg.Reason != "exit" || msg.ExitCode == nil || *msg.ExitCode != 0 {
		t.Fatalf("close %+v, want exit 0", msg)
	}
	grandchild := running(t, file)
	s.Close()
	waitGone(t, grandchild)
}

// running waits for the pid a peer's process wrote to file, and fails the
// test unless that process runs.
func running(t *testing.T, file string) int {
	t.Helper()
	pid := waitPid(t, file)
	time.Sleep(200 * time.Millisecond)
	if ended(pid) {
		t.Fatalf("process %d is not running", pid)
	}
	return pid
}

// docs/10 "shutdown ends sessions" on Windows: a daemon in a process of its
// own ends the exec trees it serves when it shuts down, and when it is
// killed outright, as the last handle to each job goes with it.
func TestDaemonEndEndsExec(t *testing.T) {
	for _, how := range []string{"shutdown", "killed"} {
		t.Run(how, func(t *testing.T) {
			a, b, d, exited := pair(t)
			file := filepath.Join(t.TempDir(), "pid")
			openExec(t, a, "beta", childArgv("spawn", file)...)
			grandchild := running(t, file)
			if how == "shutdown" {
				c, err := control.Dial(b.paths())
				if err != nil {
					t.Fatal(err)
				}
				c.Call("daemon.shutdown", nil, nil)
				c.Close()
			} else {
				d.Process.Kill()
			}
			await(t, exited, "the daemon to exit")
			waitGone(t, grandchild)
		})
	}
}

// docs/04 exec on Windows: a process the opener leaves in place, whose
// input it never reads, still ends at the opener's close.
func TestExecInputNeverRead(t *testing.T) {
	ms := fleet(t, "alpha", "beta")
	a, b := ms[0], ms[1]
	waitState(t, a, b, "connected")
	file := filepath.Join(t.TempDir(), "pid")
	ac, _ := openExec(t, a, "beta", childArgv("spawn", file)...)
	grandchild := running(t, file)
	ac.SetWriteDeadline(time.Now().Add(5 * time.Second))
	for range stream.InputWindow {
		if err := ac.WriteFrame(stream.Data, append([]byte{0}, make([]byte, 64<<10)...)); err != nil {
			t.Fatalf("input not taken: %v", err)
		}
	}
	ac.Close()
	waitGone(t, grandchild)
}
