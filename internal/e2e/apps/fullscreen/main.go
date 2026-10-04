// Command fullscreen is the e2e harness's full-screen app, a stand-in for
// vim, htop and Claude Code's default renderer: it switches to the
// alternate screen and asks for everything Claude asks for (any-event
// mouse with SGR coordinates, focus events, bracketed paste, colour-scheme
// reports, kitty keyboard "disambiguate"), then shows every input it
// receives as a quoted line, newest at the bottom. Each repaint is one
// mode-2026 synchronized update.
//
//	fullscreen
//
// The first row reads "fullscreen ready" plus a row of multi-codepoint
// graphemes; "q" restores the terminal and exits.
package main

import (
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
	"golang.org/x/term"
)

const (
	enter = "\x1b[?1049h\x1b[?1003h\x1b[?1006h\x1b[?1004h\x1b[?2004h\x1b[?2031h\x1b[>1u"
	leave = "\x1b[<u\x1b[?2031l\x1b[?2004l\x1b[?1004l\x1b[?1006l\x1b[?1003l\x1b[?1049l"
)

func main() {
	old, err := term.MakeRaw(0)
	if err != nil {
		fmt.Fprintln(os.Stderr, "fullscreen:", err)
		os.Exit(1)
	}
	signal.Ignore(syscall.SIGINT)
	os.Stdout.WriteString(enter)
	var lines []string
	paint := func() {
		rows := 24
		if ws, err := unix.IoctlGetWinsize(1, unix.TIOCGWINSZ); err == nil {
			rows = int(ws.Row)
		}
		var b strings.Builder
		b.WriteString("\x1b[?2026h\x1b[H\x1b[2J")
		b.WriteString("fullscreen ready \x1b[7m 👨‍👩‍👧 🇯🇵 👍🏽 ❤️ \x1b[0m|\r\n")
		if n := rows - 2; len(lines) > n {
			lines = lines[len(lines)-n:]
		}
		for _, l := range lines {
			b.WriteString(l + "\r\n")
		}
		b.WriteString("\x1b[?2026l")
		os.Stdout.WriteString(b.String())
	}
	paint()
	buf := make([]byte, 4096)
	for {
		n, err := os.Stdin.Read(buf)
		if err != nil {
			break
		}
		in := string(buf[:n])
		if in == "q" {
			break
		}
		lines = append(lines, "in: "+strconv.Quote(in))
		paint()
	}
	os.Stdout.WriteString(leave)
	term.Restore(0, old)
}
