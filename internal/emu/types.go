package emu

import "encoding/base64"

// The emulator's portable vocabulary: options, schemes, modes, input
// events and OSC 52. It has no libghostty in it, so it builds without cgo
// (emu_nocgo.go), where the rest of this package is a stub.

// DefaultScrollback is the number of scrollback lines a pane keeps.
const DefaultScrollback = 10000

// Options configure a new Terminal.
type Options struct {
	Cols, Rows uint16
	// Scrollback is the scrollback limit in lines; 0 means DefaultScrollback.
	Scrollback uint
	// WritePty receives the emulator's answers to terminal queries. Only
	// the server's emulator sets it. It is called synchronously from Write
	// and must not block.
	WritePty func([]byte)
	// Xtversion is the name reported for XTVERSION queries.
	Xtversion string
	// ColorScheme answers colour-scheme queries (CSI ? 996 n) when
	// WritePty is set: the scheme, and false while it is unknown (the
	// query then goes unanswered).
	ColorScheme func() (Scheme, bool)
}

// Scheme is a light or dark colour scheme (mode 2031 reports).
type Scheme uint8

const (
	SchemeDark  Scheme = 1
	SchemeLight Scheme = 2
)

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

// The clipboards of OSC 52, by their letter there.
const (
	ClipboardStandard  = 'c'
	ClipboardPrimary   = 'p'
	ClipboardSelection = 's'
)

// MaxClipboard caps the text one OSC 52 write carries, so a runaway
// selection or program can't flood the outer terminal. Some terminals
// take less (tmux and xterm by default around 100 KB).
const MaxClipboard = 1 << 20

// OSC52 is the sequence that puts data on the outer terminal's clipboard
// which (Clipboard*), or clears it when data is empty; nil when data is
// over MaxClipboard.
func OSC52(which byte, data []byte) []byte {
	if len(data) > MaxClipboard {
		return nil
	}
	b := append([]byte("\x1b]52;"), which, ';')
	b = base64.StdEncoding.AppendEncode(b, data)
	return append(b, '\a')
}
