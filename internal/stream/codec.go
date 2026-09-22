// Package stream is the wire format of a stream (docs/04): a JSON header line,
// a JSON response line, then frames.
package stream

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
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

// Header opens a stream.
type Header struct {
	V    int    `json:"v"`
	Kind string `json:"kind"`
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
}

// NewConn wraps c.
func NewConn(c net.Conn) *Conn {
	return &Conn{Conn: c, r: bufio.NewReader(c), w: c}
}

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
	var line []byte
	for {
		chunk, err := c.r.ReadSlice('\n')
		line = append(line, chunk...)
		if len(line) > MaxLine {
			return ErrTooLarge
		}
		if err == nil {
			break
		}
		if err != bufio.ErrBufferFull {
			return err
		}
	}
	d := json.NewDecoder(bytes.NewReader(line))
	return d.Decode(v)
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
