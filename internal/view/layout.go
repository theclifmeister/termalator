package view

// The geometry: where each pane of a view goes in a window. The server
// lays the view out at its Latest client's window to size the sessions;
// every client lays it out at the same size to draw it, cropping or
// padding the frame to its own window. Both use Lay, so they agree.

// Rect is a rectangle of window cells, 0-based.
type Rect struct{ X, Y, W, H int }

// Divider is a split's line: a column when Side, else a row.
type Divider struct {
	At   Rect // one cell wide or one high
	Side bool
	// Focused is set when the split holds the focused pane: it is drawn
	// in the accent colour.
	Focused bool
}

// Geometry is a view laid out in a window.
type Geometry struct {
	// SideW is the projects sidebar's width, 0 for none; Status is the
	// status bar's height (0 or 1). Area is what the panes share: the
	// window less those.
	SideW, Status int
	Area          Rect
	// Panes are the visible sessions' rectangles.
	Panes    map[string]Rect
	Dividers []Divider
}

// Chrome is the sidebar's width and the status bar's height of v in a
// window cols wide. Every view has the sidebar; a bare one has the status
// bar only when asked for.
func (v *View) Chrome(cols int) (sideW, status int) {
	status = 1
	if v.Bare && !v.StatusBar {
		status = 0
	}
	return v.Sidebar.Cols(cols), status
}

// Lay lays v out in a window of cols×rows.
func (v *View) Lay(cols, rows int) Geometry {
	sideW, status := v.Chrome(cols)
	g := Geometry{SideW: sideW, Status: status, Panes: map[string]Rect{},
		Area: Rect{sideW, 0, max(cols-sideW, 1), max(rows-status, 1)}}
	vis := v.Visible()
	switch {
	case len(vis) == 0:
	case len(vis) == 1:
		g.Panes[vis[0]] = g.Area
	default:
		g.Dividers = v.Root.lay(g.Area, v.Focus, g.Panes, nil)
	}
	return g
}

// SplitSizes divides total cells, less one for the divider, by ratio;
// each side keeps at least one cell.
func SplitSizes(total int, ratio float64) (int, int) {
	room := total - 1
	if room < 2 {
		return max(room, 0), 0
	}
	a := min(max(int(float64(room)*ratio+0.5), 1), room-1)
	return a, room - a
}

// lay gives every leaf under n its rectangle within r, and returns the
// dividers.
func (n *Node) lay(r Rect, focus string, out map[string]Rect, divs []Divider) []Divider {
	if n.Session != "" {
		out[n.Session] = r
		return divs
	}
	focused := n.Has(focus)
	if n.Side {
		aw, bw := SplitSizes(r.W, n.Ratio)
		divs = n.A.lay(Rect{r.X, r.Y, aw, r.H}, focus, out, divs)
		divs = append(divs, Divider{Rect{r.X + aw, r.Y, 1, r.H}, true, focused})
		return n.B.lay(Rect{r.X + aw + 1, r.Y, bw, r.H}, focus, out, divs)
	}
	ah, bh := SplitSizes(r.H, n.Ratio)
	divs = n.A.lay(Rect{r.X, r.Y, r.W, ah}, focus, out, divs)
	divs = append(divs, Divider{Rect{r.X, r.Y + ah, r.W, 1}, false, focused})
	return n.B.lay(Rect{r.X, r.Y + ah + 1, r.W, bh}, focus, out, divs)
}

// Neighbour is the pane next to from in a direction (dx, dy), the one
// sharing the most of from's edge; "" at the window's edge.
func (g Geometry) Neighbour(from string, dx, dy int) string {
	f, ok := g.Panes[from]
	if !ok {
		return ""
	}
	best, bestOverlap := "", 0
	for id, r := range g.Panes {
		var touches bool
		var overlap int
		switch {
		case dx > 0:
			touches, overlap = r.X == f.X+f.W+1, span(r.Y, r.H, f.Y, f.H)
		case dx < 0:
			touches, overlap = r.X+r.W+1 == f.X, span(r.Y, r.H, f.Y, f.H)
		case dy > 0:
			touches, overlap = r.Y == f.Y+f.H+1, span(r.X, r.W, f.X, f.W)
		default:
			touches, overlap = r.Y+r.H+1 == f.Y, span(r.X, r.W, f.X, f.W)
		}
		// Ties go to the smaller id, so every caller picks the same pane.
		if id != from && touches && (overlap > bestOverlap || overlap == bestOverlap && overlap > 0 && id < best) {
			best, bestOverlap = id, overlap
		}
	}
	return best
}

// span is how much [a, a+an) and [b, b+bn) overlap.
func span(a, an, b, bn int) int { return max(min(a+an, b+bn)-max(a, b), 0) }
