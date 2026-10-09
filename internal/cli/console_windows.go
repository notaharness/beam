package cli

import (
	"os"
	"time"

	"github.com/notaharness/beam/internal/stream"
	"golang.org/x/sys/windows"
	"golang.org/x/term"
)

// resizePoll is how often connect reads the console's size: Windows signals
// a resize only as an input record, among the keys stdin carries.
const resizePoll = 250 * time.Millisecond

// conout is the console's active screen buffer, whatever stdout is.
func conout() (*os.File, error) { return os.OpenFile("CONOUT$", os.O_RDWR, 0) }

// termSize is the size of the console's window; tty, its input, has none.
func termSize(*os.File) (cols, rows int, err error) {
	out, err := conout()
	if err != nil {
		return 0, 0, err
	}
	defer out.Close()
	return term.GetSize(int(out.Fd()))
}

// makeRaw puts the console in raw mode for a remote terminal: input as VT
// sequences, and output read as VT sequences rather than written as text;
// restore puts both back.
func makeRaw(tty *os.File) (restore func(), err error) {
	out, err := conout()
	if err != nil {
		return nil, err
	}
	h := windows.Handle(out.Fd())
	mode, err := vtOutput(h)
	if err != nil {
		out.Close()
		return nil, err
	}
	old, err := term.MakeRaw(int(tty.Fd()))
	if err != nil {
		_ = windows.SetConsoleMode(h, mode) // put back as found
		out.Close()
		return nil, err
	}
	return func() {
		_ = term.Restore(int(tty.Fd()), old) // nowhere to report it
		_ = windows.SetConsoleMode(h, mode)
		out.Close()
	}, nil
}

// vtOutput has the console screen buffer h read VT sequences, and returns
// its mode before.
func vtOutput(h windows.Handle) (mode uint32, err error) {
	if err := windows.GetConsoleMode(h, &mode); err != nil {
		return 0, err
	}
	return mode, windows.SetConsoleMode(h, mode|windows.ENABLE_VIRTUAL_TERMINAL_PROCESSING|windows.DISABLE_NEWLINE_AUTO_RETURN)
}

// forwardResize sends the console's size whenever it changes.
func forwardResize(_ *os.File, f *frames) func() {
	return watchSize(func() (int, int, error) { return termSize(nil) }, resizePoll, func(cols, rows int) {
		_ = f.control(stream.Ctl{Kind: "resize", Cols: cols, Rows: rows}) // a lost stream shows in relay
	})
}

// watchSize reads size every period and calls send with each new answer,
// until stop.
func watchSize(size func() (cols, rows int, err error), every time.Duration, send func(cols, rows int)) (stop func()) {
	done := make(chan struct{})
	go func() {
		cols, rows, _ := size()
		t := time.NewTicker(every)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-t.C:
			}
			w, h, err := size()
			if err != nil || w == cols && h == rows {
				continue
			}
			cols, rows = w, h
			send(w, h)
		}
	}()
	return func() { close(done) }
}
