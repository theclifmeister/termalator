//go:build unix

// Command printer is the e2e harness's first deterministic app: it prints
// a known screen, optionally streams numbered lines, and then waits.
// Real programs (vim, htop, shells) vary between machines; scenarios use
// apps like this instead.
//
//	printer [-lines N] [-delay D]
//
// It prints a banner with styles and wide characters, its window size,
// then N lines "line 1" … "line N" (D apart), then "ready". Every
// SIGWINCH prints "resized to CxR", so a scenario can tell whether the
// pane was resized.
//
// It turns its terminal's echo off: it never reads, and on Linux the echo
// of a key typed while it prints can land between a line's "\r" and
// "\n" (n_tty writes them separately), overwriting the line's first
// character.
package main

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

func main() {
	lines := flag.Int("lines", 0, "numbered lines to print")
	delay := flag.Duration("delay", 0, "pause between lines")
	flag.Parse()

	signal.Ignore(syscall.SIGINT)
	noEcho := exec.Command("stty", "-echo")
	noEcho.Stdin = os.Stdin
	noEcho.Run()
	var mu sync.Mutex // one writer at a time
	winch := make(chan os.Signal, 1)
	signal.Notify(winch, syscall.SIGWINCH)
	go func() {
		for range winch {
			if ws, err := unix.IoctlGetWinsize(1, unix.TIOCGWINSZ); err == nil {
				mu.Lock()
				fmt.Printf("resized to %dx%d\r\n", ws.Col, ws.Row)
				mu.Unlock()
			}
		}
	}()
	mu.Lock()
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
