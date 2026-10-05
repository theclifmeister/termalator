package view

import "fmt"

// Info is the info panel's layout (docs/SPEC.md §4): the panel right of
// a thread's pane that shows its task, steps, state, PR and last report.
// It is part of the view, like the sidebar, because it takes columns
// from the pane: the server sizes the session to what is left.
// internal/tui draws it.
type Info struct {
	// Width is the panel's width in columns, its border included.
	Width int `json:"width"`
	// Off hides it (prefix+|) in any window.
	Off bool `json:"off,omitempty"`
}

const (
	InfoDefault = 40 // the panel's width: a step's text and a PR line fit
	InfoMin     = 24 // narrower than this, it hides instead
)

// Clamp keeps the width in range; 0 is the default. As for the sidebar,
// there is no upper bound: a width too wide for the window is shown
// narrower (Cols) and kept.
func (p Info) Clamp() Info {
	if p.Width == 0 {
		p.Width = InfoDefault
	}
	p.Width = max(p.Width, InfoMin)
	return p
}

// room is what a window w wide leaves the panel beside a sidebar sideW
// wide and a pane of SideRoom columns.
func room(w, sideW int) int { return w - sideW - SideRoom }

// Cols is the panel's width in a window w columns wide beside a sidebar
// sideW wide: its width, cut to leave the pane SideRoom, or 0 (hidden)
// when it is off or there is no room for InfoMin columns.
func (p Info) Cols(w, sideW int) int {
	r := room(w, sideW)
	if p.Off || r < InfoMin {
		return 0
	}
	return min(p.Clamp().Width, r)
}

// Toggle turns the panel on or off (prefix+|) in a window w wide beside
// a sidebar sideW wide; msg says why turning it on shows nothing.
func (p Info) Toggle(w, sideW int) (out Info, msg string) {
	p = p.Clamp()
	p.Off = !p.Off
	if !p.Off && p.Cols(w, sideW) == 0 {
		msg = fmt.Sprintf("the window is too narrow for the info panel (it needs %d columns)", sideW+SideRoom+InfoMin)
	}
	return p, msg
}

// DragTo is p with its border (its first column) dragged to column x of
// a window w wide beside a sidebar sideW wide: never narrower than
// InfoMin, never so wide that the pane gets less than SideRoom.
func (p Info) DragTo(x, w, sideW int) Info {
	p = p.Clamp()
	p.Width = min(max(w-x, InfoMin), max(room(w, sideW), InfoMin))
	return p
}
