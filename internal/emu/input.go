package emu

import (
	"fmt"

	"go.mitchellh.com/libghostty"
)

// Input encoding for attach clients. The client decodes what the outer
// terminal sends into keys, mouse events, focus changes and pastes, then
// encodes them again here against the modes of the program in the pane
// (its kitty flags, DECCKM, mouse tracking and format, bracketed paste).
// That is how Shift+Enter reaches a program as CSI 13;2u when it asked
// for kitty keys, and as plain CR when it didn't.

// Mods are key modifiers.
type Mods uint8

const (
	ModShift Mods = 1 << iota
	ModCtrl
	ModAlt
	ModSuper
)

// SpecialKey names a key that has no text of its own.
type SpecialKey uint8

const (
	KeyNone SpecialKey = iota
	KeyEnter
	KeyTab
	KeyBackspace
	KeyEscape
	KeySpace
	KeyUp
	KeyDown
	KeyLeft
	KeyRight
	KeyHome
	KeyEnd
	KeyPageUp
	KeyPageDown
	KeyInsert
	KeyDelete
	KeyKpEnter
	KeyF1
	KeyF2
	KeyF3
	KeyF4
	KeyF5
	KeyF6
	KeyF7
	KeyF8
	KeyF9
	KeyF10
	KeyF11
	KeyF12
)

var specialKeys = [...]libghostty.Key{
	KeyNone: libghostty.KeyUnidentified, KeyEnter: libghostty.KeyEnter,
	KeyTab: libghostty.KeyTab, KeyBackspace: libghostty.KeyBackspace,
	KeyEscape: libghostty.KeyEscape, KeySpace: libghostty.KeySpace,
	KeyUp: libghostty.KeyArrowUp, KeyDown: libghostty.KeyArrowDown,
	KeyLeft: libghostty.KeyArrowLeft, KeyRight: libghostty.KeyArrowRight,
	KeyHome: libghostty.KeyHome, KeyEnd: libghostty.KeyEnd,
	KeyPageUp: libghostty.KeyPageUp, KeyPageDown: libghostty.KeyPageDown,
	KeyInsert: libghostty.KeyInsert, KeyDelete: libghostty.KeyDelete,
	KeyKpEnter: libghostty.KeyNumpadEnter,
	KeyF1:      libghostty.KeyF1, KeyF2: libghostty.KeyF2, KeyF3: libghostty.KeyF3,
	KeyF4: libghostty.KeyF4, KeyF5: libghostty.KeyF5, KeyF6: libghostty.KeyF6,
	KeyF7: libghostty.KeyF7, KeyF8: libghostty.KeyF8, KeyF9: libghostty.KeyF9,
	KeyF10: libghostty.KeyF10, KeyF11: libghostty.KeyF11, KeyF12: libghostty.KeyF12,
}

// printableKeys maps the unshifted character of a US-layout key to its
// physical key, which the kitty protocol reports.
var printableKeys = func() map[rune]libghostty.Key {
	m := map[rune]libghostty.Key{
		'`': libghostty.KeyBackquote, '\\': libghostty.KeyBackslash,
		'[': libghostty.KeyBracketLeft, ']': libghostty.KeyBracketRight,
		',': libghostty.KeyComma, '=': libghostty.KeyEqual, '-': libghostty.KeyMinus,
		'.': libghostty.KeyPeriod, '\'': libghostty.KeyQuote, ';': libghostty.KeySemicolon,
		'/': libghostty.KeySlash,
	}
	letters := []libghostty.Key{libghostty.KeyA, libghostty.KeyB, libghostty.KeyC, libghostty.KeyD,
		libghostty.KeyE, libghostty.KeyF, libghostty.KeyG, libghostty.KeyH, libghostty.KeyI,
		libghostty.KeyJ, libghostty.KeyK, libghostty.KeyL, libghostty.KeyM, libghostty.KeyN,
		libghostty.KeyO, libghostty.KeyP, libghostty.KeyQ, libghostty.KeyR, libghostty.KeyS,
		libghostty.KeyT, libghostty.KeyU, libghostty.KeyV, libghostty.KeyW, libghostty.KeyX,
		libghostty.KeyY, libghostty.KeyZ}
	for i, k := range letters {
		m['a'+rune(i)] = k
	}
	digits := []libghostty.Key{libghostty.KeyDigit0, libghostty.KeyDigit1, libghostty.KeyDigit2,
		libghostty.KeyDigit3, libghostty.KeyDigit4, libghostty.KeyDigit5, libghostty.KeyDigit6,
		libghostty.KeyDigit7, libghostty.KeyDigit8, libghostty.KeyDigit9}
	for i, k := range digits {
		m['0'+rune(i)] = k
	}
	return m
}()

// Key is one key press, as decoded from the outer terminal.
type Key struct {
	// Special is set for named keys; otherwise Rune is the key's
	// unshifted character ('a' for Shift+A).
	Special SpecialKey
	Rune    rune
	Mods    Mods
	// Text is what the key types, if anything ("A" for Shift+A).
	Text string
}

// MouseButton is a mouse button or wheel direction.
type MouseButton uint8

const (
	MouseNone MouseButton = iota
	MouseLeft
	MouseMiddle
	MouseRight
	MouseWheelUp
	MouseWheelDown
	MouseWheelLeft
	MouseWheelRight
)

var mouseButtons = [...]libghostty.MouseButton{
	MouseNone: libghostty.MouseButtonUnknown, MouseLeft: libghostty.MouseButtonLeft,
	MouseMiddle: libghostty.MouseButtonMiddle, MouseRight: libghostty.MouseButtonRight,
	MouseWheelUp: libghostty.MouseButtonFour, MouseWheelDown: libghostty.MouseButtonFive,
	MouseWheelLeft: libghostty.MouseButtonSix, MouseWheelRight: libghostty.MouseButtonSeven,
}

