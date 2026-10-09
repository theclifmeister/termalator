package session

import (
	"slices"
	"testing"
)

// TestTypedLine: the line submitted is rebuilt from keys as typed,
// pasted or split across writes; keys it can't follow make it inexact.
func TestTypedLine(t *testing.T) {
	type sub struct {
		line  string
		exact bool
	}
	for _, c := range []struct {
		name   string
		writes []string
		want   []sub
	}{
		{"typed", []string{"/clea", "x\x7fr", "\r"}, []sub{{"/clear", true}}},
		{"pasted", []string{"\x1b[200~/clear\x1b[201~", "\r"}, []sub{{"/clear", true}}},
		{"paste split", []string{"\x1b[200~a\rb\x1b[2", "01~c\r"}, []sub{{"a\rbc", true}}},
		{"paste opener split", []string{"\x1b[2", "00~x\x1b[201~\r"}, []sub{{"x", true}}},
		{"two lines", []string{"one\rtwo\n"}, []sub{{"one", true}, {"two", true}}},
		{"ctrl+u", []string{"junk\x15/new\r"}, []sub{{"/new", true}}},
		{"ctrl+c", []string{"junk\x03hi\r"}, []sub{{"hi", true}}},
		{"arrow", []string{"/c\x1b[B\r"}, []sub{{"/c", false}}},
		{"tab", []string{"/c\t\r"}, []sub{{"/c", false}}},
		{"esc", []string{"x", "\x1b", "/clear\r"}, []sub{{"x/clear", false}}},
		{"alt", []string{"\x1bbx\r"}, []sub{{"x", false}}},
		{"inexact ends at enter", []string{"\t\r/clear\r"}, []sub{{"", false}, {"/clear", true}}},
		{"mouse and focus", []string{"/cl\x1b[<0;3;4M\x1b[I\x1b[Oear\r"}, []sub{{"/clear", true}}},
		{"kitty", []string{"/clearx\x1b[127u\x1b[13u"}, []sub{{"/clear", true}}},
		{"kitty shift+enter", []string{"a\x1b[13;2ub\r"}, []sub{{"ab", false}}},
		{"utf-8 split", []string{"/é"[:2], "/é"[2:] + "\r"}, []sub{{"/é", true}}},
	} {
		var l typedLine
		var got []sub
		for _, w := range c.writes {
			l.feed([]byte(w), func(line string, exact bool) { got = append(got, sub{line, exact}) })
		}
		if !slices.Equal(got, c.want) {
			t.Errorf("%s: %+v, want %+v", c.name, got, c.want)
		}
	}
}
