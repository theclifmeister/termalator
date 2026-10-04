package emu

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
)

func newTerm(t *testing.T, cols, rows uint16) *Terminal {
	t.Helper()
	term, err := New(cols, rows)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(term.Close)
	return term
}

func TestKeyEncodingFollowsProgramModes(t *testing.T) {
	enc, err := NewEncoder()
	if err != nil {
		t.Fatal(err)
	}
	defer enc.Close()
	term := newTerm(t, 80, 24)
	shiftEnter := Key{Special: KeyEnter, Mods: ModShift}
	cases := []struct {
		name string
		k    Key
		want string
	}{
		{"letter", Key{Rune: 'a', Text: "a"}, "a"},
		{"shifted letter", Key{Rune: 'a', Mods: ModShift, Text: "A"}, "A"},
		{"ctrl-c", Key{Rune: 'c', Mods: ModCtrl}, "\x03"},
		{"enter", Key{Special: KeyEnter}, "\r"},
		{"shift-enter, legacy", shiftEnter, "\x1b[27;2;13~"},
		{"up", Key{Special: KeyUp}, "\x1b[A"},
	}
	for _, c := range cases {
		got, err := enc.Key(term, c.k)
		if err != nil || string(got) != c.want {
			t.Errorf("%s: %q, %v; want %q", c.name, got, err, c.want)
		}
	}
	// The program asks for application cursor keys, then kitty
	// "disambiguate".
	term.Write([]byte("\x1b[?1h"))
	if got, _ := enc.Key(term, Key{Special: KeyUp}); string(got) != "\x1bOA" {
		t.Errorf("up in DECCKM: %q", got)
	}
	term.Write([]byte("\x1b[>1u"))
	if got, _ := enc.Key(term, shiftEnter); string(got) != "\x1b[13;2u" {
		t.Errorf("shift-enter with kitty flags: %q", got)
	}
}

func TestMouseFocusPaste(t *testing.T) {
	enc, err := NewEncoder()
	if err != nil {
		t.Fatal(err)
	}
	defer enc.Close()
	term := newTerm(t, 80, 24)
	wheel := Mouse{Action: MousePress, Button: MouseWheelUp, X: 4, Y: 2}
	if got, _ := enc.Mouse(term, wheel); len(got) != 0 {
		t.Errorf("wheel without tracking: %q", got)
	}
	if got := Focus(term, true); got != nil {
		t.Errorf("focus without 1004: %q", got)
	}
	if got, _ := Paste(term, []byte("a\nb")); string(got) != "a\rb" {
		t.Errorf("plain paste: %q", got)
	}
	term.Write([]byte("\x1b[?1000h\x1b[?1006h\x1b[?1004h\x1b[?2004h"))
	m := term.Modes()
	if !m.MouseTracking() || !m.Focus || !m.BracketedPaste || m.AltScreen {
		t.Errorf("modes: %+v", m)
	}
	if got, _ := enc.Mouse(term, wheel); string(got) != "\x1b[<64;5;3M" {
		t.Errorf("wheel: %q", got)
	}
	if got := Focus(term, false); string(got) != "\x1b[O" {
		t.Errorf("focus lost: %q", got)
	}
	if got, _ := Paste(term, []byte("a\nb")); string(got) != "\x1b[200~a\nb\x1b[201~" {
		t.Errorf("bracketed paste: %q", got)
	}
	term.Write([]byte("\x1b[?1049h"))
	if !term.Modes().AltScreen {
		t.Error("alt screen not reported")
	}
}

func TestColorScheme(t *testing.T) {
	var replies bytes.Buffer
	scheme, known := SchemeDark, false
	term, err := NewWith(Options{Cols: 80, Rows: 24,
		WritePty:    func(b []byte) { replies.Write(b) },
		ColorScheme: func() (Scheme, bool) { return scheme, known },
	})
	if err != nil {
		t.Fatal(err)
	}
	defer term.Close()
	term.Write([]byte("\x1b[?996n"))
	if replies.Len() != 0 {
		t.Errorf("answered while the scheme is unknown: %q", replies.String())
	}
	known = true
	term.Write([]byte("\x1b[?996n"))
	if got := replies.String(); got != "\x1b[?997;1n" {
		t.Errorf("dark: %q", got)
	}
	replies.Reset()
	scheme = SchemeLight
	term.Write([]byte("\x1b[?996n"))
	if got := replies.String(); got != "\x1b[?997;2n" {
		t.Errorf("light: %q", got)
	}
	if got := string(SchemeReport(SchemeLight)); got != "\x1b[?997;2n" {
		t.Errorf("report: %q", got)
	}
	term.Write([]byte("\x1b[?2031h"))
	if !term.Modes().ColorSchemeReport {
		t.Error("2031 not reported")
	}
}

