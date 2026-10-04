// Package emu wraps libghostty-vt, the terminal emulator the server keeps for
// every pane. The rest of termalator talks to this package, never to the
// bindings directly, so a binding API change stays in one place.
//
// Only the calls the skeleton needs exist so far. Rendering for attach
// (VT formatter), snapshots for server restart and the key/mouse encoders
// arrive with milestone M1; see docs/SPEC.md.
package emu

import (
	"fmt"

	"go.mitchellh.com/libghostty"
)

// Terminal is one emulated screen. It is not safe for concurrent use; the
// owning session serialises every call.
type Terminal struct {
	t *libghostty.Terminal
}

// New returns an emulator of the given size.
func New(cols, rows uint16) (*Terminal, error) {
	t, err := libghostty.NewTerminal(libghostty.WithSize(cols, rows))
	if err != nil {
		return nil, fmt.Errorf("emu: new terminal: %w", err)
	}
	return &Terminal{t: t}, nil
}

// Write feeds PTY output into the emulator.
func (t *Terminal) Write(p []byte) (int, error) { return t.t.Write(p) }

// PlainText returns the visible screen as trimmed plain text. Screen rules
// in the state detector match against this.
func (t *Terminal) PlainText() (string, error) {
	f, err := libghostty.NewFormatter(t.t,
		libghostty.WithFormatterFormat(libghostty.FormatterFormatPlain),
		libghostty.WithFormatterTrim(true),
	)
	if err != nil {
		return "", fmt.Errorf("emu: formatter: %w", err)
	}
	defer f.Close()
	return f.FormatString()
}

// Close frees the emulator.
func (t *Terminal) Close() { t.t.Close() }
