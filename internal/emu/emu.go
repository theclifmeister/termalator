// Package emu wraps libghostty-vt, the terminal emulator the server keeps for
// every pane. The rest of termalator talks to this package, never to the
// bindings directly, so a binding API change stays in one place.
//
// The server's emulator is authoritative: it is the only one that answers
// terminal queries (DA, DSR, XTVERSION, size reports), through the WritePty
// callback. Attach clients rebuild a mirror from Snapshot and must not set
// WritePty, or every query would be answered once per client.
package emu

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	"go.mitchellh.com/libghostty"
)

// Cell size reported to programs that ask for pixel sizes (CSI 14/16 t).
// The server has no font; these are conventional values.
const (
	cellWidthPx  = 8
	cellHeightPx = 16
)

// DefaultScrollback is the number of scrollback lines a pane keeps.
const DefaultScrollback = 10000

// Terminal is one emulated screen. It is not safe for concurrent use; the
// owning session serialises every call.
type Terminal struct {
	t *libghostty.Terminal
}

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

func (s Scheme) lib() libghostty.ColorScheme {
	if s == SchemeLight {
		return libghostty.ColorSchemeLight
	}
	return libghostty.ColorSchemeDark
}

// SchemeReport is the unsolicited report (CSI ? 997 ; 1|2 n) a terminal
// sends a program that enabled mode 2031 when the scheme changes.
func SchemeReport(s Scheme) []byte {
	b, err := libghostty.ColorSchemeReportEncode(s.lib())
	if err != nil {
		return nil
	}
	return b
}

// New returns an emulator of the given size with default options.
func New(cols, rows uint16) (*Terminal, error) {
	return NewWith(Options{Cols: cols, Rows: rows})
}

// NewWith returns an emulator configured by opts.
func NewWith(o Options) (*Terminal, error) {
	sb := o.Scrollback
	if sb == 0 {
		sb = DefaultScrollback
	}
	opts := []libghostty.TerminalOption{
		libghostty.WithSize(o.Cols, o.Rows),
		// Snapshots then also carry half-received escape sequences, so a
		// mirror restored mid-sequence parses the rest of it correctly.
		libghostty.WithContinuationMaxBytes(4096),
	}
	if o.WritePty != nil {
		w := o.WritePty
		opts = append(opts,
			libghostty.WithWritePty(func(_ *libghostty.Terminal, data []byte) {
				w(append([]byte(nil), data...))
			}),
			libghostty.WithSizeReport(func(t *libghostty.Terminal) (libghostty.SizeReportSize, bool) {
				cols, _ := t.Cols()
				rows, _ := t.Rows()
				return libghostty.SizeReportSize{Rows: rows, Columns: cols,
					CellWidth: cellWidthPx, CellHeight: cellHeightPx}, true
			}),
		)
		if o.ColorScheme != nil {
			fn := o.ColorScheme
			opts = append(opts, libghostty.WithColorScheme(func(*libghostty.Terminal) (libghostty.ColorScheme, bool) {
				s, ok := fn()
				return s.lib(), ok
			}))
		}
		if o.Xtversion != "" {
			name := o.Xtversion
			opts = append(opts, libghostty.WithXtversion(func(*libghostty.Terminal) string { return name }))
		}
	}
	t, err := libghostty.NewTerminal(opts...)
	if err != nil {
		return nil, fmt.Errorf("emu: new terminal: %w", err)
	}
	if err := setScrollback(t, sb); err != nil {
		t.Close()
		return nil, err
	}
	return &Terminal{t: t}, nil
}

// setScrollback limits scrollback by lines only. libghostty also has a
// byte limit, on by default and small (a few hundred 80-column rows);
// a terminal restored from a snapshot doesn't carry it over, so a mirror
// and the server would trim at different points.
func setScrollback(t *libghostty.Terminal, lines uint) error {
	err := t.SetScrollbackMaxBytes(nil)
	if err == nil {
		err = t.SetScrollbackMaxLines(&lines)
	}
	if err != nil {
		return fmt.Errorf("emu: scrollback limit: %w", err)
	}
	return nil
}

// Decode rebuilds a terminal from a Snapshot. The result has no effects
// wired (no WritePty), which is what a client mirror wants, and the
// scrollback limit of a server pane (DefaultScrollback).
func Decode(snapshot []byte) (*Terminal, error) {
	dec, err := libghostty.NewSnapshotDecoderBytesCopy(snapshot)
	if err != nil {
		return nil, fmt.Errorf("emu: snapshot decoder: %w", err)
	}
	defer dec.Close()
	t, err := dec.Decode()
	if err != nil {
		return nil, fmt.Errorf("emu: decode snapshot: %w", err)
	}
	// The snapshot carries the screens, not the limits: give the mirror
	// the server's, or it trims scrollback differently and drifts.
	if err := setScrollback(t, DefaultScrollback); err != nil {
		t.Close()
		return nil, err
	}
	return &Terminal{t: t}, nil
}

// Write feeds PTY output into the emulator.
func (t *Terminal) Write(p []byte) (int, error) { return t.t.Write(p) }

