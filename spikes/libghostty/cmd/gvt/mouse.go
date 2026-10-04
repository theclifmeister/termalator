package main

import (
	"log"

	uv "github.com/charmbracelet/ultraviolet"
	"go.mitchellh.com/libghostty"
)

// Fake cell geometry: the encoders work in pixels, we only know cells.
const cellW, cellH = 8, 16

// outerModes mirrors the inner program's mouse/focus wishes onto the outer
// terminal, so the outer terminal only reports mouse when the app wants it
// (and otherwise keeps native selection). Called with c.mu held.
func (c *client) outerModes() []byte {
	want := map[int]bool{}
	for _, m := range []struct {
		mode libghostty.Mode
		num  int
	}{{libghostty.ModeNormalMouse, 1000}, {libghostty.ModeButtonMouse, 1002},
		{libghostty.ModeAnyMouse, 1003}, {libghostty.ModeFocusEvent, 1004}} {
		on, _ := c.mirror.Mode(m.mode)
		want[m.num] = on
	}
	// We always ask the outer terminal for SGR coordinates when tracking.
	want[1006] = want[1000] || want[1002] || want[1003]
	var b []byte
	for _, n := range []int{1000, 1002, 1003, 1004, 1006} {
		if c.outer[n] != want[n] {
			c.outer[n] = want[n]
			if want[n] {
				b = append(b, "\x1b[?"...)
				b = appendInt(b, n)
				b = append(b, 'h')
			} else {
				b = append(b, "\x1b[?"...)
				b = appendInt(b, n)
				b = append(b, 'l')
			}
		}
	}
	return b
}

func appendInt(b []byte, n int) []byte { return append(b, []byte(itoa(n))...) }

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var d []byte
	for n > 0 {
		d = append([]byte{byte('0' + n%10)}, d...)
		n /= 10
	}
	return string(d)
}

func mouseMods(m uv.KeyMod) libghostty.Mods {
	var mods libghostty.Mods
	if m.Contains(uv.ModShift) {
		mods |= libghostty.ModShift
	}
	if m.Contains(uv.ModCtrl) {
		mods |= libghostty.ModCtrl
	}
	if m.Contains(uv.ModAlt) {
		mods |= libghostty.ModAlt
	}
	return mods
}

func mouseButton(b uv.MouseButton) (libghostty.MouseButton, bool) {
	switch b {
	case uv.MouseLeft:
		return libghostty.MouseButtonLeft, true
	case uv.MouseMiddle:
		return libghostty.MouseButtonMiddle, true
	case uv.MouseRight:
		return libghostty.MouseButtonRight, true
	case uv.MouseWheelUp:
		return libghostty.MouseButtonFour, true
	case uv.MouseWheelDown:
		return libghostty.MouseButtonFive, true
	case uv.MouseWheelLeft:
		return libghostty.MouseButtonSix, true
	case uv.MouseWheelRight:
		return libghostty.MouseButtonSeven, true
	}
	return libghostty.MouseButtonUnknown, false
}

// handleMouse re-encodes an outer mouse event for the inner program, using
// the inner program's tracking mode and format. If the app is not tracking
// the mouse, the wheel scrolls this client's own viewport instead.
func (c *client) handleMouse(ev uv.Event) {
	var m uv.Mouse
	action := libghostty.MouseActionPress
	switch e := ev.(type) {
	case uv.MouseClickEvent:
		m = uv.Mouse(e)
	case uv.MouseReleaseEvent:
		m, action = uv.Mouse(e), libghostty.MouseActionRelease
	case uv.MouseMotionEvent:
		m, action = uv.Mouse(e), libghostty.MouseActionMotion
	case uv.MouseWheelEvent:
		m = uv.Mouse(e)
	default:
		return
	}
	c.mu.Lock()
	tracking, _ := c.mirror.MouseTracking()
	if !tracking {
		if m.Button == uv.MouseWheelUp || m.Button == uv.MouseWheelDown {
			d := 3
			if m.Button == uv.MouseWheelUp {
				d = -3
			}
			c.mirror.ScrollViewportDelta(d)
			c.scrolled = true
		}
		c.mu.Unlock()
		c.poke()
		return
	}
	c.mev.SetAction(action)
	if btn, ok := mouseButton(m.Button); ok {
		c.mev.SetButton(btn)
	} else {
		c.mev.ClearButton()
	}
	c.mev.SetMods(mouseMods(m.Mod))
	c.mev.SetPosition(libghostty.MousePosition{X: float32(m.X*cellW + cellW/2), Y: float32(m.Y*cellH + cellH/2)})
	c.menc.SetOptFromTerminal(c.mirror)
	c.menc.SetOptSize(libghostty.MouseEncoderSize{
		ScreenWidth: uint32(c.r.outCols) * cellW, ScreenHeight: uint32(c.r.outRows) * cellH,
		CellWidth: cellW, CellHeight: cellH})
	b, err := c.menc.Encode(c.mev)
	c.mu.Unlock()
	if err == nil && len(b) > 0 {
		log.Printf("mouse %v -> %q", ev, b)
		c.send(msgInput, b)
	}
}

func (c *client) handleFocus(gained bool) {
	c.mu.Lock()
	on, _ := c.mirror.Mode(libghostty.ModeFocusEvent)
	c.mu.Unlock()
	if !on {
		return
	}
	ev := libghostty.FocusLost
	if gained {
		ev = libghostty.FocusGained
	}
	if b, err := libghostty.FocusEncode(ev); err == nil {
		c.send(msgInput, b)
	}
}
