package view

import "slices"

// The actions: what clients ask the server to do to a view (the view.*
// control methods, docs/SPEC.md §3.3). Each changes v in place and says
// whether anything changed; the server bumps Seq and broadcasts.

// Attach shows session id: its pane gets the focus when the layout
// already holds it, else the layout becomes that one pane. project, when
// set, becomes the current project.
func (v *View) Attach(id, project string) bool {
	old := v.Clone()
	if !v.Root.Has(id) {
		v.Root, v.Zoom = &Node{Session: id}, false
	}
	v.Mode, v.Focus = ModeLayout, id
	if project != "" {
		v.Current = project
	}
	return !Equal(old, *v)
}

// Dashboard shows the dashboard; the layout stays for the next Attach of
// one of its sessions.
func (v *View) Dashboard() bool {
	if v.Mode == ModeDashboard {
		return false
	}
	v.Mode = ModeDashboard
	return true
}

// ShowProject shows project's dashboard: the dashboard, with project
// current and its coordinator's row selected.
func (v *View) ShowProject(project string) bool {
	if project == "" {
		return v.Dashboard()
	}
	old := v.Clone()
	v.Mode, v.Current, v.Selected = ModeDashboard, project, "p:"+project
	return !Equal(old, *v)
}

// Expand opens (open) or closes project in the sidebar's tree. The
// current project stays open whatever its entry says.
func (v *View) Expand(project string, open bool) bool {
	if project == "" || slices.Contains(v.Expanded, project) == open {
		return false
	}
	if open {
		v.Expanded = append(v.Expanded, project)
		slices.Sort(v.Expanded)
	} else {
		v.Expanded = slices.DeleteFunc(v.Expanded, func(p string) bool { return p == project })
	}
	return true
}

// Split splits the pane of session at (the focused one when at isn't in
// the layout) in two: at first, add beside it (side) or below it. add
// gets the focus.
func (v *View) Split(at, add string, side bool) bool {
	if v.Root == nil || v.Root.Has(add) {
		return false
	}
	if !v.Root.Has(at) {
		at = v.Focus
	}
	v.Root = v.Root.replace(at, &Node{Side: side, Ratio: 0.5, A: &Node{Session: at}, B: &Node{Session: add}})
	v.Focus, v.Zoom = add, false
	return true
}

// replace puts n where the leaf of session id was, and returns the tree.
func (n *Node) replace(id string, with *Node) *Node {
	switch {
	case n == nil:
		return nil
	case n.Session == id:
		return with
	case n.Session != "":
		return n
	}
	n.A, n.B = n.A.replace(id, with), n.B.replace(id, with)
	return n
}

// Remove takes session id's pane out; its sibling takes the split's
// place. Without panes left the view goes back to the dashboard.
func (v *View) Remove(id string) bool {
	if !v.Root.Has(id) {
		return false
	}
	v.Root = v.Root.remove(id)
	v.Zoom = false
	if v.Focus == id {
		v.Focus = ""
	}
	v.Normalize()
	return true
}

// remove is n without the leaf of session id; nil when it was the only
// one.
func (n *Node) remove(id string) *Node {
	switch {
	case n == nil, n.Session == id:
		return nil
	case n.Session != "":
		return n
	case n.A.Session == id:
		return n.B
	case n.B.Session == id:
		return n.A
	}
	n.A, n.B = n.A.remove(id), n.B.remove(id)
	return n
}

// Prune removes the panes whose session alive says is gone.
func (v *View) Prune(alive func(id string) bool) bool {
	changed := false
	for _, id := range v.Root.Leaves() {
		if !alive(id) {
			changed = v.Remove(id) || changed
		}
	}
	return changed
}

// FocusOn gives session id the focus.
func (v *View) FocusOn(id string) bool {
	if !v.Root.Has(id) || v.Focus == id {
		return false
	}
	v.Focus = id
	return true
}

// FocusNext gives the focus to the next pane, in leaf order.
func (v *View) FocusNext() bool {
	ls := v.Root.Leaves()
	if len(ls) < 2 {
		return false
	}
	i := slices.Index(ls, v.Focus)
	v.Focus = ls[(i+1)%len(ls)]
	return true
}

// FocusDir gives the focus to the pane in direction (dx, dy) in a window
// of cols×rows. A zoomed view unzooms first.
func (v *View) FocusDir(dx, dy, cols, rows int) bool {
	changed := false
	if v.Zoom {
		v.Zoom, changed = false, true
	}
	if n := v.Lay(cols, rows).Neighbour(v.Focus, dx, dy); n != "" {
		v.Focus, changed = n, true
	}
	return changed
}