// Resize changes the emulator's size, reflowing the screen.
func (t *Terminal) Resize(cols, rows uint16) error {
	if err := t.t.Resize(cols, rows, cellWidthPx, cellHeightPx); err != nil {
		return fmt.Errorf("emu: resize: %w", err)
	}
	return nil
}

// Size returns the current columns and rows.
func (t *Terminal) Size() (cols, rows uint16) {
	cols, _ = t.t.Cols()
	rows, _ = t.t.Rows()
	return cols, rows
}

// Title returns the window title the program set, if any.
func (t *Terminal) Title() string {
	s, _ := t.t.Title()
	return s
}

// Snapshot encodes the complete terminal state (both screens, scrollback,
// modes, cursor, keyboard flags, a pending escape sequence) in libghostty's
// binary snapshot format. The format is not stable across libghostty
// versions, so only a client of the same build may decode it.
func (t *Terminal) Snapshot() ([]byte, error) {
	b, err := t.t.Snapshot()
	if err != nil {
		return nil, fmt.Errorf("emu: snapshot: %w", err)
	}
	return b, nil
}

// VT returns bytes that repaint an outer terminal into this terminal's
// state: the screen with styles, then the modes, cursor, scrolling region,
// keyboard and charset state. It is a debug aid and the input of Digest,
// not a protocol: replayed into a fresh emulator it loses the inactive
// screen.
func (t *Terminal) VT() ([]byte, error) {
	f, err := libghostty.NewFormatter(t.t,
		libghostty.WithFormatterFormat(libghostty.FormatterFormatVT),
		libghostty.WithFormatterExtraModes(true),
		libghostty.WithFormatterExtraCursor(true),
		libghostty.WithFormatterExtraStyle(true),
		libghostty.WithFormatterExtraScrollingRegion(true),
		libghostty.WithFormatterExtraKeyboard(true),
		libghostty.WithFormatterExtraKittyKeyboard(true),
		libghostty.WithFormatterExtraTabstops(true),
		libghostty.WithFormatterExtraCharsets(true),
	)
	if err != nil {
		return nil, fmt.Errorf("emu: formatter: %w", err)
	}
	defer f.Close()
	s, err := f.FormatString()
	if err != nil {
		return nil, fmt.Errorf("emu: format: %w", err)
	}
	return []byte(s), nil
}

// digestHistory is how many scrollback rows Digest covers. libghostty
// trims scrollback page by page, and a terminal restored from a snapshot
// lays out its pages differently, so a mirror and the server can hold a
// few hundred rows more or less of old history. Both keep far more than
// this many.
const digestHistory = 1000

// Digest is a short hash of the emulator state: the active screen with
// styles, the last digestHistory rows of its scrollback, modes, cursor
// and keyboard flags. A mirror and the server with equal digests show the
// same thing.
func (t *Terminal) Digest() (string, error) {
	vt, err := t.VT()
	if err != nil {
		return "", err
	}
	_, rows := t.Size()
	h := sha256.Sum256(trimHistory(vt, int(rows)+digestHistory))
	return hex.EncodeToString(h[:8]), nil
}

// trimHistory drops all but the last keep rows of formatter output: the
// preamble up to the first cursor-home, then rows separated by CRLF.
func trimHistory(vt []byte, keep int) []byte {
	home := bytes.Index(vt, []byte("\x1b[H"))
	if home < 0 {
		return vt
	}
	body := vt[home+3:]
	cut := len(body)
	for n := 0; n < keep; n++ {
		i := bytes.LastIndex(body[:cut], []byte("\r\n"))
		if i < 0 {
			return vt
		}
		cut = i
	}
	out := append([]byte(nil), vt[:home+3]...)
	return append(out, body[cut:]...)
}

// PlainText returns the whole active screen, scrollback included, as
// trimmed plain text.
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

// Screen returns the visible rows, one line per row with trailing blanks
// trimmed. Screen rules in the state detector match against this.
func (t *Terminal) Screen() (string, error) {
	// The viewport must come from the render state: slicing the last rows
	// of the formatter output is wrong when the bottom rows are blank.
	rs, err := libghostty.NewRenderState()
	if err != nil {
		return "", fmt.Errorf("emu: render state: %w", err)
	}
	defer rs.Close()
	if err := rs.Update(t.t); err != nil {
		return "", fmt.Errorf("emu: render state: %w", err)
	}
	ri, err := libghostty.NewRenderStateRowIterator()
	if err != nil {
		return "", fmt.Errorf("emu: row iterator: %w", err)
	}
	defer ri.Close()
	rc, err := libghostty.NewRenderStateRowCells()
	if err != nil {
		return "", fmt.Errorf("emu: row cells: %w", err)
	}
	defer rc.Close()
	if err := rs.RowIterator(ri); err != nil {
		return "", fmt.Errorf("emu: row iterator: %w", err)
	}
	var lines []string
	for ri.Next() {
		b, err := ri.AppendText(nil, rc)
		if err != nil {
			return "", fmt.Errorf("emu: row text: %w", err)
		}
		lines = append(lines, strings.TrimRight(string(b), " "))
	}
	return strings.Join(lines, "\n"), nil
}

// Close frees the emulator.
func (t *Terminal) Close() { t.t.Close() }
