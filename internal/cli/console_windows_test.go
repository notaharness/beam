package cli

import (
	"errors"
	"testing"
	"time"
)

// docs/07 Streams: connect sends a Windows console's size when it changes,
// and only then; a size it cannot read sends nothing.
func TestWatchSize(t *testing.T) {
	type size struct {
		cols, rows int
		err        error
	}
	sizes := make(chan size, 8)
	for _, s := range []size{{80, 24, nil}, {80, 24, nil}, {100, 30, nil}, {0, 0, errors.New("no console")},
		{100, 30, nil}, {120, 40, nil}} {
		sizes <- s
	}
	read := func() (int, int, error) {
		select {
		case s := <-sizes:
			return s.cols, s.rows, s.err
		default:
			return 120, 40, nil
		}
	}
	sent := make(chan [2]int, 8)
	stop := watchSize(read, time.Millisecond, func(cols, rows int) { sent <- [2]int{cols, rows} })
	defer stop()
	for _, want := range [][2]int{{100, 30}, {120, 40}} {
		select {
		case got := <-sent:
			if got != want {
				t.Errorf("sent %v, want %v", got, want)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("no resize to %v", want)
		}
	}
	select {
	case got := <-sent:
		t.Errorf("sent %v for a size that did not change", got)
	case <-time.After(50 * time.Millisecond):
	}
}
