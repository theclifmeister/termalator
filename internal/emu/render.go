package emu

import (
	"fmt"
	"strconv"
	"unicode/utf8"

	"go.mitchellh.com/libghostty"
)

// Renderer draws a Terminal onto an outer terminal: the attach client's
// cell renderer (docs/SPEC.md §3.3). It redraws only the rows libghostty
// marks dirty, wraps every frame in mode 2026 so the outer terminal never
// shows a torn one, keeps palette colours as palette indexes and default
// colours as SGR 39/49 (so the user's theme applies), and re-anchors the
// cursor after multi-codepoint graphemes.
//
// The outer window may differ in size from the pane: the pane is cropped
// or padded, never resized. When the pane has more rows than the window,
// the renderer scrolls its view so the cursor stays visible.
type Renderer struct {
	rs *libghostty.RenderState
	ri *libghostty.RenderStateRowIterator
	rc *libghostty.RenderStateRowCells

	buf  []byte
	sgr  []byte
	last []byte
	full bool // the next frame clears and repaints everything

	outCols, outRows int
	top              int // first pane row shown
	lastCursor       string
	title            string
}

// NewRenderer returns a renderer for an outer window of cols×rows.
func NewRenderer(cols, rows uint16) (*Renderer, error) {
	rs, err := libghostty.NewRenderState()
	if err != nil {
		return nil, fmt.Errorf("emu: render state: %w", err)
	}
	ri, err := libghostty.NewRenderStateRowIterator()
	if err != nil {
		rs.Close()
		return nil, fmt.Errorf("emu: row iterator: %w", err)
	}
	rc, err := libghostty.NewRenderStateRowCells()
	if err != nil {
		ri.Close()
		rs.Close()
		return nil, fmt.Errorf("emu: row cells: %w", err)
	}
	return &Renderer{rs: rs, ri: ri, rc: rc, full: true, outCols: int(cols), outRows: int(rows)}, nil
}

// Close frees the renderer.
func (r *Renderer) Close() {
	r.rc.Close()
	r.ri.Close()
	r.rs.Close()
}

// SetSize changes the outer window size; the next frame repaints all.
func (r *Renderer) SetSize(cols, rows uint16) {
	r.outCols, r.outRows = int(cols), int(rows)
	r.full = true
}

// Invalidate makes the next frame repaint everything (after a new
// snapshot, or when the outer screen may have been disturbed).
func (r *Renderer) Invalidate() { r.full = true }

// Top is the first pane row the window shows; outer row y is pane row
// Top()+y.
func (r *Renderer) Top() int { return r.top }

// Capture takes the terminal's current state as the frame to draw. Call it
// when a mode-2026 hold starts; frames drawn while held show this state.
func (r *Renderer) Capture(t *Terminal) error { return r.rs.Update(t.t) }

