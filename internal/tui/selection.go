package tui

import (
	"fmt"
	"strings"

	"github.com/theclifmeister/terminatr/internal/emu"
)

// Copying text (docs/SPEC.md §4). The outer terminal reports the mouse
// while the sidebar shows, so its own selection needs Shift (Option in
// some macOS terminals); a plain drag over a pane whose program doesn't
// take the mouse selects in tm instead: highlighted while dragging,
// copied on release. The copy, and every clipboard write of the focused
// program (OSC 52: Claude Code's copy), goes to this console's outer
// terminal as OSC 52, so it lands on the clipboard of the machine the
// user sits at, over SSH too.

// selDrag is a drag selection in progress: its pane and where it started,
// in the pane's viewport cells.
type selDrag struct {
	p       *pane
	x, y    int
	dragged bool // the mouse moved off the start cell
}

// selPoint is m's cell in p's viewport, clamped to the pane. c.mu held.
func selPoint(p *pane, m emu.Mouse) (int, int) {
	cols, rows := p.mirror.Size()
	x := min(max(m.X-p.rect.X, 0), max(int(cols)-1, 0))
	y := min(max(m.Y-p.rect.Y, 0), p.rect.H-1) + p.r.Top()
	return x, min(max(y, 0), max(int(rows)-1, 0))
}

// selMouse handles the left button over a pane whose program doesn't take
// the mouse, and the drag that started there wherever it goes: a press
// starts a selection, motion extends it, the release copies it. A click
// without a drag only clears the old selection. c.mu held.
func (c *client) selMouse(p *pane, m emu.Mouse) {
	switch m.Action {
	case emu.MousePress:
		c.clearSel()
		x, y := selPoint(p, m)
		c.drag = &selDrag{p: p, x: x, y: y}
	case emu.MouseMotion:
		d := c.drag
		if d == nil {
			return
		}
		x, y := selPoint(d.p, m)
		if !d.dragged && x == d.x && y == d.y {
			return
		}
		d.dragged = true
		if err := d.p.mirror.Select(d.x, d.y, x, y); err != nil {
			c.log.Printf("select: %v", err)
			return
		}
		c.selOn = d.p
		d.p.r.Invalidate()
		c.poke()
	case emu.MouseRelease:
		d := c.drag
		c.drag = nil
		if d == nil || !d.dragged {
			return
		}
		text, err := d.p.mirror.SelectionText()
		if err != nil {
			c.log.Printf("selection: %v", err)
			return
		}
		c.copyText(text)
	}
}

// copyText puts text on the outer terminal's clipboard and says so in the
// status bar. c.mu held.
func (c *client) copyText(text string) {
	if text == "" {
		return
	}
	seq := emu.OSC52(emu.ClipboardStandard, []byte(text))
	if seq == nil {
		c.flash = "selection too long to copy"
	} else {
		c.clip = append(c.clip, seq...)
		n := strings.Count(text, "\n") + 1
		c.flash = fmt.Sprintf("copied %d %s", n, map[bool]string{true: "line", false: "lines"}[n == 1])
	}
	c.status()
	c.poke()
}

// clearSel removes the highlighted selection, if any. c.mu held.
func (c *client) clearSel() {
	if p := c.selOn; p != nil {
		c.selOn = nil
		if !p.gone && p.mirror != nil {
			p.mirror.ClearSelection()
			p.r.Invalidate()
			c.poke()
		}
	}
}

// forwardClipboard returns the clipboard hook for p's mirror: the focused
// program's clipboard writes go to the outer terminal while this console
// is the one the user last typed or clicked in, so two consoles on one
// view don't both copy. Runs inside mirror.Write with c.mu held.
func (c *client) forwardClipboard(p *pane) func(byte, []byte) {
	return func(which byte, data []byte) {
		if p != c.focus || c.v.Latest != "" && c.v.Latest != c.me {
			return
		}
		if seq := emu.OSC52(which, data); seq != nil {
			c.clip = append(c.clip, seq...)
			c.poke()
		}
	}
}
