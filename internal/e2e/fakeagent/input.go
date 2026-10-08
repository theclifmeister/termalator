//go:build unix

package main

import (
	"bytes"
	"os"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// keyKind is a decoded key.
type keyKind int

const (
	kNone keyKind = iota
	kRune
	kEnter
	kEsc
	kBackspace
	kUp
	kDown
	kTab
	kCtrlC
	kCtrlU
	kPaste
)

type key struct {
	kind keyKind
	r    rune
	text string // kPaste
}

// escWait is how long a lone ESC waits for the rest of a sequence.
const escWait = 30 * time.Millisecond

var (
	pasteStart = []byte("\x1b[200~")
	pasteEnd   = []byte("\x1b[201~")
)

// parseKey decodes the first key in b. It returns the key (kNone for
// ignored input), the bytes used, and whether b held a whole key. When
// flush is set, an incomplete escape sequence is read as Esc.
func parseKey(b []byte, flush bool) (key, int, bool) {
	c := b[0]
	if c == 0x1b {
		if len(b) == 1 {
			if flush {
				return key{kind: kEsc}, 1, true
			}
			return key{}, 0, false
		}
		switch b[1] {
		case '[':
			if bytes.HasPrefix(b, pasteStart) {
				end := bytes.Index(b[len(pasteStart):], pasteEnd)
				if end < 0 {
					return key{}, 0, false // a paste never times out
				}
				text := string(b[len(pasteStart) : len(pasteStart)+end])
				text = strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\r", "\n")
				return key{kind: kPaste, text: text}, len(pasteStart) + end + len(pasteEnd), true
			}
			i := 2
			for i < len(b) && b[i] >= 0x20 && b[i] <= 0x3f {
				i++
			}
			if i == len(b) {
				if flush {
					return key{kind: kEsc}, 1, true
				}
				return key{}, 0, false
			}
			return csiKey(string(b[2:i]), b[i]), i + 1, true
		case 'O':
			if len(b) < 3 {
				if flush {
					return key{kind: kEsc}, 1, true
				}
				return key{}, 0, false
			}
			switch b[2] {
			case 'A':
				return key{kind: kUp}, 3, true
			case 'B':
				return key{kind: kDown}, 3, true
			}
			return key{}, 3, true
		}
		return key{kind: kEsc}, 1, true
	}
	switch c {
	case '\r', '\n':
		return key{kind: kEnter}, 1, true
	case 0x7f, 0x08:
		return key{kind: kBackspace}, 1, true
	case 0x03:
		return key{kind: kCtrlC}, 1, true
	case 0x15:
		return key{kind: kCtrlU}, 1, true
	case '\t':
		return key{kind: kTab}, 1, true
	}
	if c < 0x20 {
		return key{}, 1, true
	}
	if !utf8.FullRune(b) {
		if flush {
			return key{}, len(b), true
		}
		return key{}, 0, false
	}
	r, n := utf8.DecodeRune(b)
	if r == utf8.RuneError {
		return key{}, n, true
	}
	return key{kind: kRune, r: r}, n, true
}

// csiKey decodes ESC [ params final.
func csiKey(params string, final byte) key {
	if strings.HasPrefix(params, "<") || strings.HasPrefix(params, "?") || strings.HasPrefix(params, ">") {
		return key{} // mouse and replies
	}
	switch final {
	case 'A':
		return key{kind: kUp}
	case 'B':
		return key{kind: kDown}
	case 'u':
		fields := strings.Split(params, ";")
		code, err := strconv.Atoi(strings.Split(fields[0], ":")[0])
		if err != nil {
			return key{}
		}
		mods := 1
		if len(fields) > 1 {
			sub := strings.Split(fields[1], ":")
			if m, err := strconv.Atoi(sub[0]); err == nil {
				mods = m
			}
			if len(sub) > 1 && sub[1] == "3" {
				return key{} // release
			}
		}
		ctrl := (mods-1)&4 != 0
		switch {
		case code == 27:
			return key{kind: kEsc}
		case code == 13:
			return key{kind: kEnter}
		case code == 127 || code == 8:
			return key{kind: kBackspace}
		case code == 9:
			return key{kind: kTab}
		case ctrl && code == 'c':
			return key{kind: kCtrlC}
		case ctrl && code == 'u':
			return key{kind: kCtrlU}
		case ctrl:
			return key{}
		case code >= 0x20:
			return key{kind: kRune, r: rune(code)}
		}
	}
	return key{}
}

// decodeKeys decodes every whole key in b and returns the rest.
func decodeKeys(b []byte, flush bool, emit func(key)) []byte {
	for len(b) > 0 {
		k, n, ok := parseKey(b, flush)
		if !ok {
			break
		}
		b = b[n:]
		if k.kind != kNone {
			emit(k)
		}
	}
	return b
}

// inputLoop reads stdin and hands decoded keys to handleKey. EOF (the
// PTY closed) ends the session.
func (a *app) inputLoop() {
	ch := make(chan []byte)
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := os.Stdin.Read(buf)
			if n > 0 {
				ch <- append([]byte(nil), buf[:n]...)
			}
			if err != nil {
				close(ch)
				return
			}
		}
	}()
	var pending []byte
	var timer <-chan time.Time
	for {
		flush := false
		select {
		case b, ok := <-ch:
			if !ok {
				a.exit(0, "other")
				return
			}
			pending = append(pending, b...)
		case <-timer:
			flush = true
		}
		pending = decodeKeys(pending, flush, a.handleKey)
		timer = nil
		if len(pending) > 0 {
			timer = time.After(escWait)
		}
	}
}
