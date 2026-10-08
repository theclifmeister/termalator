//go:build cgo

package emu

import "go.mitchellh.com/libghostty"

// Text selection and the clipboard (docs/SPEC.md §4): the attach client
// selects text in a mirror with the mouse and copies it to the outer
// terminal's clipboard with OSC 52, which works over SSH; OSC 52 a
// program writes is forwarded the same way.

// Select selects the text from viewport cell (x0, y0) to (x1, y1),
// 0-based and inclusive, in either order, as a terminal does for a mouse
// drag: whole rows between the two ends. The selection stays on the cells
// as the screen scrolls.
func (t *Terminal) Select(x0, y0, x1, y1 int) error {
	cols, rows := t.Size()
	ref := func(x, y int) (*libghostty.GridRef, error) {
		x = min(max(x, 0), int(cols)-1)
		y = min(max(y, 0), int(rows)-1)
		return t.t.GridRef(libghostty.Point{Tag: libghostty.PointTagViewport, X: uint16(x), Y: uint32(y)})
	}
	// GridRefs are borrowed: take each one right before it is copied.
	start, err := ref(x0, y0)
	if err != nil {
		return err
	}
	s := *start
	end, err := ref(x1, y1)
	if err != nil {
		return err
	}
	return t.t.SetSelection(&libghostty.Selection{Start: s, End: *end})
}

// ClearSelection removes the selection.
func (t *Terminal) ClearSelection() error { return t.t.SetSelection(nil) }

// SelectionText is the selected text as plain text: soft-wrapped rows
// joined, trailing blanks trimmed; "" without a selection.
func (t *Terminal) SelectionText() (string, error) {
	return t.t.SelectionFormatString(
		libghostty.WithSelectionFormat(libghostty.FormatterFormatPlain),
		libghostty.WithSelectionUnwrap(true),
		libghostty.WithSelectionTrim(true),
	)
}

// OnClipboard registers fn for the program's clipboard writes (OSC 52,
// and iTerm2's Copy): which clipboard (Clipboard*) and the text, "" to
// clear it. Reads (OSC 52 ?) never reach fn, so a program can't read the
// user's clipboard through a mirror. fn runs inside Write.
func (t *Terminal) OnClipboard(fn func(which byte, data []byte)) {
	t.t.SetEffectClipboardWrite(func(_ *libghostty.Terminal, w libghostty.ClipboardWrite) libghostty.ClipboardWriteReply {
		which := byte(ClipboardStandard)
		switch w.Location {
		case libghostty.ClipboardLocationPrimary:
			which = ClipboardPrimary
		case libghostty.ClipboardLocationSelection:
			which = ClipboardSelection
		}
		var data []byte
		for _, c := range w.Contents {
			if c.MIME == "text/plain" || data == nil {
				data = c.Data
			}
		}
		fn(which, data)
		return libghostty.ClipboardWriteReply{Result: libghostty.ClipboardWriteSuccess}
	})
}
