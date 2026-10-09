// Package term is terminatr's outer terminal: whether a file is one, its
// size, raw mode, and the events the terminal's owner sends (a resize, a
// request to let go of it).
//
// The terminal calls go through golang.org/x/term, which covers the
// Windows console too. RawMode and Events are per OS: termios and signals
// on Unix; on Windows the console mode (with VT input and output switched
// on, so keys and escapes travel as on Unix), a poll of the console size
// for resizes, and the console's close and break events for Detach.
package term

import (
	"context"
	"os"

	xterm "golang.org/x/term"
)

// IsTerminal reports whether f is a terminal.
func IsTerminal(f *os.File) bool { return xterm.IsTerminal(int(f.Fd())) }

// Size returns f's terminal size; ok is false when f is not a terminal or
// reports no size. On Windows a console's input handle has no size of its
// own: it reports the console's screen buffer.
func Size(f *os.File) (cols, rows int, ok bool) {
	cols, rows, err := size(f)
	if err != nil || cols <= 0 || rows <= 0 {
		return 0, 0, false
	}
	return cols, rows, true
}

// MakeRaw puts the terminal f into raw mode; restore puts it back as it
// was.
func MakeRaw(f *os.File) (restore func() error, err error) {
	fd := int(f.Fd())
	old, err := xterm.MakeRaw(fd)
	if err != nil {
		return nil, err
	}
	undo := enableVT(f)
	return func() error {
		undo()
		return xterm.Restore(fd, old)
	}, nil
}

// Kind is what an Event says.
type Kind int

const (
	// Resize: the terminal changed size; ask Size for the new one.
	Resize Kind = iota
	// Detach: the terminal went away or its user asked the program to let
	// go (on Unix SIGHUP, SIGTERM, SIGINT).
	Detach
)

// Event is one event from the terminal's owner.
type Event struct {
	Kind Kind
	// Why names the cause of a Detach, e.g. "hangup".
	Why string
}

// Events delivers the terminal's events until ctx ends, when the channel
// closes. While it runs, the events no longer act on the process (an
// interrupt does not kill it).
func Events(ctx context.Context) <-chan Event {
	ch := make(chan Event, 4)
	go events(ctx, ch)
	return ch
}
