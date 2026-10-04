package view

import "fmt"

// Sidebar is the projects sidebar's layout (docs/SPEC.md §4): part of
// the view, so every console shows it alike. internal/tui draws it.
type Sidebar struct {
	// Width is the full sidebar's width in columns, its border included.
	Width int `json:"width"`
	// Slim keeps the slim strip (glyphs and short names) in any window.
	Slim bool `json:"slim"`
}

const (
	SideDefault = 24 // a full sidebar's width
	SideMin     = 14 //
	SideMax     = 48 //
	SideSlim    = 7  // the slim strip: a marker, a glyph, 4 letters, the border
	SideRoom    = 60 // a full sidebar leaves the panes at least this many columns
	SideStep    = 2  // { and } change the width this much
)

// Clamp keeps the width in range; 0 is the default.
func (s Sidebar) Clamp() Sidebar {
	if s.Width == 0 {
		s.Width = SideDefault
	}
	s.Width = min(max(s.Width, SideMin), SideMax)
	return s
}

// Cols is the sidebar's width in a window w columns wide: the full width,
// or the slim strip when asked for or when the window is narrow. It never
// disappears.
func (s Sidebar) Cols(w int) int {
	full := s.Clamp().Width
	if s.Slim || w-full < SideRoom {
		return min(SideSlim, max(w-1, 1))
	}
	return full
}

// Full says whether the sidebar is at its full width in a window w wide.
func (s Sidebar) Full(w int) bool { return s.Cols(w) > SideSlim }

// Key applies a sidebar key ({ narrower, } wider, b slim strip on or
// off) to s in a window w wide; msg says why nothing changed.
func (s Sidebar) Key(key string, w int) (out Sidebar, msg string) {
	s = s.Clamp()
	narrow := fmt.Sprintf("the window is too narrow for the full sidebar (it needs %d columns)", s.Width+SideRoom)
	switch key {
	case "b":
		s.Slim = !s.Slim
		if !s.Slim && !s.Full(w) {
			msg = narrow
		}
		return s, msg
	case "{", "}":
		if s.Slim || !s.Full(w) {
			s.Slim = false
			if !s.Full(w) {
				return s, narrow
			}
			return s, ""
		}
		d := SideStep
		if key == "{" {
			d = -d
		}
		// Never wider than the window allows: the panes keep SideRoom.
		s.Width = min(max(s.Width+d, SideMin), SideMax, max(w-SideRoom, SideMin))
	}
	return s, ""
}

// DragTo is s with its border dragged to column x of a window w wide.
func (s Sidebar) DragTo(x, w int) Sidebar {
	s.Slim = x+1 <= SideSlim
	if !s.Slim {
		s.Width = min(max(x+1, SideMin), SideMax, max(w-SideRoom, SideMin))
	}
	return s.Clamp()
}
