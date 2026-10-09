package stream

import (
	"errors"
	"io"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

// exitStatus is how a process ended: its exit code.
type exitStatus = uint32

// killedCode is the exit code of a process its group's end killed.
const killedCode = 1

// exitMsg is the close frame for a finished process.
func exitMsg(code exitStatus) CloseMsg {
	n := int(code)
	return CloseMsg{Reason: "exit", ExitCode: &n}
}

// loginVar reports whether a Windows logon sets the variable named k
// (docs/04): who and where the user is, the system's directories, the path
// and the temporary directory.
func loginVar(k string) bool { return loginVars[strings.ToUpper(k)] }

var loginVars = map[string]bool{"ALLUSERSPROFILE": true, "APPDATA": true, "COMMONPROGRAMFILES": true,
	"COMMONPROGRAMFILES(X86)": true, "COMMONPROGRAMW6432": true, "COMPUTERNAME": true, "COMSPEC": true,
	"HOMEDRIVE": true, "HOMEPATH": true, "LOCALAPPDATA": true, "NUMBER_OF_PROCESSORS": true, "OS": true,
	"PATH": true, "PATHEXT": true, "PROCESSOR_ARCHITECTURE": true, "PROGRAMDATA": true, "PROGRAMFILES": true,
	"PROGRAMFILES(X86)": true, "PROGRAMW6432": true, "PUBLIC": true, "SYSTEMDRIVE": true, "SYSTEMROOT": true,
	"TEMP": true, "TMP": true, "USERDOMAIN": true, "USERNAME": true, "USERPROFILE": true, "WINDIR": true}

// envKey is a variable's name as Windows compares it, ignoring case.
func envKey(k string) string { return strings.ToUpper(k) }

// group is a stream's process and every process it creates: a job object
// of its own, which the process is created in, so nothing it creates runs
// outside it, and which none of them may leave. Its end terminates the job;
// closing the job's last handle kills what is left in it, the daemon's exit
// included.
type group struct {
	job, proc windows.Handle
	output    [2]io.Closer  // the daemon's ends of stdout and stderr
	exited    chan struct{} // the process has exited
	status    exitStatus    // its exit code, once exited

	mu     sync.Mutex
	reaped bool
}

// watch makes the group of job and its process proc.
func watch(job, proc windows.Handle) *group {
	g := &group{job: job, proc: proc, exited: make(chan struct{})}
	go func() {
		ev, err := windows.WaitForSingleObject(proc, windows.INFINITE)
		if err == nil && ev == windows.WAIT_OBJECT_0 {
			err = windows.GetExitCodeProcess(proc, &g.status)
		}
		if err != nil || ev != windows.WAIT_OBJECT_0 {
			g.kill()
			g.status = killedCode
		}
		close(g.exited)
	}()
	return g
}

// kill ends every process in the group at once.
func (g *group) kill() {
	g.mu.Lock()
	defer g.mu.Unlock()
	if !g.reaped {
		_ = windows.TerminateJobObject(g.job, killedCode) // the job may be empty already
	}
}

// reap closes the group once its process has exited: the daemon's ends of
// its output, as a Unix reap does, and the job's last handle, which kills
// what is left in it.
func (g *group) reap() {
	<-g.exited
	g.mu.Lock()
	defer g.mu.Unlock()
	if !g.reaped {
		for _, c := range g.output {
			_ = c.Close() // nothing to report it to
		}
		_ = windows.CloseHandle(g.proc)
		_ = windows.CloseHandle(g.job)
		g.reaped = true
	}
}

// startGroup starts cmd in a job of its own, with pipes for its standard
// streams. A batch file is refused: cmd.exe runs it, and parses its command
// line by rules of its own, so its arguments would not arrive as sent.
func startGroup(cmd *exec.Cmd) (g *group, stdin io.WriteCloser, stdout, stderr io.Reader, err error) {
	if batch(cmd.Path) {
		return nil, nil, nil, nil, errors.New("a batch file takes no argv as sent; run cmd.exe /c with its command line")
	}
	job, err := newJob()
	if err != nil {
		return nil, nil, nil, nil, err
	}
	pipes, err := stdioPipes()
	if err != nil {
		return nil, nil, nil, nil, errors.Join(err, windows.CloseHandle(job))
	}
	proc, err := createInJob(job, cmd, pipes.child)
	_ = pipes.closeChild() // the process holds its own; one that would not close only stays open
	if err != nil {
		return nil, nil, nil, nil, errors.Join(err, pipes.closeParent(), windows.CloseHandle(job))
	}
	g = watch(job, proc)
	g.output = [2]io.Closer{pipes.stdout, pipes.stderr}
	return g, pipes.stdin, pipes.stdout, pipes.stderr, nil
}

// batch reports whether CreateProcess would run path as a batch file: by
// the extension of its full path, which, as CreateProcess takes it, has lost
// the dots and spaces a name may trail (run.bat. is run.bat).
func batch(path string) bool {
	if abs, err := filepath.Abs(path); err == nil {
		path = abs
	}
	ext := strings.ToLower(filepath.Ext(path))
	return ext == ".bat" || ext == ".cmd"
}

// newJob is a job whose last handle's close kills what is in it, and which
// no process leaves.
func newJob() (windows.Handle, error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return 0, err
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{BasicLimitInformation: windows.JOBOBJECT_BASIC_LIMIT_INFORMATION{
		LimitFlags: windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE}}
	_, err = windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info)))
	if err != nil {
		return 0, errors.Join(err, windows.CloseHandle(job))
	}
	return job, nil
}