// replay feeds a renderer's frames into an outer terminal and returns its
// screen.
func replay(t *testing.T, outer *Terminal, frames ...[]byte) string {
	t.Helper()
	for _, f := range frames {
		outer.Write(f)
	}
	s, err := outer.Screen()
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestRendererDrawsAndCrops(t *testing.T) {
	pane := newTerm(t, 20, 6)
	outer := newTerm(t, 20, 6)
	r, err := NewRenderer(20, 6)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	pane.Write([]byte("\x1b[1mbold\x1b[0m \x1b[31mred\x1b[0m 日本\r\nline 2"))
	f, err := r.Frame(pane, false)
	if err != nil || f == nil {
		t.Fatalf("first frame: %q %v", f, err)
	}
	if !bytes.HasPrefix(f, []byte("\x1b[?2026h")) || !bytes.HasSuffix(f, []byte("\x1b[?2026l")) {
		t.Errorf("frame not wrapped in 2026: %q", f)
	}
	want, _ := pane.Screen()
	if got := replay(t, outer, f); got != want {
		t.Errorf("outer:\n%s\nwant:\n%s", got, want)
	}
	if f, _ := r.Frame(pane, false); f != nil {
		t.Errorf("unchanged pane drew %q", f)
	}
	// Only the dirty row is redrawn.
	pane.Write([]byte("!"))
	f, _ = r.Frame(pane, false)
	if bytes.Contains(f, []byte("bold")) || !bytes.Contains(f, []byte("line 2!")) {
		t.Errorf("dirty frame: %q", f)
	}
	want, _ = pane.Screen()
	if got := replay(t, outer, f); got != want {
		t.Errorf("outer after update:\n%s\nwant:\n%s", got, want)
	}

	// A smaller window shows the rows around the cursor.
	small := newTerm(t, 10, 3)
	r.SetSize(10, 3)
	pane.Write([]byte("\r\nthree\r\nfour\r\nfive"))
	f, _ = r.Frame(pane, false)
	if r.Top() != 2 {
		t.Errorf("top = %d, want 2", r.Top())
	}
	if got := replay(t, small, f); got != "three\nfour\nfive" {
		t.Errorf("cropped:\n%s", got)
	}
}

func TestRendererHoldsSyncUpdates(t *testing.T) {
	pane := newTerm(t, 20, 4)
	r, err := NewRenderer(20, 4)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	held := false
	pane.OnRenderHold(func(h bool) {
		if h {
			r.Capture(pane)
		}
		held = h
	})
	pane.Write([]byte("before"))
	r.Frame(pane, false)
	pane.Write([]byte("\x1b[?2026h\r\x1b[Khalf"))
	if !held {
		t.Fatal("no hold")
	}
	if f, _ := r.Frame(pane, held); bytes.Contains(f, []byte("half")) {
		t.Errorf("drew a torn frame: %q", f)
	}
	pane.Write([]byte(" done\x1b[?2026l"))
	if held {
		t.Fatal("hold not released")
	}
	if f, _ := r.Frame(pane, held); !bytes.Contains(f, []byte("half done")) {
		t.Errorf("frame after the hold: %q", f)
	}
}

// A mirror restored from a snapshot must trim its scrollback exactly like
// the server's emulator, or the two drift apart once output goes on.
func TestMirrorTracksServerPastScrollbackLimit(t *testing.T) {
	for _, n := range []int{100, 800, DefaultScrollback + 1000} {
		src := newTerm(t, 80, 24)
		for i := 1; i <= n; i++ {
			fmt.Fprintf(src, "row %d\r\n", i)
		}
		snap, err := src.Snapshot()
		if err != nil {
			t.Fatal(err)
		}
		mirror, err := Decode(snap)
		if err != nil {
			t.Fatal(err)
		}
		defer mirror.Close()
		for _, term := range []*Terminal{src, mirror} {
			for j := 0; j < DefaultScrollback/2; j++ {
				fmt.Fprintf(term, "more %d\r\n", j)
			}
		}
		a, _ := src.Digest()
		b, _ := mirror.Digest()
		if a != b {
			t.Errorf("after %d rows: mirror digest %s, server %s", n, b, a)
		}
		text, _ := src.PlainText()
		if kept := strings.Count(text, "\n"); n > DefaultScrollback && kept < DefaultScrollback*9/10 {
			t.Errorf("after %d rows the server kept only %d", n, kept)
		}
	}
}

// A client resynced mid-firehose (a fresh snapshot, then the rest of the
// stream) ends equal to the server, wherever the snapshot was cut.
func TestMirrorResyncMidStream(t *testing.T) {
	var all bytes.Buffer
	for i := 1; i <= 3*DefaultScrollback; i++ {
		fmt.Fprintf(&all, "firehose-row-%d-padding\r\n", i)
	}
	all.WriteString("\x1b[1mdone\x1b[0m\r\n$ ")
	data := all.Bytes()
	for _, cut := range []int{1000, len(data) / 3, len(data) - 10} {
		src := newTerm(t, 80, 24)
		src.Write(data[:cut])
		snap, _ := src.Snapshot()
		mirror, err := Decode(snap)
		if err != nil {
			t.Fatal(err)
		}
		defer mirror.Close()
		src.Write(data[cut:])
		mirror.Write(data[cut:])
		a, _ := src.Digest()
		b, _ := mirror.Digest()
		if a != b {
			t.Errorf("snapshot at byte %d: mirror digest %s, server %s", cut, b, a)
		}
		// Recent history still counts.
		mirror.Write([]byte("\x1b[5;1Hchanged"))
		if c, _ := mirror.Digest(); c == a {
			t.Errorf("digest ignores a changed screen row")
		}
	}
}

// TestRendererSharedRect: a renderer given a rectangle of a shared
// window draws there and nowhere else, erases only up to its edge, and
// leaves the cursor to the caller.
func TestRendererSharedRect(t *testing.T) {
	pane, err := New(10, 3)
	if err != nil {
		t.Fatal(err)
	}
	defer pane.Close()
	pane.Write([]byte("abc\r\nde\x1b[31mf\x1b[0m"))

	outer, err := New(30, 6)
	if err != nil {
		t.Fatal(err)
	}
	defer outer.Close()
	for range 6 {
		outer.Write([]byte(strings.Repeat("x", 30)))
	}

	r, err := NewRenderer(30, 6)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	r.SetRect(5, 1, 10, 3)
	b, err := r.Frame(pane, false)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(b, []byte("\x1b[2J")) || bytes.Contains(b, []byte("\x1b[K")) || bytes.Contains(b, []byte("\x1b[?2026")) {
		t.Fatalf("shared frame clears the window or syncs it: %q", b)
	}
	outer.Write(b)
	got, _ := outer.Screen()
	want := strings.Join([]string{
		strings.Repeat("x", 30),
		"xxxxxabc       xxxxxxxxxxxxxxx",
		"xxxxxdef       xxxxxxxxxxxxxxx",
		"xxxxx          xxxxxxxxxxxxxxx",
		strings.Repeat("x", 30),
		strings.Repeat("x", 30),
	}, "\n")
	if got != want {
		t.Fatalf("outer screen:\n%s\nwant:\n%s", got, want)
	}
	// The cursor sits after "def", offset by the rectangle.
	if c := string(r.Cursor()); !strings.HasPrefix(c, "\x1b[3;9H") {
		t.Fatalf("cursor %q", c)
	}
	// Nothing changed: no frame.
	if b, _ := r.Frame(pane, false); b != nil {
		t.Fatalf("idle frame %q", b)
	}
	// Back to the whole window: a full frame again.
	r.SetSize(30, 6)
	if b, _ := r.Frame(pane, false); !bytes.Contains(b, []byte("\x1b[2J")) {
		t.Fatalf("full-window frame %q", b)
	}
}
