// Command printer is the e2e harness's first deterministic app: it prints
// a known screen, optionally streams numbered lines, and then waits.
// Real programs (vim, htop, shells) vary between machines; scenarios use
// apps like this instead.
//
//	printer [-lines N] [-delay D]
//
// It prints a banner with styles and wide characters, its window size,
// then N lines "line 1" … "line N" (D apart), then "ready".
package main

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

func main() {
	lines := flag.Int("lines", 0, "numbered lines to print")
	delay := flag.Duration("delay", 0, "pause between lines")
	flag.Parse()

	signal.Ignore(syscall.SIGINT)
	w := bufio.NewWriter(os.Stdout)
	fmt.Fprint(w, "printer: \x1b[1mbold\x1b[0m \x1b[32mgreen\x1b[0m wide:日本語\r\n")
	if ws, err := unix.IoctlGetWinsize(1, unix.TIOCGWINSZ); err == nil {
		fmt.Fprintf(w, "size %dx%d\r\n", ws.Col, ws.Row)
	}
	w.Flush()
	for i := 1; i <= *lines; i++ {
		fmt.Fprintf(w, "line %d\r\n", i)
		if *delay > 0 {
			w.Flush()
			time.Sleep(*delay)
		}
	}
	fmt.Fprint(w, "ready\r\n")
	w.Flush()
	for {
		time.Sleep(time.Hour) // a bare select{} would trip the deadlock detector
	}
}
