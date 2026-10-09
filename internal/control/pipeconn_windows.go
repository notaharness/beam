package control

import (
	"errors"
	"io"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sys/windows"
)

// pipeConn is a connected instance of the pipe, at either end. A read and a
// write can be pending at once, each overlapped on an event and a buffer of
// its own, which the kernel holds while it is pending; a deadline or Close
// cancels what is pending. (An *os.File would do this through the runtime's
// poller, but its reads and writes share an unsynchronised offset.)
type pipeConn struct {
	h      windows.Handle
	addr   pipeAddr
	r, w   *pipeIO
	closed atomic.Bool
	once   sync.Once
	life   sync.RWMutex // held to close the handles, shared to set an event
}

// pipeIO is one direction of a pipeConn: one operation at a time, under mu.
type pipeIO struct {
	mu       sync.Mutex
	ov       windows.Overlapped
	done     windows.Handle // ov's event
	kick     windows.Handle // set when the deadline changes
	deadline atomic.Int64   // in Unix nanoseconds; 0 for none
	buf      []byte
	n        uint32 // a count x/sys reads under the race detector; GetOverlappedResult's is the one used
}

func newPipeIO() (*pipeIO, error) {
	done, err := windows.CreateEvent(nil, 1, 0, nil)
	if err != nil {
		return nil, err
	}
	kick, err := windows.CreateEvent(nil, 0, 0, nil)
	if err != nil {
		return nil, errors.Join(err, windows.CloseHandle(done))
	}
	return &pipeIO{done: done, kick: kick, buf: make([]byte, pipeBuffer)}, nil
}

func (p *pipeIO) close() error {
	return errors.Join(windows.CloseHandle(p.done), windows.CloseHandle(p.kick))
}

// newPipeConn takes over h, an overlapped handle to a connected instance;
// on an error it closes h.
func newPipeConn(h windows.Handle, path string) (*pipeConn, error) {
	r, err := newPipeIO()
	if err != nil {
		return nil, errors.Join(err, windows.CloseHandle(h))
	}
	w, err := newPipeIO()
	if err != nil {
		return nil, errors.Join(err, r.close(), windows.CloseHandle(h))
	}
	return &pipeConn{h: h, addr: pipeAddr(path), r: r, w: w}, nil
}

func (c *pipeConn) Read(b []byte) (int, error) {
	c.r.mu.Lock()
	defer c.r.mu.Unlock()
	for {
		n, err := c.r.do(c, len(b), func(buf []byte, ov *windows.Overlapped) error {
			return windows.ReadFile(c.h, buf, &c.r.n, ov)
		})
		copy(b, c.r.buf[:n])
		switch {
		case errors.Is(err, windows.ERROR_BROKEN_PIPE), errors.Is(err, windows.ERROR_PIPE_NOT_CONNECTED):
			return n, io.EOF
		case n > 0 || err != nil || len(b) == 0:
			return n, err
		} // a write of nothing, at the other end
	}
}

func (c *pipeConn) Write(b []byte) (int, error) {
	c.w.mu.Lock()
	defer c.w.mu.Unlock()
	var total int
	for total < len(b) {
		n, err := c.w.do(c, len(b)-total, func(buf []byte, ov *windows.Overlapped) error {
			copy(buf, b[total:])
			return windows.WriteFile(c.h, buf, &c.w.n, ov)
		})
		total += n
		if err != nil {
			return total, err
		}
	}
	return total, nil
}

// do runs op on at most n bytes of p's buffer and waits for it, until p's
// deadline or the conn's close cancels it. p.mu must be held.
func (p *pipeIO) do(c *pipeConn, n int, op func([]byte, *windows.Overlapped) error) (int, error) {
	if c.closed.Load() {
		return 0, net.ErrClosed
	}
	if p.expired() {
		return 0, os.ErrDeadlineExceeded
	}
	p.ov = windows.Overlapped{HEvent: p.done}
	err := op(p.buf[:min(n, len(p.buf))], &p.ov)
	if c.closed.Load() { // Close cancelled what was pending before this began
		_ = windows.CancelIoEx(c.h, &p.ov) // done or cancelled, the result says
	}
	if err != nil && !errors.Is(err, windows.ERROR_IO_PENDING) {
		return 0, err
	}
	timedOut := p.wait(c.h)
	var done uint32
	err = windows.GetOverlappedResult(c.h, &p.ov, &done, true)
	switch {
	case errors.Is(err, windows.ERROR_OPERATION_ABORTED) && c.closed.Load():
		err = net.ErrClosed
	case errors.Is(err, windows.ERROR_OPERATION_ABORTED) && timedOut:
		err = os.ErrDeadlineExceeded
	}
	return int(done), err
}

// wait waits for the pending operation to complete, or cancels it at the
// deadline, which may change meanwhile; it reports whether it cancelled.
func (p *pipeIO) wait(h windows.Handle) bool {
	for {
		timeout := uint32(windows.INFINITE)
		if d := p.deadline.Load(); d != 0 {
			left := time.Until(time.Unix(0, d))
			if left <= 0 {
				_ = windows.CancelIoEx(h, &p.ov) // done or cancelled, the result says
				return true
			}
			timeout = uint32(min(left.Milliseconds()+1, windows.INFINITE-1))
		}
		ev, err := windows.WaitForMultipleObjects([]windows.Handle{p.done, p.kick}, false, timeout)
		if err != nil || ev == windows.WAIT_OBJECT_0 {
			return false
		}
	}
}

func (p *pipeIO) expired() bool {
	d := p.deadline.Load()
	return d != 0 && time.Now().UnixNano() >= d
}

// setDeadline sets p's deadline, which a pending operation waits for too.
func (c *pipeConn) setDeadline(p *pipeIO, t time.Time) {
	var d int64
	if !t.IsZero() {
		d = t.UnixNano()
	}
	p.deadline.Store(d)
	c.life.RLock()
	defer c.life.RUnlock()
	if !c.closed.Load() {
		_ = windows.SetEvent(p.kick) // the next wait reads the deadline either way
	}
}

// Close cancels what is pending in either direction, waits for it, and
// closes the handle.
func (c *pipeConn) Close() error {
	err := net.ErrClosed
	c.once.Do(func() {
		c.closed.Store(true)
		_ = windows.CancelIoEx(c.h, nil) // nothing pending is not a failure
		c.r.mu.Lock()
		c.w.mu.Lock()
		c.life.Lock()
		err = errors.Join(windows.CloseHandle(c.h), c.r.close(), c.w.close())
		c.life.Unlock()
		c.w.mu.Unlock()
		c.r.mu.Unlock()
	})
	return err
}

func (c *pipeConn) LocalAddr() net.Addr  { return c.addr }
func (c *pipeConn) RemoteAddr() net.Addr { return c.addr }

func (c *pipeConn) SetDeadline(t time.Time) error {
	c.setDeadline(c.r, t)
	c.setDeadline(c.w, t)
	return nil
}

func (c *pipeConn) SetReadDeadline(t time.Time) error  { c.setDeadline(c.r, t); return nil }
func (c *pipeConn) SetWriteDeadline(t time.Time) error { c.setDeadline(c.w, t); return nil }
