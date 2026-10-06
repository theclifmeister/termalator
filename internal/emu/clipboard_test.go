package emu

import (
	"strings"
	"testing"
)

// TestSelectionText selects like a mouse drag, either way round, and
// joins soft-wrapped rows.
func TestSelectionText(t *testing.T) {
	term, err := New(10, 4)
	if err != nil {
		t.Fatal(err)
	}
	defer term.Close()
	term.Write([]byte("hello you\r\nsecond row\r\n0123456789abc"))
	if s, _ := term.SelectionText(); s != "" {
		t.Fatalf("no selection: %q", s)
	}
	for _, c := range []struct {
		x0, y0, x1, y1 int
		want           string
	}{
		{0, 0, 4, 0, "hello"},
		{6, 0, 5, 1, "you\nsecond"},
		{5, 1, 6, 0, "you\nsecond"},   // dragged up and left
		{0, 2, 2, 3, "0123456789abc"}, // soft-wrapped: one line
		{-3, 0, 99, 0, "hello you"},   // clamped to the screen
	} {
		if err := term.Select(c.x0, c.y0, c.x1, c.y1); err != nil {
			t.Fatal(err)
		}
		if got, err := term.SelectionText(); err != nil || got != c.want {
			t.Errorf("Select(%d,%d,%d,%d) = %q %v, want %q", c.x0, c.y0, c.x1, c.y1, got, err, c.want)
		}
	}
	term.ClearSelection()
	if s, _ := term.SelectionText(); s != "" {
		t.Fatalf("cleared: %q", s)
	}
}

// TestRendererDrawsSelection: selected cells, blanks included, are drawn
// in reverse video; the rest of the row is not.
func TestRendererDrawsSelection(t *testing.T) {
	term, _ := New(10, 2)
	defer term.Close()
	term.Write([]byte("ab cd"))
	r, _ := NewRenderer(10, 2)
	defer r.Close()
	if _, err := r.Frame(term, false); err != nil {
		t.Fatal(err)
	}
	term.Select(1, 0, 3, 0)
	r.Invalidate()
	b, err := r.Frame(term, false)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(b); !strings.Contains(got, "a\x1b[0;7mb c\x1b[0md") {
		t.Fatalf("frame %q: want b, the blank and c reversed", got)
	}
}

// TestOnClipboard: OSC 52 writes reach the callback decoded, with their
// clipboard; reads never do.
func TestOnClipboard(t *testing.T) {
	term, _ := New(20, 2)
	defer term.Close()
	type write struct {
		which byte
		data  string
	}
	var got []write
	term.OnClipboard(func(which byte, data []byte) { got = append(got, write{which, string(data)}) })
	term.Write([]byte("\x1b]52;c;aGVs"))
	term.Write([]byte("bG8=\x07x\x1b]52;p;d29ybGQ=\x1b\\"))
	term.Write([]byte("\x1b]52;c;?\x07"))
	term.Write([]byte("\x1b]52;c;\x07"))
	want := []write{{'c', "hello"}, {'p', "world"}, {'c', ""}}
	if len(got) != len(want) {
		t.Fatalf("writes %q, want %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("writes %q, want %q", got, want)
		}
	}
	if s, _ := term.Screen(); !strings.HasPrefix(s, "x") {
		t.Fatalf("screen %q", s)
	}
}

func TestOSC52(t *testing.T) {
	if got := string(OSC52(ClipboardStandard, []byte("hi\n"))); got != "\x1b]52;c;aGkK\a" {
		t.Fatalf("OSC52 = %q", got)
	}
	if got := string(OSC52(ClipboardStandard, nil)); got != "\x1b]52;c;\a" {
		t.Fatalf("clear = %q", got)
	}
	if OSC52(ClipboardStandard, make([]byte, MaxClipboard+1)) != nil {
		t.Fatal("over the cap: want nil")
	}
}
