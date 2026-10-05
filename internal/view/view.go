// Package view is the server-owned view (docs/SPEC.md §3.3, Views): what
// every console joined to it shows. The screen (the dashboard or the
// attached layout), the split tree with its ratios, the focus and zoom,
// the dashboard's selection, the current project and the projects
// sidebar with its tree live here, once, in the server; consoles only render them.
//
// The package is the model alone, with no I/O: the tree, the actions on
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
	ModeLayout    = "layout" // the split layout of attached sessions
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

	Mode  string `json:"mode"`
	Root  *Node  `json:"root,omitempty"`
	Focus string `json:"focus,omitempty"` // a session in Root
	Zoom  bool   `json:"zoom,omitempty"`

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

// Node is a split or, with Session set, one pane.
type Node struct {
	Session string `json:"session,omitempty"`
	// Side is true for panes side by side (a divider column between
	// them), false for one above the other (a divider row).
	Side  bool    `json:"side,omitempty"`
	Ratio float64 `json:"ratio,omitempty"` // A's share of the room left after the divider
	A     *Node   `json:"a,omitempty"`
	B     *Node   `json:"b,omitempty"`
}

// Clone is a deep copy of v, safe to hand to another goroutine.
func (v View) Clone() View {
	v.Root = v.Root.clone()
	v.Expanded = slices.Clone(v.Expanded)
	return v
}

// Here is what the sidebar's tree highlights, the row you are on: in the
// layout, the focused session (its coordinator or thread row); on the
// dashboard, the current project's row (session "").
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

func (n *Node) clone() *Node {
	if n == nil {
		return nil
	}
	c := *n
	c.A, c.B = n.A.clone(), n.B.clone()
	return &c
}

// Leaves are the sessions under n, left to right and top to bottom.
func (n *Node) Leaves() []string { return n.leaves(nil) }

func (n *Node) leaves(out []string) []string {
	switch {
	case n == nil:
		return out
	case n.Session != "":
		return append(out, n.Session)
	}
	return n.B.leaves(n.A.leaves(out))
}

// Has says whether session id is a pane of n.
func (n *Node) Has(id string) bool { return slices.Contains(n.Leaves(), id) }

// Visible are the sessions shown: every pane, or the zoomed one.
func (v *View) Visible() []string {
	if v.Mode != ModeLayout || v.Root == nil {
		return nil
	}
	if v.Zoom && v.Root.Has(v.Focus) {
		return []string{v.Focus}
	}
	return v.Root.Leaves()
}

// Valid checks a view read from disk or the wire: a tree whose leaves
// are distinct sessions, splits with two children, a focus in the tree.
func (v *View) Valid() error {
	seen := map[string]bool{}
	var walk func(n *Node, depth int) error
	walk = func(n *Node, depth int) error {
		switch {
		case depth > 64:
			return fmt.Errorf("view %s: the tree is too deep", v.Name)
		case n.Session != "":
			if n.A != nil || n.B != nil {
				return fmt.Errorf("view %s: pane %s has children", v.Name, n.Session)
			}
			if seen[n.Session] {
				return fmt.Errorf("view %s: %s shows twice", v.Name, n.Session)
			}
			seen[n.Session] = true
			return nil
		case n.A == nil || n.B == nil:
			return fmt.Errorf("view %s: a split without two sides", v.Name)
		}
		if err := walk(n.A, depth+1); err != nil {
			return err
		}
		return walk(n.B, depth+1)
	}
	if v.Root != nil {
		if err := walk(v.Root, 0); err != nil {
			return err
		}
	}
	if v.Focus != "" && !seen[v.Focus] {
		return fmt.Errorf("view %s: the focus %s is not a pane", v.Name, v.Focus)
	}
	return nil
}

// Normalize repairs what Valid allows to be loose: ratios in range, a
// focus when there are panes, the dashboard when there are none.
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
	var fix func(n *Node)
	fix = func(n *Node) {
		if n == nil || n.Session != "" {
			return
		}
		if n.Ratio <= 0 || n.Ratio >= 1 {
			n.Ratio = 0.5
		}
		fix(n.A)
		fix(n.B)
	}
	fix(v.Root)
	if v.Mode != ModeLayout && v.Mode != ModeDashboard {
		v.Mode = ModeDashboard
	}
	leaves := v.Root.Leaves()
	if len(leaves) == 0 {
		v.Root, v.Focus, v.Zoom = nil, "", false
		if v.Mode == ModeLayout {
			v.Mode = ModeDashboard
		}
		return
	}
	if !slices.Contains(leaves, v.Focus) {
		v.Focus = leaves[0]
	}
	if len(leaves) == 1 {
		v.Zoom = false
	}
}

// Equal says whether a and b show the same thing (Seq aside).
func Equal(a, b View) bool {
	a.Seq, b.Seq = 0, 0
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return string(x) == string(y)
}
