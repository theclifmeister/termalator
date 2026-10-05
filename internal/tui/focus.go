package tui

import "slices"

// The keyboard's areas (docs/SPEC.md §4): one model on the dashboard and
// in a session. A click gives its area the keyboard; tab (prefix+tab in a
// session) moves it to the next area, esc back to the main one. On the
// dashboard the areas are the list, the details panel and the sidebar;
// in a session the pane, the sidebar and the info panel. Which area has
// the keyboard is each console's own.

// area is an area that takes the keyboard.
type area int

const (
	areaMain    area = iota // the list on the dashboard, the pane in a session
	areaDetails             // the dashboard's details panel
	areaSide                // the projects sidebar
	areaInfo                // the info panel beside a thread's pane
)

// cycle is the area after cur among areas, in tab's order, or the one
// before it with back (shift+tab); an area not among them counts as the
// first.
func cycle(areas []area, cur area, back bool) area {
	i := max(slices.Index(areas, cur), 0)
	d := 1
	if back {
		d = len(areas) - 1
	}
	return areas[(i+d)%len(areas)]
}
