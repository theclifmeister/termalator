package main

import (
	"strconv"
	"unicode/utf8"

	"go.mitchellh.com/libghostty"
)

// renderer turns a libghostty RenderState into bytes for the outer terminal.
// It redraws only rows libghostty marks dirty, keeps palette colours as
// palette indexes (so the user's theme still applies) and default colours as
// SGR 39/49, and wraps each frame in mode 2026 for the outer terminal.
type renderer struct {
	rs   *libghostty.RenderState
	ri   *libghostty.RenderStateRowIterator
	rc   *libghostty.RenderStateRowCells
	buf  []byte
	full bool // next frame must clear and repaint everything

	outCols, outRows uint16
	lastCursor       string
	title            string
}

func newRenderer() (*renderer, error) {
	rs, err := libghostty.NewRenderState()
	if err != nil {
		return nil, err
	}
	ri, err := libghostty.NewRenderStateRowIterator()
	if err != nil {
		return nil, err
	}
	rc, err := libghostty.NewRenderStateRowCells()
	if err != nil {
		return nil, err
	}
	return &renderer{rs: rs, ri: ri, rc: rc, full: true}, nil
}

func appendCUP(b []byte, row, col int) []byte {
	b = append(b, "\x1b["...)
	b = strconv.AppendInt(b, int64(row), 10)
	b = append(b, ';')
	b = strconv.AppendInt(b, int64(col), 10)
	return append(b, 'H')
}

func appendColor(b []byte, c libghostty.StyleColor, base int) []byte {
	// base is 30 (fg), 40 (bg) or 50 (underline colour, 58/59).
	switch c.Tag {
	case libghostty.StyleColorPalette:
		n := int(c.Palette)
		switch {
		case base != 50 && n < 8:
			b = append(b, ';')
			b = strconv.AppendInt(b, int64(base+n), 10)
		case base != 50 && n < 16:
			b = append(b, ';')
			b = strconv.AppendInt(b, int64(base+60+n-8), 10)
		default:
			b = append(b, ';')
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

// sgrFor returns the full SGR sequence (starting from reset) for a style.
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
	case libghostty.UnderlineNone:
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

// frame renders the terminal. If held is true the render state was already
// captured at the start of a mode-2026 hold and must not be refreshed.
// Returns nil when nothing changed.
func (r *renderer) frame(t *libghostty.Terminal, held bool) ([]byte, error) {
	if !held {
		if err := r.rs.Update(t); err != nil {
			return nil, err
		}
	}
	dirty, err := r.rs.Dirty()
	if err != nil {
		return nil, err
	}
	b := r.buf[:0]
	b = append(b, "\x1b[?2026h\x1b[?25l"...)
	wrote := false
	if r.full {
		b = append(b, "\x1b[0m\x1b[H\x1b[2J"...)
		wrote = true
	}
	if r.full || dirty != libghostty.RenderStateDirtyFalse {
		if err := r.rs.RowIterator(r.ri); err != nil {
			return nil, err
		}
		var sgr, last []byte
		y := 0
		for r.ri.Next() {
			if y >= int(r.outRows) {
				r.ri.SetDirty(false)
				y++
				continue
			}
			rowDirty, _ := r.ri.Dirty()
			if r.full || dirty == libghostty.RenderStateDirtyFull || rowDirty {
				wrote = true
				b = appendCUP(b, y+1, 1)
				b = append(b, "\x1b[0m"...)
				last = last[:0]
				if err := r.ri.Cells(r.rc); err != nil {
					return nil, err
				}
				x := 0
				pendingBlank := 0 // run of default blank cells, emitted lazily
				for r.rc.Next() {
					if x >= int(r.outCols) {
						break
					}
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
						if x+2 > int(r.outCols) {
							break
						}
					}
					if len(g) == 0 && st.IsDefault() {
						pendingBlank++
						x++
						continue
					}
					if pendingBlank > 0 {
						if len(last) != 4 || string(last) != "\x1b[0m" {
							b = append(b, "\x1b[0m"...)
							last = append(last[:0], "\x1b[0m"...)
						}
						for ; pendingBlank > 0; pendingBlank-- {
							b = append(b, ' ')
						}
					}
					sgr = sgrFor(sgr[:0], st)
					if string(sgr) != string(last) {
						b = append(b, sgr...)
						last = append(last[:0], sgr...)
					}
					if len(g) == 0 || wide == libghostty.CellWideSpacerHead {
						b = append(b, ' ')
					} else {
						for _, cp := range g {
							b = appendRune(b, rune(cp))
						}
						// Multi-codepoint graphemes (ZWJ, flags, skin tones,
						// VS16) are where outer terminals disagree on width.
						// Re-anchor the cursor so one disagreement can't
						// shift the rest of the row.
						if len(g) > 1 {
							b = appendCUP(b, y+1, x+w+1)
						}
					}
					x += w
				}
				// Trailing default blanks: erase instead of printing spaces.
				if pendingBlank > 0 || x < int(r.outCols) {
					b = append(b, "\x1b[0m\x1b[K"...)
					last = last[:0]
				}
			}
			r.ri.SetDirty(false)
			y++
		}
	}
	r.rs.SetDirty(libghostty.RenderStateDirtyFalse)
	r.full = false

	// Cursor: position, visibility and shape.
	cur, err := r.rs.Cursor()
	if err != nil {
		return nil, err
	}
	var cb []byte
	if cur.Visible && cur.ViewportHasValue && cur.ViewportX < r.outCols && cur.ViewportY < r.outRows {
		cb = appendCUP(cb, int(cur.ViewportY)+1, int(cur.ViewportX)+1)
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
	if !wrote && string(cb) == r.lastCursor {
		r.buf = b
		return nil, nil
	}
	r.lastCursor = string(cb)
	b = append(b, cb...)
	// Forward the inner title to the outer terminal.
	if title, _ := t.Title(); title != r.title {
		r.title = title
		b = append(b, "\x1b]2;"...)
		b = append(b, title...)
		b = append(b, '\a')
	}
	b = append(b, "\x1b[?2026l"...)
	r.buf = b
	return b, nil
}

func appendRune(b []byte, r rune) []byte {
	var tmp [4]byte
	n := encodeRune(tmp[:], r)
	return append(b, tmp[:n]...)
}

func encodeRune(p []byte, r rune) int { return utf8.EncodeRune(p, r) }
