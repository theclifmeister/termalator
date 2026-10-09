//go:build windows

package term

import (
	"context"
	"os"
	"os/signal"
	"syscall"
	"time"

	"golang.org/x/sys/windows"
)

// resizePoll is how often the console size is read for changes. Reading
// WINDOW_BUFFER_SIZE_EVENT records would mean reading the input handle,
// which the client's own key reader owns, so the size is polled instead.
const resizePoll = 100 * time.Millisecond

const (
	enableVirtualTerminalInput      = 0x0200
	enableVirtualTerminalProcessing = 0x0004
	disableNewlineAutoReturn        = 0x0008
)

// enableVT switches the console of f on to VT: on an input handle keys
// arrive as escape sequences; on stdout and stderr the console reads
// escapes instead of drawing control characters. The returned func puts
// every mode back. x/term's MakeRaw clears the line, echo and processed
// flags already; this adds the VT ones.
func enableVT(f *os.File) func() {
	var undo []func()
	set := func(h windows.Handle, add, remove uint32) {
		var mode uint32
		if windows.GetConsoleMode(h, &mode) != nil {
			return
		}
		if windows.SetConsoleMode(h, mode&^remove|add) == nil {
			undo = append(undo, func() { windows.SetConsoleMode(h, mode) })
		}
	}
	set(windows.Handle(f.Fd()), enableVirtualTerminalInput, 0)
	for _, std := range []*os.File{os.Stdout, os.Stderr} {
		set(windows.Handle(std.Fd()), enableVirtualTerminalProcessing|disableNewlineAutoReturn, 0)
	}
	return func() {
		for i := len(undo) - 1; i >= 0; i-- {
			undo[i]()
		}
	}
}

// RawMode reports whether f is a console with line input and echo off.
func RawMode(f *os.File) bool {
	var mode uint32
	if windows.GetConsoleMode(windows.Handle(f.Fd()), &mode) != nil {
		return false
	}
	return mode&(windows.ENABLE_LINE_INPUT|windows.ENABLE_ECHO_INPUT) == 0
}

// events turns a change of the console size into Resize, and the console
// closing (window closed, logoff, shutdown), Ctrl+Break and Ctrl+C into
// Detach. Go reports the first as SIGTERM and the others as SIGINT; in raw
// mode Ctrl+C is a key, not a signal.
func events(ctx context.Context, ch chan<- Event) {
	defer close(ch)
	sigs := make(chan os.Signal, 4)
	signal.Notify(sigs, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sigs)
	tick := time.NewTicker(resizePoll)
	defer tick.Stop()
	cols, rows, _ := Size(os.Stdout)
	for {
		var ev Event
		select {
		case <-ctx.Done():
			return
		case sig := <-sigs:
			why := "interrupt"
			if sig == syscall.SIGTERM {
				why = "hangup"
			}
			ev = Event{Kind: Detach, Why: why}
		case <-tick.C:
			c, r, ok := Size(os.Stdout)
			if !ok || (c == cols && r == rows) {
				continue
			}
			cols, rows = c, r
			ev = Event{Kind: Resize}
		}
		select {
		case ch <- ev:
		case <-ctx.Done():
			return
		}
	}
}
