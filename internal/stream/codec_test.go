package stream

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"strings"
	"testing"
	"testing/iotest"
)

func pipe(t *testing.T) (*Conn, *Conn) {
	t.Helper()
	a, b := net.Pipe()
	t.Cleanup(func() { a.Close(); b.Close() })
	return NewConn(a), NewConn(b)
}

func TestLineRoundTrip(t *testing.T) {
	a, b := pipe(t)
	go a.WriteLine(Header{V: 1, Kind: "hello"})
	var h Header
	if err := b.ReadLine(&h); err != nil {
		t.Fatal(err)
	}
	if h.V != 1 || h.Kind != "hello" {
		t.Fatalf("got %+v", h)
	}
}

func TestLineCap(t *testing.T) {
	for _, tc := range []struct {
		name string
		n    int // bytes before the newline
		ok   bool
	}{
		{"at cap", MaxLine - 1, true},
		{"over cap", MaxLine, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pad := tc.n - len(`{"v":1,"kind":"x","pad":""}`)
			line := `{"v":1,"kind":"x","pad":"` + strings.Repeat("a", pad) + `"}` + "\n"
			c := &Conn{r: bufio.NewReader(iotest.OneByteReader(strings.NewReader(line)))}
			var h Header
			err := c.ReadLine(&h)
			if tc.ok != (err == nil) {
				t.Fatalf("len %d: err = %v", tc.n, err)
			}
			if !tc.ok && !errors.Is(err, ErrTooLarge) {
				t.Fatalf("err = %v, want ErrTooLarge", err)
			}
		})
	}
}

func TestWriteLineCap(t *testing.T) {
	for _, tc := range []struct {
		name string
		n    int // JSON bytes; the newline makes the line one longer
		ok   bool
	}{
		{"at cap", MaxLine - 1, true},
		{"over cap", MaxLine, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			c := &Conn{w: &buf}
			pad := strings.Repeat("a", tc.n-len(`{"pad":""}`))
			err := c.WriteLine(map[string]string{"pad": pad})
			if tc.ok != (err == nil) || !tc.ok && !errors.Is(err, ErrTooLarge) {
				t.Fatalf("err = %v", err)
			}
			if tc.ok != (buf.Len() == MaxLine) {
				t.Fatalf("wrote %d bytes", buf.Len())
			}
		})
	}
}

func TestReadLineErrors(t *testing.T) {
	for _, in := range []string{
		"",
		"{\"v\":1",
		"not json\n",
		"{\"v\":1,\"kind\":\"hello\"} junk\n", // one value, then garbage
		"{\"v\":1,\"kind\":\"hello\"}{\"v\":2}\n", // two values on one line
	} {
		c := &Conn{r: bufio.NewReader(strings.NewReader(in))}
		var h Header
		if err := c.ReadLine(&h); err == nil {
			t.Errorf("%q accepted", in)
		}
	}
}

func TestFrameRoundTripOneByteAtATime(t *testing.T) {
	var buf bytes.Buffer
	w := &Conn{w: &buf}
	frames := []struct {
		t Type
		p []byte
	}{
		{Data, []byte("hello")},
		{Control, []byte(`{"kind":"ping"}`)},
		{Close, nil},
		{Data, bytes.Repeat([]byte{7}, MaxPayload)},
	}
	for _, f := range frames {
		if err := w.WriteFrame(f.t, f.p); err != nil {
			t.Fatal(err)
		}
	}
	r := &Conn{r: bufio.NewReader(iotest.OneByteReader(&buf))}
	for i, f := range frames {
		typ, p, err := r.ReadFrame()
		if err != nil {
			t.Fatalf("frame %d: %v", i, err)
		}
		if typ != f.t || !bytes.Equal(p, f.p) {
			t.Fatalf("frame %d: got type %d, %d bytes", i, typ, len(p))
		}
	}
	if _, _, err := r.ReadFrame(); err != io.EOF {
		t.Fatalf("after last frame: %v, want EOF", err)
	}
}

func TestFrameErrors(t *testing.T) {
	over := make([]byte, 5)
	binary.BigEndian.PutUint32(over[1:], MaxPayload+1)
	for _, tc := range []struct {
		name string
		in   []byte
		want error
	}{
		{"payload over cap", over, ErrTooLarge},
		{"unknown type", []byte{3, 0, 0, 0, 0}, ErrFrameType},
		{"truncated header", []byte{0, 0}, io.ErrUnexpectedEOF},
		{"truncated payload", []byte{0, 0, 0, 0, 4, 'a'}, io.ErrUnexpectedEOF},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := &Conn{r: bufio.NewReader(bytes.NewReader(tc.in))}
			if _, _, err := c.ReadFrame(); !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
		})
	}
	w := &Conn{w: io.Discard}
	if err := w.WriteFrame(Data, make([]byte, MaxPayload+1)); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("write over cap: %v", err)
	}
	if err := w.WriteFrame(3, nil); !errors.Is(err, ErrFrameType) {
		t.Fatalf("write unknown type: %v", err)
	}
}

func TestConnReadsThroughBuffer(t *testing.T) {
	a, b := pipe(t)
	go func() {
		a.WriteLine(Response{OK: true})
		a.Write([]byte("raw"))
		a.Close()
	}()
	var r Response
	if err := b.ReadLine(&r); err != nil || !r.OK {
		t.Fatalf("response %+v, %v", r, err)
	}
	rest, err := io.ReadAll(b)
	if err != nil || string(rest) != "raw" {
		t.Fatalf("bytes after the line: %q, %v", rest, err)
	}
}

func TestParseClose(t *testing.T) {
	lost := CloseMsg{Reason: "connection-lost"}
	for _, tc := range []struct {
		payload string
		want    string // the reason
		code    int
	}{
		{`{"reason":"exit","exitCode":3}`, "exit", 3},
		{`{"reason":"exit"}`, lost.Reason, 0},
		{`{"reason":"offline","detail":"x"}`, "offline", 0},
		{`{"reason":`, lost.Reason, 0},
	} {
		m := ParseClose([]byte(tc.payload))
		if m.Reason != tc.want || m.ExitCode != nil && *m.ExitCode != tc.code {
			t.Errorf("%s: got %+v", tc.payload, m)
		}
	}
}
