// Command printer is the e2e harness's first deterministic app: it prints
// a known screen, optionally streams numbered lines, and then waits.
// Real programs (vim, htop, shells) vary between machines; scenarios use
// apps like this instead.
//
//	printer [-lines N] [-delay D]
//
// It prints a banner with styles and wide characters, its window size,
// then N lines "line 1" … "line N" (D apart), then "ready". Every
// SIGWINCH (on Windows a change of the console size) prints "resized to
// CxR", so a scenario can tell whether the pane was resized.
//
// It turns its terminal's echo off: it never reads, and on Linux the echo
// of a key typed while it prints can land between a line's "\r" and
// "\n" (n_tty writes them separately), overwriting the line's first
// character. A Windows console echoes only during a read, so there it
// needs nothing.
package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"sync"
	"time"

	"github.com/theclifmeister/terminatr/internal/plat/term"
)

func main() {
	lines := flag.Int("lines", 0, "numbered lines to print")
	delay := flag.Duration("delay", 0, "pause between lines")
	flag.Parse()

	signal.Ignore(os.Interrupt)
	echoOff()
	var mu sync.Mutex // one writer at a time
	go func() {
		// Events takes over the hangup too: it still ends the printer.
		for ev := range term.Events(context.Background()) {
			if ev.Kind == term.Detach {
				if ev.Why == "interrupt" {
					continue
				}
				os.Exit(0)
			}
			if cols, rows, ok := term.Size(os.Stdout); ok {
				mu.Lock()
				fmt.Printf("resized to %dx%d\r\n", cols, rows)
				mu.Unlock()
			}
		}
	}()
	mu.Lock()
	w := bufio.NewWriter(os.Stdout)
	fmt.Fprint(w, "printer: \x1b[1mbold\x1b[0m \x1b[32mgreen\x1b[0m wide:日本語\r\n")
	if cols, rows, ok := term.Size(os.Stdout); ok {
		fmt.Fprintf(w, "size %dx%d\r\n", cols, rows)
	}
	w.Flush()
	for i := 1; i <= *lines; i++ {
		fmt.Fprintf(w, "line %d\r\n", i)
		if *delay > 0 {
			w.Flush()
			mu.Unlock()
			time.Sleep(*delay)
			mu.Lock()
		}
	}
	fmt.Fprint(w, "ready\r\n")
	w.Flush()
	mu.Unlock()
	for {
		time.Sleep(time.Hour) // a bare select{} would trip the deadlock detector
	}
}
