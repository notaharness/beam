package stream

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net"
	"strings"
	"testing"
	"testing/iotest"

	"pgregory.net/rapid"
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

// splitReader hands out b in the chunk sizes it is given, cycling through
// them.
type splitReader struct {
	b     []byte
	sizes []int
	i     int
}

func (r *splitReader) Read(p []byte) (int, error) {
	if len(r.b) == 0 {
		return 0, io.EOF
	}
	n := min(len(p), len(r.b), r.sizes[r.i%len(r.sizes)])
	r.i++
	copy(p, r.b[:n])
	r.b = r.b[n:]
	return n, nil
}

func drawSplits(t *rapid.T) []int {
	return rapid.SliceOfN(rapid.IntRange(1, 97), 1, 16).Draw(t, "splits")
}

// docs/10: frames come through whole under any split of the byte stream.
func TestFramesUnderArbitrarySplits(t *testing.T) {
	type frame struct {
		t Type
		p []byte
	}
	rapid.Check(t, func(t *rapid.T) {
		frames := rapid.SliceOfN(rapid.Custom(func(t *rapid.T) frame {
			return frame{Type(rapid.IntRange(0, 2).Draw(t, "type")), rapid.SliceOfN(rapid.Byte(), 0, 2048).Draw(t, "payload")}
		}), 0, 8).Draw(t, "frames")
		var buf bytes.Buffer
		w := &Conn{w: &buf}
		for _, f := range frames {
			if err := w.WriteFrame(f.t, f.p); err != nil {
				t.Fatal(err)
			}
		}
		r := &Conn{r: bufio.NewReaderSize(&splitReader{b: buf.Bytes(), sizes: drawSplits(t)}, 16)}
		for i, f := range frames {
			typ, p, err := r.ReadFrame()
			if err != nil || typ != f.t || !bytes.Equal(p, f.p) {
				t.Fatalf("frame %d: type %d, %d bytes, %v", i, typ, len(p), err)
			}
		}
		if _, _, err := r.ReadFrame(); err != io.EOF {
			t.Fatalf("after the last frame: %v, want EOF", err)
		}
	})
}

// docs/10: a header line is read whole at any length up to the cap, however
// it arrives and however small the buffer, and refused beyond it.
func TestHeaderLinesAroundTheCap(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		n := rapid.IntRange(MaxLine-64, MaxLine+64).Draw(t, "bytes before the newline")
		line := `{"v":1,"kind":"x","pad":"` + strings.Repeat("a", n-len(`{"v":1,"kind":"x","pad":""}`)) + "\"}\n"
		size := rapid.IntRange(16, 8192).Draw(t, "buffer")
		c := &Conn{r: bufio.NewReaderSize(&splitReader{b: []byte(line), sizes: drawSplits(t)}, size)}
		var h Header
		err := c.ReadLine(&h)
		switch {
		case n+1 <= MaxLine && (err != nil || h.Kind != "x"):
			t.Fatalf("%d bytes: %+v, %v", n+1, h, err)
		case n+1 > MaxLine && !errors.Is(err, ErrTooLarge):
			t.Fatalf("%d bytes: %v, want ErrTooLarge", n+1, err)
		}
	})
}

// docs/05: beam's JSON escapes neither markup nor line separators, so text
// goes out no larger than it came.
func TestMarshalUnescaped(t *testing.T) {
	b, err := Marshal(map[string]any{"s": "<a & b>\u2028\u2029", "raw": json.RawMessage("\"\u2028<\"")})
	if want := "{\"raw\":\"\u2028<\",\"s\":\"<a & b>\u2028\u2029\"}"; err != nil || string(b) != want {
		t.Errorf("%s, %v; want %s", b, err, want)
	}
}