// procThreadAttributeJobList is PROC_THREAD_ATTRIBUTE_JOB_LIST: the jobs a
// process is created in, before it runs.
const procThreadAttributeJobList = 0x2000d

// createInJob creates cmd's process in job, inheriting stdio and no other
// handle, with no console window of its own, and returns its handle.
func createInJob(job windows.Handle, cmd *exec.Cmd, stdio [3]windows.Handle) (windows.Handle, error) {
	attrs, err := windows.NewProcThreadAttributeList(2)
	if err != nil {
		return 0, err
	}
	defer attrs.Delete()
	inherit, jobs := stdio, [1]windows.Handle{job}
	if err := attrs.Update(windows.PROC_THREAD_ATTRIBUTE_HANDLE_LIST, unsafe.Pointer(&inherit[0]), unsafe.Sizeof(inherit)); err != nil {
		return 0, err
	}
	if err := attrs.Update(procThreadAttributeJobList, unsafe.Pointer(&jobs[0]), unsafe.Sizeof(jobs)); err != nil {
		return 0, err
	}
	si := &windows.StartupInfoEx{ProcThreadAttributeList: attrs.List()}
	si.Cb = uint32(unsafe.Sizeof(*si))
	si.Flags = windows.STARTF_USESTDHANDLES
	si.StdInput, si.StdOutput, si.StdErr = stdio[0], stdio[1], stdio[2]
	app, line, dir, env, err := processStrings(cmd)
	if err != nil {
		return 0, err
	}
	var pi windows.ProcessInformation
	flags := uint32(windows.CREATE_UNICODE_ENVIRONMENT | windows.EXTENDED_STARTUPINFO_PRESENT | windows.CREATE_NO_WINDOW)
	if err := windows.CreateProcess(app, line, nil, nil, true, flags, env, dir, &si.StartupInfo, &pi); err != nil {
		return 0, err
	}
	_ = windows.CloseHandle(pi.Thread) // the process holds its thread
	return pi.Process, nil
}

// processStrings are cmd's path, command line (its arguments quoted as the
// C runtime and CommandLineToArgvW parse them), directory and environment
// block, as CreateProcess takes them.
func processStrings(cmd *exec.Cmd) (app, line, dir, env *uint16, err error) {
	if app, err = windows.UTF16PtrFromString(cmd.Path); err != nil {
		return
	}
	if line, err = windows.UTF16PtrFromString(windows.ComposeCommandLine(cmd.Args)); err != nil {
		return
	}
	if dir, err = windows.UTF16PtrFromString(cmd.Dir); err != nil {
		return
	}
	var block []uint16
	for _, kv := range cmd.Env {
		u, err := windows.UTF16FromString(kv) // NUL-terminated
		if err != nil {
			return nil, nil, nil, nil, err
		}
		block = append(block, u...)
	}
	if len(block) == 0 {
		block = append(block, 0) // an empty block is two NULs
	}
	block = append(block, 0)
	return app, line, dir, &block[0], nil
}
