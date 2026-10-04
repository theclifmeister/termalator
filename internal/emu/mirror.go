package emu

import "go.mitchellh.com/libghostty"

// What an attach client's mirror needs beyond feeding bytes: the modes it
// copies onto the outer terminal, its own viewport, and mode-2026 holds.

// Modes are the program's terminal modes that matter to an attach client.
type Modes struct {
	NormalMouse       bool // 1000
	ButtonMouse       bool // 1002
	AnyMouse          bool // 1003
	Focus             bool // 1004
	BracketedPaste    bool // 2004
	ColorSchemeReport bool // 2031
	AltScreen         bool // the alternate screen is active
}

// MouseTracking reports whether the program asked for any mouse reports.
func (m Modes) MouseTracking() bool { return m.NormalMouse || m.ButtonMouse || m.AnyMouse }

// Modes returns the program's current modes.
func (t *Terminal) Modes() Modes {
	mode := func(m libghostty.Mode) bool { on, _ := t.t.Mode(m); return on }
	scr, _ := t.t.ActiveScreen()
	return Modes{
		NormalMouse:       mode(libghostty.ModeNormalMouse),
		ButtonMouse:       mode(libghostty.ModeButtonMouse),
		AnyMouse:          mode(libghostty.ModeAnyMouse),
		Focus:             mode(libghostty.ModeFocusEvent),
		BracketedPaste:    mode(libghostty.ModeBracketedPaste),
		ColorSchemeReport: mode(libghostty.ModeColorSchemeReport),
		AltScreen:         scr == libghostty.ScreenAlternate,
	}
}

// ScrollViewport moves this terminal's viewport by delta rows (negative is
// up, into the scrollback). It changes nothing the program can see.
func (t *Terminal) ScrollViewport(delta int) { t.t.ScrollViewportDelta(delta) }

// ScrollViewportBottom puts the viewport back on the live screen.
func (t *Terminal) ScrollViewportBottom() { t.t.ScrollViewportBottom() }

// OnRenderHold registers fn for mode-2026 synchronized updates: held is
// true when the program starts one and false when it ends. fn runs inside
// Write. A Renderer given to fn's caller should Capture at hold start, so
// it keeps drawing the last complete frame.
func (t *Terminal) OnRenderHold(fn func(held bool)) {
	t.t.SetEffectRenderHold(func(_ *libghostty.Terminal, held bool) { fn(held) })
}