// ToggleZoom zooms the focused pane to the whole area, and back.
func (v *View) ToggleZoom() bool {
	if len(v.Root.Leaves()) < 2 {
		return false
	}
	v.Zoom = !v.Zoom
	return true
}

// Even rebuilds the tree as one row of equal panes when it was stacked
// at the top, else as one column.
func (v *View) Even() bool {
	ls := v.Root.Leaves()
	if len(ls) < 2 {
		return false
	}
	v.Root, v.Zoom = even(ls, !v.Root.Side), false
	return true
}

func even(ls []string, side bool) *Node {
	if len(ls) == 1 {
		return &Node{Session: ls[0]}
	}
	return &Node{Side: side, Ratio: 1 / float64(len(ls)), A: &Node{Session: ls[0]}, B: even(ls[1:], side)}
}

// ResizeTowards moves the divider nearest to the focused pane on the
// given axis by cells, in a window of cols×rows: positive moves it right
// or down. It reports whether there was one to move.
func (v *View) ResizeTowards(side bool, cells, cols, rows int) bool {
	if v.Zoom {
		return false
	}
	g := v.Lay(cols, rows)
	path := v.Root.path(v.Focus, nil)
	for i := len(path) - 2; i >= 0; i-- {
		s := path[i]
		if s.Side != side {
			continue
		}
		total := s.A.extent(g, side) + s.B.extent(g, side) // the room less the divider
		if total < 2 {
			return false
		}
		r := min(max(s.Ratio+float64(cells)/float64(total), 0.05), 0.95)
		if r == s.Ratio {
			return false
		}
		s.Ratio = r
		return true
	}
	return false
}

// DragDivider moves the divider through cell (x, y) of the view laid out
// in a window of cols×rows so that it sits at column (a side divider) or
// row to, as far as the panes beside it allow: the mouse dragging it. It
// reports whether one moved.
func (v *View) DragDivider(x, y, to, cols, rows int) bool {
	if v.Zoom || v.Root == nil {
		return false
	}
	return v.Root.drag(v.Lay(cols, rows).Area, x, y, to)
}

// drag finds the split whose divider holds (x, y) under n, laid out in r,
// and moves it to to.
func (n *Node) drag(r Rect, x, y, to int) bool {
	if n == nil || n.Session != "" {
		return false
	}
	if n.Side {
		aw, bw := SplitSizes(r.W, n.Ratio)
		if x == r.X+aw && y >= r.Y && y < r.Y+r.H {
			return n.moveTo(to-r.X, r.W-1)
		}
		return n.A.drag(Rect{r.X, r.Y, aw, r.H}, x, y, to) || n.B.drag(Rect{r.X + aw + 1, r.Y, bw, r.H}, x, y, to)
	}
	ah, bh := SplitSizes(r.H, n.Ratio)
	if y == r.Y+ah && x >= r.X && x < r.X+r.W {
		return n.moveTo(to-r.Y, r.H-1)
	}
	return n.A.drag(Rect{r.X, r.Y, r.W, ah}, x, y, to) || n.B.drag(Rect{r.X, r.Y + ah + 1, r.W, bh}, x, y, to)
}

// moveTo gives the split's A side a cells of room (the room less the
// divider), within the same bounds as ResizeTowards.
func (n *Node) moveTo(a, room int) bool {
	if room < 2 {
		return false
	}
	r := min(max(float64(a)/float64(room), 0.05), 0.95)
	if r == n.Ratio {
		return false
	}
	n.Ratio = r
	return true
}

// path is the nodes from n down to the leaf of session id, empty when
// it isn't under n.
func (n *Node) path(id string, out []*Node) []*Node {
	switch {
	case n == nil:
		return nil
	case n.Session == id:
		return append(out, n)
	case n.Session != "":
		return nil
	}
	out = append(out, n)
	if p := n.A.path(id, out); p != nil {
		return p
	}
	return n.B.path(id, out)
}

// extent is n's width (side) or height in g.
func (n *Node) extent(g Geometry, side bool) int {
	lo, hi := 1<<30, 0
	for _, id := range n.Leaves() {
		r := g.Panes[id]
		a, z := r.Y, r.Y+r.H
		if side {
			a, z = r.X, r.X+r.W
		}
		lo, hi = min(lo, a), max(hi, z)
	}
	return max(hi-lo, 0)
}
