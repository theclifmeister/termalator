// Package view is the server-owned view (docs/SPEC.md §3.3, Views): what
// every console joined to it shows. The screen (the dashboard or an
// attached session), the session it shows, the dashboard's selection, the current project and the projects
// sidebar with its tree live here, once, in the server; consoles only render them.
//
// The package is the model alone, with no I/O: the view, the actions on
// it and the geometry. The server (internal/server) keeps the views, applies
// the actions its clients send and broadcasts each new version; clients
// (internal/tui) lay out the same tree with the same functions, so every
// console computes the same rectangles from the same view.
package view

import (
	"encoding/json"
	"fmt"
	"slices"
)

// Main is the view every console joins unless it asks for its own.
const Main = "main"

// MaxKey bounds a row key (Selected, SideSel) a client sends.
const MaxKey = 512

// MaxExpanded bounds the projects a view keeps expanded.
const MaxExpanded = 256

// Modes: what the view shows.
const (
	ModeDashboard = "dashboard"
	ModeLayout    = "layout" // an attached session (the name predates single panes)
)

// View is one view, as the server keeps and broadcasts it.
type View struct {
	Name string `json:"name"`
	// Own views belong to one console (tm --own, tm attach) and go away
	// with it; they are never saved.
	Own bool `json:"own,omitempty"`
	// Bare views have no dashboard: tm attach and tm project open, which
	// end on a detach. They show the sidebar too; a click on it hands the
	// console over to view main (docs/SPEC.md §3.3).
	Bare bool `json:"bare,omitempty"`
	// StatusBar draws the status bar in a bare view; the others always
	// have one.
	StatusBar bool `json:"status_bar,omitempty"`
	// Seq goes up with every change, so a client keeps the newest
	// version it has seen.
	Seq uint64 `json:"seq"`

	Mode string `json:"mode"`
	// Focus is the session the view shows: one pane, beside the sidebar
	// (ModeLayout). It stays while the dashboard shows, for going back.
	Focus string `json:"focus,omitempty"`

	// Selected is the dashboard's selected row (its key).
	Selected string `json:"selected,omitempty"`
	// Current is the project last opened: the one the dashboard shows,
	// always expanded in the sidebar's tree, and where ] and [ count
	// from.
	Current string  `json:"current,omitempty"`
	Sidebar Sidebar `json:"sidebar"`
	// Expanded are the projects the sidebar's tree shows open besides
	// the current one, sorted. The tree's highlighted row follows from
	// the view too (Here).
	Expanded []string `json:"expanded,omitempty"`
	// SideSel is the tree row the keyboard is on in the sidebar (its key:
	// "p:<project>", "c:<project>" or "t:<project>/<thread>"); a key no
	// longer in the tree, or "", means the row you are on (Here).
	SideSel string `json:"side_sel,omitempty"`

	// Latest is the client whose window sizes the layout: the one that
	// last typed, resized its window or changed the layout. Cols and Rows
	// are that window's size; 0 until a client joined.
	Latest string `json:"latest,omitempty"`
	Cols   uint16 `json:"cols,omitempty"`
	Rows   uint16 `json:"rows,omitempty"`
}

// Clone is a copy of v, safe to hand to another goroutine.
func (v View) Clone() View {
	v.Expanded = slices.Clone(v.Expanded)
	return v
}

// Here is what the sidebar's tree highlights, the row you are on: with a
// session shown, its coordinator or thread row; on the dashboard, the
// current project's row (session "").
func (v *View) Here() (project, session string) {
	if v.Mode == ModeLayout {
		return v.Current, v.Focus
	}
	return v.Current, ""
}

// IsExpanded says whether the tree shows project open: the current one
// always is.
func (v *View) IsExpanded(project string) bool {
	return project != "" && (project == v.Current || slices.Contains(v.Expanded, project))
}

// Has says whether session id is the view's pane.
func (v *View) Has(id string) bool { return id != "" && v.Focus == id }

// Visible are the sessions shown: the view's one pane, when it shows a
// session.
func (v *View) Visible() []string {
	if v.Mode != ModeLayout || v.Focus == "" {
		return nil
	}
	return []string{v.Focus}
}

// Valid checks a view read from disk or the wire.
func (v *View) Valid() error {
	if len(v.Focus) > MaxKey {
		return fmt.Errorf("view %s: a session id too long", v.Name)
	}
	return nil
}

// Normalize repairs what Valid allows to be loose: the dashboard when
// there is no session to show. Views saved before panes were single
// (a split tree in "root") keep their focused session, the one they
// showed in front.
func (v *View) Normalize() {
	v.Sidebar = v.Sidebar.Clamp()
	if len(v.Expanded) > 0 {
		v.Expanded = slices.DeleteFunc(v.Expanded, func(p string) bool { return p == "" })
		slices.Sort(v.Expanded)
		v.Expanded = slices.Compact(v.Expanded)
		if len(v.Expanded) > MaxExpanded {
			v.Expanded = v.Expanded[:MaxExpanded]
		}
	} else {
		v.Expanded = nil
	}
	if v.Mode != ModeLayout && v.Mode != ModeDashboard {
		v.Mode = ModeDashboard
	}
	if v.Focus == "" && v.Mode == ModeLayout {
		v.Mode = ModeDashboard
	}
}

// Equal says whether a and b show the same thing (Seq aside).
func Equal(a, b View) bool {
	a.Seq, b.Seq = 0, 0
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return string(x) == string(y)
}
