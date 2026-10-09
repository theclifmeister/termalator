package session

import (
	"bytes"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/theclifmeister/terminatr/internal/agent"
)

// typed follows keys sent to the pane in the agent's typed line, when
// the agent is a Typist tm launched, and tells it of each line submitted
// while it is idle. Keys sent while it isn't (an answer to a menu, text
// typed during a turn) leave the box unknown: the line is inexact.
func (s *Session) typed(p []byte) {
	rt := s.agentRT()
	if rt == nil || rt.observed {
		return
	}
	ty, ok := rt.a.(agent.Typist)
	if !ok {
		return
	}
	type line struct {
		text  string
		exact bool
	}
	var lines []line
	idle := rt.tr.State().State == agent.StateIdle
	rt.typedMu.Lock()
	if idle {
		rt.typed.feed(p, func(text string, exact bool) { lines = append(lines, line{text, exact}) })
	} else {
		rt.typed.reset()
		rt.typed.inexact = true
	}
	rt.typedMu.Unlock()
	if len(lines) == 0 {
		return
	}
	t := s.promptTarget(rt)
	for _, l := range lines {
		ty.Typed(t, l.text, l.exact)
	}
}

// typedLine rebuilds the line typed into an agent's prompt box from the
// keys sent to its pane, so an agent.Typist hears of the lines submitted
// by hand (docs/SPEC.md §8.6). It follows text, pastes, Backspace and
// Ctrl+U; any other key that edits or moves (arrows, Tab, Esc, control
// and Alt keys) makes the line inexact. Mouse reports, focus events and
// terminal replies leave it alone.
type typedLine struct {
	buf     []rune
	inexact bool
	pending []byte // an escape sequence or rune cut at a write's end
	paste   bool   // between the bracketed-paste markers
}

const maxTypedLine = 4096 // runes kept; a longer line only needs its start

var (
	pasteOpen  = []byte("\x1b[200~")
	pasteClose = []byte("\x1b[201~")
)

// reset forgets the line, as after Enter.
func (l *typedLine) reset() {
	l.buf, l.inexact, l.pending, l.paste = l.buf[:0], false, nil, false
}

// feed takes one write of keys and calls submit for each Enter outside a
// paste, with the line and whether it is exact.
func (l *typedLine) feed(p []byte, submit func(line string, exact bool)) {
	b := append(l.pending, p...)
	l.pending = nil
	for len(b) > 0 {
		if l.paste {
			end := bytes.Index(b, pasteClose)
			if end < 0 {
				// Keep a possible start of the closing marker for the next write.
				keep := min(len(b), len(pasteClose)-1)
				for keep > 0 && !bytes.HasPrefix(pasteClose, b[len(b)-keep:]) {
					keep--
				}
				l.text(b[:len(b)-keep])
				l.pending = append([]byte(nil), b[len(b)-keep:]...)
				return
			}
			l.text(b[:end])
			b = b[end+len(pasteClose):]
			l.paste = false
			continue
		}
		c, n := b[0], 1
		if c == 0x1b {
			var ok bool
			if c, n, ok = l.escape(b); !ok {
				l.pending = append([]byte(nil), b...)
				return
			}
		} else if c >= 0x20 && c != 0x7f {
			if !utf8.FullRune(b) {
				l.pending = append([]byte(nil), b...)
				return
			}
			var r rune
			r, n = utf8.DecodeRune(b)
			l.add(r)
			c = 0
		}
		b = b[n:]
		switch {
		case c == 0:
		case c == '\r' || c == '\n':
			submit(string(l.buf), !l.inexact)
			l.reset()
		case c == 0x7f || c == 0x08:
			if len(l.buf) > 0 {
				l.buf = l.buf[:len(l.buf)-1]
			}
		case c == 0x15 || c == 0x03: // Ctrl+U, Ctrl+C: the box is empty
			l.reset()
		default:
			l.inexact = true
		}
	}
}

// escape reads the escape sequence at b's start: the key it stands for
// as the byte feed handles (0 for none, 0x1b for one that makes the line
// inexact), its length, or false when b ends inside it. A lone ESC
// ending a write is the Esc key: consoles send a key's sequence whole.
func (l *typedLine) escape(b []byte) (byte, int, bool) {
	if len(b) == 1 {
		return 0x1b, 1, true
	}
	switch b[1] {
	case '[':
		if bytes.HasPrefix(b, pasteOpen) {
			l.paste = true
			return 0, len(pasteOpen), true
		}
		i := 2
		for i < len(b) && b[i] >= 0x20 && b[i] <= 0x3f {
			i++
		}
		if i == len(b) {
			return 0, 0, false
		}
		return csiKey(string(b[2:i]), b[i]), i + 1, true
	case 'O':
		if len(b) < 3 {
			return 0, 0, false
		}
		return 0x1b, 3, true
	case 0x1b:
		return 0x1b, 1, true // Esc, then another sequence
	}
	return 0x1b, 2, true // Alt with a key
}

// csiKey is the key ESC [ params final stands for: the kitty keyboard
// protocol's plain Enter and Backspace; nothing for mouse and focus
// reports and terminal replies; any other key is inexact (0x1b).
func csiKey(params string, final byte) byte {
	if params != "" && strings.ContainsRune("<?>", rune(params[0])) || final == 'I' || final == 'O' || final == 'M' {
		return 0
	}
	if final == 'u' {
		f := strings.Split(params, ";")
		code, err := strconv.Atoi(strings.Split(f[0], ":")[0])
		if err == nil && (len(f) == 1 || f[1] == "1") {
			switch code {
			case 13:
				return '\r'
			case 127:
				return 0x7f
			}
		}
	}
	return 0x1b
}

func (l *typedLine) text(b []byte) {
	for len(b) > 0 {
		r, n := utf8.DecodeRune(b)
		l.add(r)
		b = b[n:]
	}
}

func (l *typedLine) add(r rune) {
	if len(l.buf) < maxTypedLine {
		l.buf = append(l.buf, r)
	}
}