// Frame returns the bytes that bring the outer terminal up to date, or
// nil when nothing changed. With held set it draws the state of the last
// Capture instead of the terminal's current one.
func (r *Renderer) Frame(t *Terminal, held bool) ([]byte, error) {
	if !held {
		if err := r.rs.Update(t.t); err != nil {
			return nil, err
		}
	}
	dirty, err := r.rs.Dirty()
	if err != nil {
		return nil, err
	}
	cur, err := r.rs.Cursor()
	if err != nil {
		return nil, err
	}
	paneRows, _ := r.rs.Rows()
	if top := r.viewTop(int(paneRows), cur); top != r.top {
		r.top, r.full = top, true
	}

	b := append(r.buf[:0], "\x1b[?2026h\x1b[?25l"...)
	wrote := false
	if r.full {
		b = append(b, "\x1b[0m\x1b[H\x1b[2J"...)
		wrote = true
	}
	if r.full || dirty != libghostty.RenderStateDirtyFalse {
		if err := r.rs.RowIterator(r.ri); err != nil {
			return nil, err
		}
		for y := 0; r.ri.Next(); y++ {
			oy := y - r.top
			if oy >= 0 && oy < r.outRows {
				rowDirty, _ := r.ri.Dirty()
				if r.full || dirty == libghostty.RenderStateDirtyFull || rowDirty {
					wrote = true
					if b, err = r.row(b, oy); err != nil {
						return nil, err
					}
				}
			}
			r.ri.SetDirty(false)
		}
	}
	r.rs.SetDirty(libghostty.RenderStateDirtyFalse)
	r.full = false

	// Cursor: position, visibility and shape.
	var cb []byte
	if cy := int(cur.ViewportY) - r.top; cur.Visible && cur.ViewportHasValue &&
		int(cur.ViewportX) < r.outCols && cy >= 0 && cy < r.outRows {
		cb = appendCUP(cb, cy+1, int(cur.ViewportX)+1)
		shape := 2
		switch cur.VisualStyle {
		case libghostty.CursorVisualStyleBar:
			shape = 6
		case libghostty.CursorVisualStyleUnderline:
			shape = 4
		}
		if cur.Blinking {
			shape--
		}
		cb = append(cb, "\x1b["...)
		cb = strconv.AppendInt(cb, int64(shape), 10)
		cb = append(cb, " q\x1b[?25h"...)
	}
	title, _ := t.t.Title()
	if !wrote && string(cb) == r.lastCursor && title == r.title {
		r.buf = b
		return nil, nil
	}
	r.lastCursor = string(cb)
	b = append(b, cb...)
	// Forward the program's title to the outer window.
	if title != r.title {
		r.title = title
		b = append(b, "\x1b]2;"...)
		b = append(b, sanitizeTitle(title)...)
		b = append(b, '\a')
	}
	b = append(b, "\x1b[?2026l"...)
	r.buf = b
	return b, nil
}

// viewTop picks the first pane row to show: 0 when the pane fits, else
// the current top moved just enough to keep the cursor in view.
func (r *Renderer) viewTop(paneRows int, cur *libghostty.RenderStateCursor) int {
	if paneRows <= r.outRows || r.outRows <= 0 {
		return 0
	}
	top := r.top
	if cur.ViewportHasValue {
		cy := int(cur.ViewportY)
		if cy < top {
			top = cy
		} else if cy >= top+r.outRows {
			top = cy - r.outRows + 1
		}
	}
	return min(max(top, 0), paneRows-r.outRows)
}

// row draws the iterator's current row at outer row oy.
func (r *Renderer) row(b []byte, oy int) ([]byte, error) {
	b = appendCUP(b, oy+1, 1)
	b = append(b, "\x1b[0m"...)
	r.last = append(r.last[:0], "\x1b[0m"...)
	if err := r.ri.Cells(r.rc); err != nil {
		return nil, err
	}
	x := 0
	pendingBlank := 0 // a run of default blank cells, written lazily
	for r.rc.Next() && x < r.outCols {
		cell, err := r.rc.Raw()
		if err != nil {
			return nil, err
		}
		wide, _ := cell.Wide()
		if wide == libghostty.CellWideSpacerTail {
			continue // covered by the wide cell before it
		}
		st, err := r.rc.Style()
		if err != nil {
			return nil, err
		}
		g, _ := r.rc.Graphemes()
		w := 1
		if wide == libghostty.CellWideWide {
			w = 2
			if x+2 > r.outCols {
				break
			}
		}
		if len(g) == 0 && st.IsDefault() {
			pendingBlank++
			x++
			continue
		}
		if pendingBlank > 0 {
			if string(r.last) != "\x1b[0m" {
				b = append(b, "\x1b[0m"...)
				r.last = append(r.last[:0], "\x1b[0m"...)
			}
			for ; pendingBlank > 0; pendingBlank-- {
				b = append(b, ' ')
			}
		}
		r.sgr = sgrFor(r.sgr[:0], st)
		if string(r.sgr) != string(r.last) {
			b = append(b, r.sgr...)
			r.last = append(r.last[:0], r.sgr...)
		}
		if len(g) == 0 || wide == libghostty.CellWideSpacerHead {
			b = append(b, ' ')
		} else {
			for _, cp := range g {
				b = utf8.AppendRune(b, rune(cp))
			}
			// Outer terminals disagree about the width of ZWJ sequences,
			// flags, skin tones and VS16: re-anchor the cursor so one
			// disagreement can't shift the rest of the row.
			if len(g) > 1 {
				b = appendCUP(b, oy+1, x+w+1)
			}
		}
		x += w
	}
	// Trailing default blanks: erase instead of printing spaces.
	if pendingBlank > 0 || x < r.outCols {
		b = append(b, "\x1b[0m\x1b[K"...)
		r.last = r.last[:0]
	}
	return b, nil
}

