// Command termquery is the e2e harness's stand-in for a prompt program
// such as starship, run from a shell's PROMPT_COMMAND before every prompt:
// it asks the terminal for its colour scheme (CSI ? 996 n), the cursor
// position (CSI 6 n) and its attributes (DA1, CSI c), the way prompt and
// TUI libraries do, with DA1 as the fence for answers that may never
// come. It reads the answers from /dev/tty in raw mode and prints
//
//	q-ok      the DA1 answer arrived (other answers may or may not have)
//	q-timeout nothing answered DA1 within -timeout
//
// A pane whose queries go unanswered stalls a real prompt, so a scenario
// checks for q-ok.
//
//	termquery [-timeout D]
package main

import (
	"bytes"
	"flag"
	"fmt"
	"os"
	"time"

	"golang.org/x/sys/unix"
	"golang.org/x/term"
)

func main() {
	timeout := flag.Duration("timeout", 3*time.Second, "how long to wait for DA1")
	flag.Parse()
	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		fmt.Println("q-error", err)
		return
	}
	defer tty.Close()
	fd := int(tty.Fd())
	old, err := term.MakeRaw(fd)
	if err != nil {
		fmt.Println("q-error", err)
		return
	}
	got := query(tty, fd, *timeout)
	term.Restore(fd, old)
	if got {
		fmt.Println("q-ok")
	} else {
		fmt.Println("q-timeout")
	}
}

// query writes the queries and reads until a DA1 answer (CSI ? … c) or
// the timeout.
func query(tty *os.File, fd int, timeout time.Duration) bool {
	if _, err := tty.WriteString("\x1b[?996n\x1b[6n\x1b[c"); err != nil {
		return false
	}
	deadline := time.Now().Add(timeout)
	var buf []byte
	b := make([]byte, 256)
	for time.Now().Before(deadline) {
		fds := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
		ms := int(time.Until(deadline) / time.Millisecond)
		if n, err := unix.Poll(fds, max(ms, 1)); err != nil && err != unix.EINTR {
			return false
		} else if n <= 0 {
			continue
		}
		n, err := unix.Read(fd, b)
		if err != nil && err != unix.EINTR && err != unix.EAGAIN {
			return false
		}
		if n > 0 {
			buf = append(buf, b[:n]...)
		}
		if i := bytes.Index(buf, []byte("\x1b[?")); i >= 0 && bytes.IndexByte(buf[i:], 'c') >= 0 {
			return true
		}
	}
	return false
}
