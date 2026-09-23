package stream

import (
	"io"
	"sync"
)

// InputWindow is how many of the opener's input frames on pty and exec, data
// or control, may be outstanding: sent and not yet answered taken (docs/04,
// Input).
const InputWindow = 4

// window counts an opener's outstanding input frames and answers them. A
// frame holds its place until its taken is written, so at most a window of
// answers wait in acks and taken never blocks: the reader queues the answer
// and reads on, whatever the output's pace.
type window struct {
	mu   sync.Mutex
	n    int
	acks chan struct{}
}

// input starts delivering the opener's input to in, and answering each frame
// taken on w. The caller feeds the returned queue.
func input(w *writer, in io.WriteCloser) (*window, chan<- []byte) {
	win := &window{acks: make(chan struct{}, InputWindow)}
	q := make(chan []byte, InputWindow)
	go func() {
		deliver(q, in, win.taken)
		close(win.acks) // feed, the other sender, closed q before
	}()
	go win.answer(w)
	return win, q
}

// take admits an input frame; false is an overrun.
func (win *window) take() bool {
	win.mu.Lock()
	defer win.mu.Unlock()
	if win.n == InputWindow {
		return false
	}
	win.n++
	return true
}

// taken queues the answer for a frame taken.
func (win *window) taken() { win.acks <- struct{}{} }

// answer writes the queued answers, giving each frame's place back just
// before the opener can learn of it.
func (win *window) answer(w *writer) {
	for range win.acks {
		win.mu.Lock()
		win.n--
		win.mu.Unlock()
		_ = w.control(Ctl{Kind: "taken"}) // a lost opener ends feed
	}
}

// feed reads the opener's frames until its side ends or sends close, or it
// overruns its window, which feed reports. Each data or control frame takes
// a place in win. frame turns it into input for q (nil ending the input), or
// handles it at once, and it is answered then. q holds a window and answers
// are queued, so feed waits neither on the process nor on the output: it
// always reads on. It closes q.
func feed(c *Conn, q chan<- []byte, win *window, frame func(Type, []byte) (in []byte, queue bool)) (overrun bool) {
	defer close(q)
	for {
		t, p, err := c.ReadFrame()
		switch {
		case err != nil || t == Close:
			return false
		case !win.take():
			return true
		}
		if in, queue := frame(t, p); queue {
			q <- in
		} else {
			win.taken()
		}
	}
}

// discard drops an opener's frames after its overrun until its side ends, so
// the close it is sent is not lost to a reset over input left unread.
func discard(c *Conn) {
	for {
		if _, _, err := c.ReadFrame(); err != nil {
			return
		}
	}
}

// deliver writes queued input to w until q closes, nil closing w, and calls
// taken after each. A process that exited, or whose input was closed on the
// opener's departure, fails the writes and the rest is dropped.
func deliver(q <-chan []byte, w io.WriteCloser, taken func()) {
	for p := range q {
		if p == nil {
			_ = w.Close() // the process sees end of input
		} else {
			_, _ = w.Write(p)
		}
		taken()
	}
}