// MouseAction is what happened to a button.
type MouseAction uint8

const (
	MousePress MouseAction = iota
	MouseRelease
	MouseMotion
)

// Mouse is one mouse event in pane cells (0-based).
type Mouse struct {
	Action MouseAction
	Button MouseButton
	Mods   Mods
	X, Y   int
}

// Encoder encodes input for the program in one terminal. It is not safe
// for concurrent use; callers serialise it with the terminal.
type Encoder struct {
	key   *libghostty.KeyEncoder
	kev   *libghostty.KeyEvent
	mouse *libghostty.MouseEncoder
	mev   *libghostty.MouseEvent
}

// NewEncoder returns an input encoder.
func NewEncoder() (*Encoder, error) {
	e := &Encoder{}
	var err error
	if e.key, err = libghostty.NewKeyEncoder(); err == nil {
		if e.kev, err = libghostty.NewKeyEvent(); err == nil {
			if e.mouse, err = libghostty.NewMouseEncoder(); err == nil {
				e.mev, err = libghostty.NewMouseEvent()
			}
		}
	}
	if err != nil {
		e.Close()
		return nil, fmt.Errorf("emu: input encoder: %w", err)
	}
	return e, nil
}

// Close frees the encoder.
func (e *Encoder) Close() {
	if e.mev != nil {
		e.mev.Close()
	}
	if e.mouse != nil {
		e.mouse.Close()
	}
	if e.kev != nil {
		e.kev.Close()
	}
	if e.key != nil {
		e.key.Close()
	}
}

func libMods(m Mods) libghostty.Mods {
	var mods libghostty.Mods
	if m&ModShift != 0 {
		mods |= libghostty.ModShift
	}
	if m&ModCtrl != 0 {
		mods |= libghostty.ModCtrl
	}
	if m&ModAlt != 0 {
		mods |= libghostty.ModAlt
	}
	if m&ModSuper != 0 {
		mods |= libghostty.ModSuper
	}
	return mods
}

// Key encodes a key press for t's program.
func (e *Encoder) Key(t *Terminal, k Key) ([]byte, error) {
	mods := libMods(k.Mods)
	key := libghostty.KeyUnidentified
	var unshifted rune
	switch {
	case k.Special != KeyNone && int(k.Special) < len(specialKeys):
		key = specialKeys[k.Special]
		if k.Special == KeySpace {
			unshifted = ' '
		}
	default:
		if pk, ok := printableKeys[k.Rune]; ok {
			key = pk
		}
		unshifted = k.Rune
	}
	text := k.Text
	if mods&(libghostty.ModCtrl|libghostty.ModAlt|libghostty.ModSuper) != 0 {
		text = "" // the encoder derives control and alt sequences itself
	}
	var consumed libghostty.Mods
	if text != "" && mods&libghostty.ModShift != 0 {
		consumed = libghostty.ModShift
	}
	e.kev.SetAction(libghostty.KeyActionPress)
	e.kev.SetKey(key)
	e.kev.SetMods(mods)
	e.kev.SetConsumedMods(consumed)
	e.kev.SetUTF8(text)
	e.kev.SetUnshiftedCodepoint(unshifted)
	e.key.SetOptFromTerminal(t.t)
	return e.key.Encode(e.kev)
}

// Mouse encodes a mouse event for t's program, in the tracking mode and
// format it asked for. It returns nothing when the program doesn't track
// that kind of event.
func (e *Encoder) Mouse(t *Terminal, m Mouse) ([]byte, error) {
	switch m.Action {
	case MouseRelease:
		e.mev.SetAction(libghostty.MouseActionRelease)
	case MouseMotion:
		e.mev.SetAction(libghostty.MouseActionMotion)
	default:
		e.mev.SetAction(libghostty.MouseActionPress)
	}
	if m.Button != MouseNone && int(m.Button) < len(mouseButtons) {
		e.mev.SetButton(mouseButtons[m.Button])
	} else {
		e.mev.ClearButton()
	}
	e.mev.SetMods(libMods(m.Mods))
	// The encoder works in pixels; the pane only has cells.
	e.mev.SetPosition(libghostty.MousePosition{
		X: float32(m.X*cellWidthPx + cellWidthPx/2),
		Y: float32(m.Y*cellHeightPx + cellHeightPx/2),
	})
	cols, rows := t.Size()
	e.mouse.SetOptFromTerminal(t.t)
	e.mouse.SetOptSize(libghostty.MouseEncoderSize{
		ScreenWidth: uint32(cols) * cellWidthPx, ScreenHeight: uint32(rows) * cellHeightPx,
		CellWidth: cellWidthPx, CellHeight: cellHeightPx})
	return e.mouse.Encode(e.mev)
}

// Paste encodes pasted text: bracketed if the program asked for it, and
// with unsafe control characters neutralised otherwise.
func Paste(t *Terminal, text []byte) ([]byte, error) {
	on, _ := t.t.Mode(libghostty.ModeBracketedPaste)
	return libghostty.PasteEncode(text, on)
}

// Focus encodes a focus change; nil when the program didn't ask for focus
// events (mode 1004).
func Focus(t *Terminal, gained bool) []byte {
	if on, _ := t.t.Mode(libghostty.ModeFocusEvent); !on {
		return nil
	}
	ev := libghostty.FocusLost
	if gained {
		ev = libghostty.FocusGained
	}
	b, err := libghostty.FocusEncode(ev)
	if err != nil {
		return nil
	}
	return b
}
