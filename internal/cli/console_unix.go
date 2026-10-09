//go:build unix

package cli

import (
	"os"
	"os/signal"
	"syscall"

	"github.com/notaharness/beam/internal/stream"
	"golang.org/x/term"
)

// termSize is the size of the terminal tty is.
func termSize(tty *os.File) (cols, rows int, err error) { return term.GetSize(int(tty.Fd())) }

// makeRaw puts the terminal tty is in raw mode; restore puts it back.
func makeRaw(tty *os.File) (restore func(), err error) {
	old, err := term.MakeRaw(int(tty.Fd()))
	if err != nil {
		return nil, err
	}
	return func() { _ = term.Restore(int(tty.Fd()), old) }, nil // nowhere to report it
}

// forwardResize sends the terminal's size on every SIGWINCH.
func forwardResize(tty *os.File, f *frames) func() {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGWINCH)
	go func() {
		for range ch {
			if w, h, err := term.GetSize(int(tty.Fd())); err == nil {
				_ = f.control(stream.Ctl{Kind: "resize", Cols: w, Rows: h}) // a lost stream shows in relay
			}
		}
	}()
	return func() { signal.Stop(ch); close(ch) }
}
