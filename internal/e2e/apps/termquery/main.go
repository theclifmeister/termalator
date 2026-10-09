// Command termquery is the e2e harness's stand-in for a prompt program
// such as starship, run from a shell's PROMPT_COMMAND before every prompt:
// it asks the terminal for its colour scheme (CSI ? 996 n), the cursor
// position (CSI 6 n) and its attributes (DA1, CSI c), the way prompt and
// TUI libraries do, with DA1 as the fence for answers that may never
// come. It reads the answers from the terminal (/dev/tty; on Windows the
// console) in raw mode and prints
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

	"github.com/theclifmeister/terminatr/internal/plat/term"
)

func main() {
	timeout := flag.Duration("timeout", 3*time.Second, "how long to wait for DA1")
	flag.Parse()
	in, out, err := openTTY()
	if err != nil {
		fmt.Println("q-error", err)
		return
	}
	restore, err := term.MakeRaw(in)
	if err != nil {
		fmt.Println("q-error", err)
		return
	}
	got := query(in, out, *timeout)
	restore()
	if got {
		fmt.Println("q-ok")
	} else {
		fmt.Println("q-timeout")
	}
	// A read still waiting for input ends with the process.
	os.Exit(0)
}

// query writes the queries and reads until a DA1 answer (CSI ? … c) or
// the timeout.
func query(in, out *os.File, timeout time.Duration) bool {
	if _, err := out.WriteString("\x1b[?996n\x1b[6n\x1b[c"); err != nil {
		return false
	}
	da1 := make(chan bool, 1)
	go func() {
		var buf []byte
		b := make([]byte, 256)
		for {
			n, err := in.Read(b)
			buf = append(buf, b[:n]...)
			if i := bytes.Index(buf, []byte("\x1b[?")); i >= 0 && bytes.IndexByte(buf[i:], 'c') >= 0 {
				da1 <- true
				return
			}
			if err != nil {
				da1 <- false
				return
			}
		}
	}()
	select {
	case ok := <-da1:
		return ok
	case <-time.After(timeout):
		return false
	}
}
