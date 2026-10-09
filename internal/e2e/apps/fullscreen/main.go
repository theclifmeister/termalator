// Command fullscreen is the e2e harness's full-screen app, a stand-in for
// vim, htop and Claude Code's default renderer: it switches to the
// alternate screen and asks for everything Claude asks for (any-event
// mouse with SGR coordinates, focus events, bracketed paste, colour-scheme
// reports, kitty keyboard "disambiguate"), then shows every input it
// receives as a quoted line, newest at the bottom. Each repaint is one
// mode-2026 synchronized update.
//
//	fullscreen [-no-scheme]
//
// The first row reads "fullscreen ready" plus a row of multi-codepoint
// graphemes; "q" restores the terminal and exits. -no-scheme asks for no
// colour-scheme reports.
package main

import (
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"strings"

	"github.com/theclifmeister/terminatr/internal/plat/term"
)

const (
	enter = "\x1b[?1049h\x1b[?1003h\x1b[?1006h\x1b[?1004h\x1b[?2004h\x1b[?2031h\x1b[>1u"
	leave = "\x1b[<u\x1b[?2031l\x1b[?2004l\x1b[?1004l\x1b[?1006l\x1b[?1003l\x1b[?1049l"
)

func main() {
	noScheme := flag.Bool("no-scheme", false, "ask for no colour-scheme reports (mode 2031)")
	flag.Parse()
	enter, leave := enter, leave
	if *noScheme {
		enter = strings.Replace(enter, "\x1b[?2031h", "", 1)
		leave = strings.Replace(leave, "\x1b[?2031l", "", 1)
	}
	restore, err := term.MakeRaw(os.Stdin)
	if err != nil {
		fmt.Fprintln(os.Stderr, "fullscreen:", err)
		os.Exit(1)
	}
	signal.Ignore(os.Interrupt)
	os.Stdout.WriteString(enter)
	var lines []string
	paint := func() {
		rows := 24
		if _, r, ok := term.Size(os.Stdout); ok {
			rows = r
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
	in := ""
	for {
		n, err := os.Stdin.Read(buf)
		if err != nil {
			break
		}
		in += string(buf[:n])
		if in == "q" {
			break
		}
		// A bracketed paste is one input, as an app's parser sees it,
		// even when it arrives in several reads (ConPTY passes the
		// opening \x1b[200~ on its own).
		if i := strings.LastIndex(in, "\x1b[200~"); i >= 0 && !strings.Contains(in[i:], "\x1b[201~") {
			continue
		}
		lines = append(lines, "in: "+strconv.Quote(in))
		in = ""
		paint()
	}
	os.Stdout.WriteString(leave)
	restore()
}
