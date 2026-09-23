// Package stream is the wire format of a stream (docs/04): a JSON header line,
// a JSON response line, then frames.
package stream

import (
	"bufio"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
)

// Limits from docs/04.
const (
	MaxLine    = 64 << 10 // header or response line, newline included
	MaxPayload = 1 << 20  // frame payload
)

// Type is a frame type.
type Type byte

// Frame types.
const (
	Data    Type = 0
	Control Type = 1
	Close   Type = 2
)

var (
	ErrTooLarge  = errors.New("stream: over the size limit")
	ErrFrameType = errors.New("stream: unknown frame type")
)

// Header opens a stream. Argv, Cwd and Env are for pty and exec, Cols and
// Rows for pty.
type Header struct {
	V    int               `json:"v"`
	Kind string            `json:"kind"`
	Argv []string          `json:"argv,omitempty"`
	Cwd  string            `json:"cwd,omitempty"`
	Env  map[string]string `json:"env,omitempty"`
	Cols int               `json:"cols,omitempty"`
	Rows int               `json:"rows,omitempty"`
}

// Stream kinds.
const (
	KindHello = "hello"
	KindSync  = "sync"
	KindPTY   = "pty"
	KindExec  = "exec"
	KindMsg   = "msg"
)

// Ctl is a control frame's payload; each kind uses some of the fields.
type Ctl struct {
	Kind string `json:"kind"`
	T    int64  `json:"t,omitempty"`    // ping, pong
	Cols int    `json:"cols,omitempty"` // resize
	Rows int    `json:"rows,omitempty"` // resize
}

// CloseMsg is a close frame's payload.
type CloseMsg struct {
	Reason   string  `json:"reason"`
	Detail   string  `json:"detail,omitempty"`
	ExitCode *int    `json:"exitCode,omitempty"`
	Signal   *string `json:"signal,omitempty"`
}

// ParseClose decodes a close frame's payload. One that is malformed, or an
// exit without its code, reads as connection-lost.
func ParseClose(p []byte) CloseMsg {
	var m CloseMsg
	if json.Unmarshal(p, &m) != nil || m.Reason == "exit" && m.ExitCode == nil {
		return CloseMsg{Reason: "connection-lost"}
	}
	return m
}

// WriteJSON writes v as the payload of a frame of type t.
func (c *Conn) WriteJSON(t Type, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return c.WriteFrame(t, b)
}

// Response answers a header, and carries the result of hello.
type Response struct {
	OK     bool   `json:"ok"`
	Reason string `json:"reason,omitempty"`
	Detail string `json:"detail,omitempty"`
}

// Conn is a stream connection. Reads go through a buffer so that bytes after a
// line are not lost.
type Conn struct {
	net.Conn
	r *bufio.Reader
	w io.Writer

	mu     sync.Mutex // orders Close against a process start
	closed bool
	done   chan struct{} // closed by Close
}

// NewConn wraps c.
func NewConn(c net.Conn) *Conn {
	return &Conn{Conn: c, r: bufio.NewReader(c), w: c, done: make(chan struct{})}
}

// Close closes the connection; Done is closed with it.
func (c *Conn) Close() error {
	c.mu.Lock()
	if !c.closed {
		c.closed = true
		close(c.done)
	}
	c.mu.Unlock()
	return c.Conn.Close()
}

// unlessClosed runs start unless the stream has been closed here, holding
// Close off until it returns: a stream closed before its process starts never
// starts it.
func (c *Conn) unlessClosed(start func() error) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return net.ErrClosed
	}
	return start()
}

// Done is closed once the stream has been closed on this side, which a reader
// held back from the connection notices here.
func (c *Conn) Done() <-chan struct{} { return c.done }

func (c *Conn) Read(p []byte) (int, error)  { return c.r.Read(p) }
func (c *Conn) Write(p []byte) (int, error) { return c.w.Write(p) }

// WriteLine writes v as one JSON line.
func (c *Conn) WriteLine(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if len(b)+1 > MaxLine {
		return ErrTooLarge
	}
	_, err = c.w.Write(append(b, '\n'))
	return err
}

// ReadLine reads one JSON line into v.
func (c *Conn) ReadLine(v any) error {
	line, err := ReadLine(c.r, MaxLine)
	if err != nil {
		return err
	}
	return json.Unmarshal(line, v) // the whole line is one value
}

// ReadLine reads one newline-terminated line of at most max bytes, newline
// included.
func ReadLine(r *bufio.Reader, max int) ([]byte, error) {
	var line []byte
	for {
		chunk, err := r.ReadSlice('\n')
		line = append(line, chunk...)
		if len(line) > max {
			return nil, ErrTooLarge
		}
		if err == nil {
			return line, nil
		}
		if err != bufio.ErrBufferFull {
			return nil, err
		}
	}
}

// NewConnReader is NewConn for a connection whose first bytes were already
// read into r.
func NewConnReader(c net.Conn, r *bufio.Reader) *Conn {
	return &Conn{Conn: c, r: r, w: c, done: make(chan struct{})}
}

// WriteFrame writes one frame.
func (c *Conn) WriteFrame(t Type, payload []byte) error {
	if t > Close {
		return ErrFrameType
	}
	if len(payload) > MaxPayload {
		return ErrTooLarge
	}
	b := make([]byte, 5+len(payload))
	b[0] = byte(t)
	binary.BigEndian.PutUint32(b[1:], uint32(len(payload)))
	copy(b[5:], payload)
	_, err := c.w.Write(b)
	return err
}

// ReadFrame reads one frame. A clean end of stream before a frame is io.EOF.
func (c *Conn) ReadFrame() (Type, []byte, error) {
	var h [5]byte
	if _, err := io.ReadFull(c.r, h[:]); err != nil {
		return 0, nil, err
	}
	t, n := Type(h[0]), binary.BigEndian.Uint32(h[1:])
	if t > Close {
		return 0, nil, fmt.Errorf("%w %d", ErrFrameType, t)
	}
	if n > MaxPayload {
		return 0, nil, ErrTooLarge
	}
	p := make([]byte, n)
	if _, err := io.ReadFull(c.r, p); err != nil {
		return 0, nil, err
	}
	return t, p, nil
}
