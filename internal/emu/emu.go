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
		libghostty.WithMaxScrollbackLines(sb),
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
		if o.Xtversion != "" {
			name := o.Xtversion
			opts = append(opts, libghostty.WithXtversion(func(*libghostty.Terminal) string { return name }))
		}
	}
	t, err := libghostty.NewTerminal(opts...)
	if err != nil {
		return nil, fmt.Errorf("emu: new terminal: %w", err)
	}
	return &Terminal{t: t}, nil
}

// Decode rebuilds a terminal from a Snapshot. The result has no effects
// wired (no WritePty), which is what a client mirror wants.
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

// Digest is a short hash of the full emulator state (both screens' visible
// content, styles, modes, cursor, keyboard flags). A mirror and the server
// with equal digests show the same thing.
func (t *Terminal) Digest() (string, error) {
	vt, err := t.VT()
	if err != nil {
		return "", err
	}
	h := sha256.Sum256(vt)
	return hex.EncodeToString(h[:8]), nil
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

// Rows returns the visible rows twice: as shown, and with every faint
// (dim) cell blanked. Screen rules with skip_dim match against the second,
// so ghost text such as a prompt suggestion isn't read as typed input
// (docs/SPEC.md §8.2). Trailing blanks are trimmed on every row.
func (t *Terminal) Rows() (plain, noDim []string, err error) {
	rs, err := libghostty.NewRenderState()
	if err != nil {
		return nil, nil, fmt.Errorf("emu: render state: %w", err)
	}
	defer rs.Close()
	if err := rs.Update(t.t); err != nil {
		return nil, nil, fmt.Errorf("emu: render state: %w", err)
	}
	ri, err := libghostty.NewRenderStateRowIterator()
	if err != nil {
		return nil, nil, fmt.Errorf("emu: row iterator: %w", err)
	}
	defer ri.Close()
	rc, err := libghostty.NewRenderStateRowCells()
	if err != nil {
		return nil, nil, fmt.Errorf("emu: row cells: %w", err)
	}
	defer rc.Close()
	if err := rs.RowIterator(ri); err != nil {
		return nil, nil, fmt.Errorf("emu: row iterator: %w", err)
	}
	var style libghostty.RenderCellStyle
	var buf []byte
	for ri.Next() {
		if err := ri.Cells(rc); err != nil {
			return nil, nil, fmt.Errorf("emu: row cells: %w", err)
		}
		var line, dimless []byte
		for rc.Next() {
			raw, err := rc.Raw()
			if err != nil {
				return nil, nil, fmt.Errorf("emu: cell: %w", err)
			}
			if w, _ := raw.Wide(); w == libghostty.CellWideSpacerTail || w == libghostty.CellWideSpacerHead {
				continue
			}
			buf, err = rc.AppendGraphemes(buf[:0])
			if err != nil {
				return nil, nil, fmt.Errorf("emu: cell text: %w", err)
			}
			if len(buf) == 0 {
				buf = append(buf, ' ')
			}
			line = append(line, buf...)
			if err := rc.StyleInto(&style); err == nil && style.Faint {
				dimless = append(dimless, bytes.Repeat([]byte{' '}, len([]rune(string(buf))))...)
			} else {
				dimless = append(dimless, buf...)
			}
		}
		plain = append(plain, strings.TrimRight(string(line), " "))
		noDim = append(noDim, strings.TrimRight(string(dimless), " "))
	}
	return plain, noDim, nil
}
