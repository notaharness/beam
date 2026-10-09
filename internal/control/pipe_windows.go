package control

import (
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"sync"
	"time"

	"golang.org/x/sys/windows"
)

// The control socket on Windows is a local named pipe in byte mode (docs/06),
// its handles overlapped.
const (
	pipeBuffer  = 64 << 10
	pipeBusyFor = 5 * time.Second // the longest a dial waits for a free instance
)

// lock holds an exclusive lock on the file at path while the daemon runs.
func lock(path string) (func(), error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	err = windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY,
		0, 1, 0, new(windows.Overlapped))
	if err != nil {
		f.Close()
		return nil, ErrLocked
	}
	return func() { f.Close() }, nil
}

// listen serves the pipe at path, its every instance this user's alone and
// refusing remote clients. The first instance must be the pipe's first: a
// name someone already holds is not taken over, another user's least of all.
func listen(path string) (net.Listener, error) {
	sa, err := userOnly("")
	if err != nil {
		return nil, err
	}
	l := &pipeListener{path: path, sa: sa}
	l.next, err = l.instance(windows.FILE_FLAG_FIRST_PIPE_INSTANCE)
	if errors.Is(err, windows.ERROR_ACCESS_DENIED) {
		c, derr := dial(path)
		if derr != nil {
			return nil, derr
		}
		c.Close()
		return nil, fmt.Errorf("%s: another daemon is listening", path)
	}
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if l.event, err = windows.CreateEvent(nil, 1, 0, nil); err != nil {
		_ = windows.CloseHandle(l.next) // the event's error is the one to report
		return nil, err
	}
	return l, nil
}

// pipeListener accepts on one instance of the pipe at a time, and makes the
// next before it hands a connected one over: an instance of the pipe exists
// for as long as the listener does, so its name stays this user's.
type pipeListener struct {
	path  string
	sa    *windows.SecurityAttributes
	event windows.Handle // signals the pending connect

	mu     sync.Mutex
	next   windows.Handle     // the instance a client connects to next
	ov     windows.Overlapped // its pending connect
	busy   bool               // an Accept waits on it
	closed bool
}

func (l *pipeListener) instance(first uint32) (windows.Handle, error) {
	name, err := windows.UTF16PtrFromString(l.path)
	if err != nil {
		return 0, err
	}
	return windows.CreateNamedPipe(name, windows.PIPE_ACCESS_DUPLEX|windows.FILE_FLAG_OVERLAPPED|first,
		windows.PIPE_TYPE_BYTE|windows.PIPE_READMODE_BYTE|windows.PIPE_WAIT|windows.PIPE_REJECT_REMOTE_CLIENTS,
		windows.PIPE_UNLIMITED_INSTANCES, pipeBuffer, pipeBuffer, 0, l.sa)
}

// Accept waits for a client on the pending instance.
func (l *pipeListener) Accept() (net.Conn, error) {
	for {
		h, err := l.connect()
		if err != nil {
			return nil, err
		}
		if h != 0 {
			return newPipeConn(h, l.path)
		}
	}
}

// connect returns the instance a client connected to, with the next one
// pending in its place; 0 for a client that left before it was seen.
func (l *pipeListener) connect() (windows.Handle, error) {
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		return 0, net.ErrClosed
	}
	h := l.next
	l.ov = windows.Overlapped{HEvent: l.event}
	err := windows.ConnectNamedPipe(h, &l.ov)
	l.busy = true
	l.mu.Unlock()
	if errors.Is(err, windows.ERROR_IO_PENDING) {
		var n uint32
		err = windows.GetOverlappedResult(h, &l.ov, &n, true) // Close cancels it
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.busy = false
	switch {
	case l.closed:
		return 0, errors.Join(net.ErrClosed, windows.CloseHandle(h), windows.CloseHandle(l.event))
	case errors.Is(err, windows.ERROR_NO_DATA): // connected and gone again
		return 0, windows.DisconnectNamedPipe(h)
	case err != nil && !errors.Is(err, windows.ERROR_PIPE_CONNECTED):
		return 0, err
	}
	next, err := l.instance(0)
	if err != nil {
		return 0, errors.Join(err, windows.CloseHandle(h))
	}
	l.next = next
	return h, nil
}

// Close stops listening: a pending connect is cancelled, and the waiting
// Accept closes the instance and the event; without one, Close does.
func (l *pipeListener) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return nil
	}
	l.closed = true
	if l.busy {
		return windows.CancelIoEx(l.next, &l.ov)
	}
	return errors.Join(windows.CloseHandle(l.next), windows.CloseHandle(l.event))
}

func (l *pipeListener) Addr() net.Addr { return pipeAddr(l.path) }

type pipeAddr string

func (pipeAddr) Network() string  { return "pipe" }
func (a pipeAddr) String() string { return string(a) }

// openSocket connects to the pipe at path, waiting while every instance is
// taken. The pipe's server may identify this process but not act as it.
func openSocket(path string) (net.Conn, error) {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	for deadline := time.Now().Add(pipeBusyFor); ; time.Sleep(10 * time.Millisecond) {
		h, err := windows.CreateFile(name, windows.GENERIC_READ|windows.GENERIC_WRITE, 0, nil, windows.OPEN_EXISTING,
			windows.FILE_FLAG_OVERLAPPED|windows.SECURITY_SQOS_PRESENT|windows.SECURITY_IDENTIFICATION, 0)
		switch {
		case err == nil:
			return newPipeConn(h, path)
		case !errors.Is(err, windows.ERROR_PIPE_BUSY) || time.Now().After(deadline):
			return nil, &fs.PathError{Op: "open", Path: path, Err: err}
		}
	}
}

// dial connects to the pipe at path if this user owns it.
func dial(path string) (net.Conn, error) {
	c, err := openSocket(path)
	if err != nil {
		return nil, err
	}
	sid, err := userSID()
	if err == nil {
		err = listenedBy(c.(*pipeConn), sid)
	}
	if err != nil {
		c.Close()
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return c, nil
}

// listenedBy refuses a pipe whose owner is another user than sid (docs/06):
// whoever made a pipe of that name first is not this user's daemon. It asks
// the kernel for the owner of the pipe this connection is to, so no pipe made
// after a check counts.
func listenedBy(c *pipeConn, sid *windows.SID) error {
	sd, err := windows.GetSecurityInfo(c.h, windows.SE_KERNEL_OBJECT, windows.OWNER_SECURITY_INFORMATION)
	if err != nil {
		return err
	}
	owner, _, err := sd.Owner()
	if err != nil {
		return err
	}
	if !owner.Equals(sid) {
		return fmt.Errorf("listened on by another user (%s): %w", owner, fs.ErrPermission)
	}
	return nil
}
