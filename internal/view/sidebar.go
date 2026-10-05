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
	SideDefault = 32 // a full sidebar's width: room for a thread's id and a few words of its title
	SideMin     = 14 // no maximum: only the window bounds it (SideRoom)
	SideSlim    = 7  // the slim strip: a marker, a glyph, 3 letters, a blank, the border
	SideRoom    = 60 // a full sidebar leaves the panes at least this many columns
	SideStep    = 2  // { and } change the width this much
)

// Clamp keeps the width in range; 0 is the default. There is no upper
// bound: a width too wide for the window is shown narrower (Cols) and
// kept, so it comes back in a wider window.
func (s Sidebar) Clamp() Sidebar {
	if s.Width == 0 {
		s.Width = SideDefault
	}
	s.Width = max(s.Width, SideMin)
	return s
}

// Cols is the sidebar's width in a window w columns wide: the full width,
// cut to leave the panes SideRoom, or the slim strip when asked for or
// when the window is narrow. A width wider than the default shrinks down
// to the default before the strip takes over. It never disappears.
func (s Sidebar) Cols(w int) int {
	full := s.Clamp().Width
	if s.Slim || w-SideRoom < s.least() {
		return min(SideSlim, max(w-1, 1))
	}
	return min(full, w-SideRoom)
}

// least is the narrowest the full sidebar shrinks to in a narrow window.
func (s Sidebar) least() int { return min(s.Clamp().Width, SideDefault) }

// Full says whether the sidebar is at its full width in a window w wide.
func (s Sidebar) Full(w int) bool { return s.Cols(w) > SideSlim }

// Key applies a sidebar key ({ narrower, } wider, b slim strip on or
// off) to s in a window w wide; msg says why nothing changed.
func (s Sidebar) Key(key string, w int) (out Sidebar, msg string) {
	s = s.Clamp()
	narrow := fmt.Sprintf("the window is too narrow for the full sidebar (it needs %d columns)", s.least()+SideRoom)
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
		// Step from the width shown, never wider than the window allows
		// (the panes keep SideRoom); at that limit } keeps a wider saved
		// width as it is.
		shown, most := s.Cols(w), max(w-SideRoom, SideMin)
		if d > 0 && shown >= most {
			return s, ""
		}
		s.Width = min(max(shown+d, SideMin), most)
	}
	return s, ""
}

// DragTo is s with its border dragged to column x of a window w wide.
func (s Sidebar) DragTo(x, w int) Sidebar {
	s.Slim = x+1 <= SideSlim
	if !s.Slim {
		s.Width = min(max(x+1, SideMin), max(w-SideRoom, SideMin))
	}
	return s.Clamp()
}
