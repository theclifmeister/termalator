package main

import (
	"fmt"
	"testing"

	"go.mitchellh.com/libghostty"
)

func newTestTerm(t *testing.T, cols, rows uint16, opts ...libghostty.TerminalOption) *libghostty.Terminal {
	t.Helper()
	term, err := libghostty.NewTerminal(append([]libghostty.TerminalOption{libghostty.WithSize(cols, rows),
		libghostty.WithMaxScrollbackLines(10000)}, opts...)...)
	if err != nil {
		t.Fatal(err)
	}
	return term
}

// A snapshot-restored terminal must evolve exactly like the original when
// fed the same bytes and resizes afterwards.
func TestSnapshotThenResizeMatches(t *testing.T) {
	src := newTestTerm(t, 100, 30)
	for i := 1; i <= 200; i++ {
		fmt.Fprintf(src, "%d\r\n", i)
	}
	snap, err := src.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	mirror, err := decodeSnapshot(snap)
	if err != nil {
		t.Fatal(err)
	}
	if a, b := digest(src), digest(mirror); a != b {
		t.Fatalf("right after restore: %s != %s", a, b)
	}
	sm, _ := src.ScrollbackMaxLines()
	mm, _ := mirror.ScrollbackMaxLines()
	t.Logf("scrollback max lines: src=%v mirror=%v", deref(sm), deref(mm))
	src.Resize(70, 20, 8, 16)
	mirror.Resize(70, 20, 8, 16)
	if a, b := digest(src), digest(mirror); a != b {
		t.Errorf("after shrink: %s != %s", a, b)
	}
	src.Resize(130, 40, 8, 16)
	mirror.Resize(130, 40, 8, 16)
	if a, b := digest(src), digest(mirror); a != b {
		t.Errorf("after grow: %s != %s\nsrc:\n%s\nmirror:\n%s", a, b, plainText(src, false), plainText(mirror, false))
	}
}

func deref(p *uint) any {
	if p == nil {
		return "nil(unlimited/default)"
	}
	return *p
}

// Can the VT formatter output (with extras) act as a version-independent
// snapshot? Replay it into a fresh terminal and compare full state.
func TestVTReplayAsSnapshot(t *testing.T) {
	src := newTestTerm(t, 100, 30)
	for i := 1; i <= 200; i++ {
		fmt.Fprintf(src, "\x1b[3%dmline %d\x1b[0m 漢字 👋🏽\r\n", i%8, i)
	}
	// Some modes a TUI would set, plus a scroll region and alt screen.
	src.VTWrite([]byte("\x1b[?2004h\x1b[?1h\x1b[>1u\x1b[?1003h\x1b[?1006h\x1b[5;20r\x1b[?1049h\x1b[2;3Halt screen\x1b[1;1H"))
	dump := stateDump(src)
	dst := newTestTerm(t, 100, 30)
	dst.VTWrite([]byte(dump))
	if a, b := digest(src), digest(dst); a != b {
		t.Errorf("VT replay differs: %s vs %s", a, b)
	}
	{
		for _, m := range []struct {
			name string
			mode libghostty.Mode
		}{{"bracketed paste", libghostty.ModeBracketedPaste}, {"DECCKM", libghostty.ModeDECCKM}, {"any mouse", libghostty.ModeAnyMouse}, {"alt screen", libghostty.ModeAltScreenSave}} {
			x, _ := src.Mode(m.mode)
			y, _ := dst.Mode(m.mode)
			t.Logf("  %-16s src=%v replay=%v", m.name, x, y)
			if x != y {
				t.Errorf("mode %s differs", m.name)
			}
		}
		sx, _ := src.ScrollbackRows()
		dx, _ := dst.ScrollbackRows()
		kx, _ := src.KittyKeyboardFlags()
		ky, _ := dst.KittyKeyboardFlags()
		t.Logf("  scrollback rows src=%d replay=%d; kitty flags src=%d replay=%d", sx, dx, kx, ky)
		t.Logf("  screen text equal: %v", plainText(src, false) == plainText(dst, false))
		if sx != dx || kx != ky || plainText(src, false) != plainText(dst, false) {
			t.Errorf("replay lost state")
		}
	}
	// Leave the alt screen in both and compare the primary screen too
	// (scrollback and kitty flags are per screen).
	src.VTWrite([]byte("\x1b[?1049l"))
	dst.VTWrite([]byte("\x1b[?1049l"))
	sx, _ := src.ScrollbackRows()
	dx, _ := dst.ScrollbackRows()
	kx, _ := src.KittyKeyboardFlags()
	ky, _ := dst.KittyKeyboardFlags()
	t.Logf("primary: scrollback rows src=%d replay=%d; kitty flags src=%d replay=%d; digest %s vs %s", sx, dx, kx, ky, digest(src), digest(dst))
	// Known limitation (see FINDINGS.md): the VT formatter only encodes the
	// active screen, so the inactive primary screen's scrollback and kitty
	// flags are lost. The binary snapshot carries both screens.
	if sx == dx && kx == ky {
		t.Errorf("expected VT replay to lose the inactive screen; did libghostty change?")
	}
}
