package main

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/theclifmeister/terminatr/internal/plat/term"
)

const (
	termSetup   = "\x1b[?1049h\x1b[?1003h\x1b[?1006h\x1b[?1004h\x1b[?2004h\x1b[>1u\x1b[?25l"
	termRestore = "\x1b[<u\x1b[?2004l\x1b[?1004l\x1b[?1006l\x1b[?1003l\x1b[?25h\x1b[?1049l"
	dimOn       = "\x1b[2m"
	dimOff      = "\x1b[22m"
	spinFrames  = "◐◑◒◓"
)

// setupTerm puts stdin in raw mode and switches to the full-screen modes.
// It returns the function that undoes both.
func setupTerm() func() {
	restore, err := term.MakeRaw(os.Stdin)
	os.Stdout.WriteString(termSetup)
	return func() {
		os.Stdout.WriteString(termRestore)
		if err == nil {
			_ = restore()
		}
	}
}

// termSize is the terminal size, 80x24 when unknown.
func termSize() (cols, rows int) {
	cols, rows, ok := term.Size(os.Stdout)
	if !ok {
		return 80, 24
	}
	return cols, rows
}

// convLine is one line of the conversation. Streaming text is revealed
// over dur from start.
type convLine struct {
	prefix string
	text   string
	start  time.Time
	dur    time.Duration
}

// visible is the part of the line shown at now.
func (l *convLine) visible(now time.Time) string {
	if l.dur <= 0 {
		return l.prefix + l.text
	}
	el := now.Sub(l.start)
	if el >= l.dur {
		return l.prefix + l.text
	}
	r := []rune(l.text)
	n := int(float64(len(r)) * float64(el) / float64(l.dur))
	return l.prefix + string(r[:n])
}

// freeze stops the reveal at now.
func (l *convLine) freeze(now time.Time) {
	if l.dur > 0 {
		l.text = strings.TrimPrefix(l.visible(now), l.prefix)
		l.dur = 0
	}
}

// wrap splits s into chunks of at most width runes, keeping newlines.
func wrap(s string, width int) []string {
	var out []string
	for _, line := range strings.Split(s, "\n") {
		r := []rune(line)
		if len(r) == 0 {
			out = append(out, "")
			continue
		}
		for len(r) > width {
			out = append(out, string(r[:width]))
			r = r[width:]
		}
		out = append(out, string(r))
	}
	return out
}

func rule(width int) string { return strings.Repeat("─", width) }

// requestRedraw asks the render loop for a new frame.
func (a *app) requestRedraw() {
	select {
	case a.redraw <- struct{}{}:
	default:
	}
}

// renderLoop draws a frame on every change, and every 150 ms while
// something animates.
func (a *app) renderLoop() {
	tick := time.NewTicker(150 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-a.redraw:
		case <-tick.C:
			a.mu.Lock()
			busy := a.statusLocked() == "busy"
			a.mu.Unlock()
			if !busy {
				continue
			}
		}
		a.mu.Lock()
		frame := a.frameLocked(time.Now())
		a.mu.Unlock()
		a.outMu.Lock()
		if !a.exiting {
			os.Stdout.WriteString(frame)
		}
		a.outMu.Unlock()
	}
}

// frameLocked builds a whole screen: title, clear, lines.
func (a *app) frameLocked(now time.Time) string {
	if a.cx != nil {
		return a.codexFrameLocked(now)
	}
	cols, rows := termSize()
	title := "✳ Fake Claude"
	if a.statusLocked() == "busy" {
		f := []rune(spinFrames)
		title = string(f[int(now.Sub(a.born)/(150*time.Millisecond))%len(f)]) + " Fake Claude"
	}
	header := []string{"✻ Fake Claude Code " + a.version}
	header = append(header, wrap("  cwd: "+a.cwd, cols)...)
	header = append(header, "")
	bottom := a.bottomLocked(now, cols)
	var conv []string
	if a.dialog == nil || (a.dialog.kind != "trust" && a.dialog.kind != "bypass") {
		for _, l := range a.conv {
			conv = append(conv, wrap(l.visible(now), cols)...)
		}
	}
	avail := rows - len(header) - len(bottom)
	if avail < 0 {
		header = nil
		avail = rows - len(bottom)
	}
	if avail < 0 {
		bottom = bottom[len(bottom)-rows:]
		avail = 0
	}
	if len(conv) > avail {
		conv = conv[len(conv)-avail:]
	}
	lines := append(append(header, conv...), bottom...)
	var b strings.Builder
	fmt.Fprintf(&b, "\x1b]0;%s\a\x1b[H\x1b[2J", title)
	b.WriteString(strings.Join(lines, "\r\n"))
	return b.String()
}