func appendCUP(b []byte, row, col int) []byte {
	b = append(b, "\x1b["...)
	b = strconv.AppendInt(b, int64(row), 10)
	b = append(b, ';')
	b = strconv.AppendInt(b, int64(col), 10)
	return append(b, 'H')
}

// appendColor appends one colour of an SGR sequence. base is 30 (fg), 40
// (bg) or 50 (underline colour, 58).
func appendColor(b []byte, c libghostty.StyleColor, base int) []byte {
	switch c.Tag {
	case libghostty.StyleColorPalette:
		n := int(c.Palette)
		b = append(b, ';')
		switch {
		case base != 50 && n < 8:
			b = strconv.AppendInt(b, int64(base+n), 10)
		case base != 50 && n < 16:
			b = strconv.AppendInt(b, int64(base+60+n-8), 10)
		default:
			b = strconv.AppendInt(b, int64(base+8), 10)
			b = append(b, ";5;"...)
			b = strconv.AppendInt(b, int64(n), 10)
		}
	case libghostty.StyleColorRGB:
		b = append(b, ';')
		b = strconv.AppendInt(b, int64(base+8), 10)
		b = append(b, ";2;"...)
		b = strconv.AppendInt(b, int64(c.RGB.R), 10)
		b = append(b, ';')
		b = strconv.AppendInt(b, int64(c.RGB.G), 10)
		b = append(b, ';')
		b = strconv.AppendInt(b, int64(c.RGB.B), 10)
	}
	return b
}

// sgrFor returns the complete SGR sequence, starting from a reset, for st.
func sgrFor(dst []byte, st *libghostty.Style) []byte {
	dst = append(dst, "\x1b[0"...)
	if st.Bold() {
		dst = append(dst, ";1"...)
	}
	if st.Faint() {
		dst = append(dst, ";2"...)
	}
	if st.Italic() {
		dst = append(dst, ";3"...)
	}
	switch st.Underline() {
	case libghostty.UnderlineSingle:
		dst = append(dst, ";4"...)
	case libghostty.UnderlineDouble:
		dst = append(dst, ";4:2"...)
	case libghostty.UnderlineCurly:
		dst = append(dst, ";4:3"...)
	case libghostty.UnderlineDotted:
		dst = append(dst, ";4:4"...)
	case libghostty.UnderlineDashed:
		dst = append(dst, ";4:5"...)
	}
	if st.Blink() {
		dst = append(dst, ";5"...)
	}
	if st.Inverse() {
		dst = append(dst, ";7"...)
	}
	if st.Invisible() {
		dst = append(dst, ";8"...)
	}
	if st.Strikethrough() {
		dst = append(dst, ";9"...)
	}
	if st.Overline() {
		dst = append(dst, ";53"...)
	}
	dst = appendColor(dst, st.FgColor(), 30)
	dst = appendColor(dst, st.BgColor(), 40)
	dst = appendColor(dst, st.UnderlineColor(), 50)
	return append(dst, 'm')
}

// sanitizeTitle drops control characters, so a title can't end the OSC
// early and inject sequences into the outer terminal.
func sanitizeTitle(s string) []byte {
	out := make([]byte, 0, len(s))
	for _, c := range []byte(s) {
		if c >= 0x20 && c != 0x7f {
			out = append(out, c)
		}
	}
	return out
}