// bottomLocked is the spinner, the queue note and the input box, or the
// dialog that replaces the box.
func (a *app) bottomLocked(now time.Time, cols int) []string {
	var out []string
	if d := a.dialog; d != nil {
		out = append(out, rule(cols))
		for _, l := range d.lines() {
			out = append(out, wrap(l, cols)...)
		}
		return out
	}
	if a.spinning {
		secs := int(now.Sub(a.turnStart) / time.Second)
		tokens := int(now.Sub(a.turnStart)/time.Millisecond) / 7
		out = append(out, "", fmt.Sprintf("✢ %s (%ds · ↓ %d tokens)", a.spinLabel, secs, tokens))
	}
	if a.queuedPromptsLocked() > 0 {
		out = append(out, "Press up to edit queued messages")
	}
	out = append(out, rule(cols))
	if len(a.input) == 0 && a.suggestion != "" {
		out = append(out, "❯ "+dimOn+a.suggestion+dimOff)
	} else {
		for i, l := range strings.Split(string(a.input), "\n") {
			p := "  "
			if i == 0 {
				p = "❯ "
			}
			out = append(out, wrap(p+l, cols)...)
		}
	}
	out = append(out, rule(cols), "  ? for shortcuts")
	return out
}

// dialog is a modal choice that replaces the input box.
type dialog struct {
	kind     string // trust, bypass, permission, question
	title    string // permission: "Create file", "Bash command", ...
	subject  string // permission: "create x.txt"
	header   string // question
	question string
	options  []string
	sel      int
	textOpt  int    // question: the option that is a text field once focused, or -1
	text     []rune // what was typed into it
	notes    bool   // codexq: Tab opened the notes field
	at, of   int    // codexq: "Question at/of"
	shownAt  time.Time
	debounce time.Duration
	result   chan int // the chosen option, 1-based; 0 to exit
}

// lines are the dialog's rows, with ❯ on the selected option.
func (d *dialog) lines() []string {
	switch d.kind {
	case "cxtrust", "hooksreview", "update", "approval", "newdlg":
		return d.codexLines()
	}
	var out []string
	opts := func() {
		for i, o := range d.options {
			mark := "  "
			if i == d.sel {
				mark = "❯ "
			}
			if d.kind == "question" && i == d.textOpt && len(d.text) > 0 {
				o = string(d.text)
			}
			out = append(out, fmt.Sprintf(" %s%d. %s", mark, i+1, o))
		}
	}
	switch d.kind {
	case "trust":
		// As 2.1.289 draws it: no numbers on the options.
		out = append(out,
			" Accessing workspace:", "",
			" "+d.subject, "",
			" Quick safety check: Is this a project you created or one you trust? (Like your own code, a",
			" well-known open source project, or work from your team). If not, take a moment to review what's in",
			" this folder first.", "",
			" Claude Code'll be able to read, edit, and execute files here.", "",
			" Security guide", "")
		for i, o := range d.options {
			mark := "  "
			if i == d.sel {
				mark = "❯ "
			}
			out = append(out, " "+mark+o)
		}
		out = append(out, "", " Enter to confirm · Esc to cancel")
	case "bypass":
		out = append(out, " WARNING: Claude Code running in Bypass Permissions mode")
		opts()
		out = append(out, " Enter to confirm · Esc to exit")
	case "permission":
		out = append(out, " "+d.title, " Do you want to "+d.subject+"?")
		opts()
		out = append(out, " Esc to cancel · Tab to amend")
	case "remote":
		out = append(out, " Remote Control", "", " This session is available in the Claude mobile app.", "")
		opts()
		out = append(out, "", " Enter to select · Esc to continue")
	case "codexq":
		// As Codex 0.160 draws request_user_input in plan mode.
		out = append(out, fmt.Sprintf("  Question %d/%d (%d unanswered)", d.at, d.of, d.of-d.at+1), "  "+d.question)
		for i, o := range d.options {
			mark := "  "
			if i == d.sel {
				mark = "› "
			}
			switch {
			case i == d.textOpt && d.notes:
				o += "  " + string(d.text) + "_"
			case i == d.textOpt:
				o += "  Optionally, add details in notes (tab)"
			}
			out = append(out, fmt.Sprintf("  %s%d. %s", mark, i+1, o))
		}
		// The last of several questions submits them all (T155).
		switch {
		case d.of == 1:
			out = append(out, "  tab to add notes | enter to submit answer | esc to interrupt")
		case d.at < d.of:
			out = append(out, "  tab to add notes | enter to submit answer | ←/→ to navigate questions | esc to interrupt")
		default:
			out = append(out, "  tab to add notes | enter to submit all | ←/→ to navigate questions | esc to interrupt")
		}
	case "question":
		out = append(out, " ☐ "+d.header, " "+d.question)
		opts()
		out = append(out, " Enter to select · ↑/↓ to navigate · Esc to cancel")
	}
	return out
}
