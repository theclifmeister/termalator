package tui

// Split panes in the attach view (docs/SPEC.md §4): the window is a tree
// of splits whose leaves are sessions. The tree is geometry only; the
// attach client keeps the connections and mirrors.

// rect is a rectangle of window cells, 0-based.
type rect struct{ x, y, w, h int }

// node is a split or, with leaf set, one pane.
type node struct {
	leaf *pane
	// side is true for panes side by side (a divider column between
	// them), false for one above the other (a divider row).
	side   bool
	ratio  float64 // a's share of the room left after the divider
	a, b   *node
	parent *node
}

// divider is a split's line: a column when side, else a row.
type divider struct {
	at   rect // one cell wide or one high
	side bool
	n    *node // the split it belongs to
}

// splitSizes divides total cells, less one for the divider, by ratio;
// each side keeps at least one cell.
func splitSizes(total int, ratio float64) (int, int) {
	room := total - 1
	if room < 2 {
		return max(room, 0), 0
	}
	a := min(max(int(float64(room)*ratio+0.5), 1), room-1)
	return a, room - a
}

// layout gives every leaf under n its rectangle within r, and returns
// the dividers.
func (n *node) layout(r rect, out []divider) []divider {
	if n.leaf != nil {
		n.leaf.rect = r
		return out
	}
	if n.side {
		aw, bw := splitSizes(r.w, n.ratio)
		out = n.a.layout(rect{r.x, r.y, aw, r.h}, out)
		out = append(out, divider{rect{r.x + aw, r.y, 1, r.h}, true, n})
		return n.b.layout(rect{r.x + aw + 1, r.y, bw, r.h}, out)
	}
	ah, bh := splitSizes(r.h, n.ratio)
	out = n.a.layout(rect{r.x, r.y, r.w, ah}, out)
	out = append(out, divider{rect{r.x, r.y + ah, r.w, 1}, false, n})
	return n.b.layout(rect{r.x, r.y + ah + 1, r.w, bh}, out)
}

// leaves are the panes under n, left to right and top to bottom.
func (n *node) leaves(out []*pane) []*pane {
	if n == nil {
		return out
	}
	if n.leaf != nil {
		return append(out, n.leaf)
	}
	return n.b.leaves(n.a.leaves(out))
}

// find is the leaf node holding p.
func (n *node) find(p *pane) *node {
	if n == nil {
		return nil
	}
	if n.leaf == p {
		return n
	}
	if n.leaf != nil {
		return nil
	}
	if f := n.a.find(p); f != nil {
		return f
	}
	return n.b.find(p)
}

// contains says whether p is under n.
func (n *node) contains(p *pane) bool { return n.find(p) != nil }

// splitLeaf splits the leaf holding at in two, at first and the new pane
// second, and returns the tree's root (which changes when at was it).
func splitLeaf(root *node, at, add *pane, side bool) *node {
	l := root.find(at)
	if l == nil {
		return root
	}
	n := &node{side: side, ratio: 0.5, parent: l.parent}
	n.a = &node{leaf: at, parent: n}
	n.b = &node{leaf: add, parent: n}
	return replace(root, l, n)
}

// removeLeaf takes p's leaf out; its sibling takes the split's place.
// It returns the new root, nil when p was the only pane.
func removeLeaf(root *node, p *pane) *node {
	l := root.find(p)
	if l == nil {
		return root
	}
	split := l.parent
	if split == nil {
		return nil
	}
	sib := split.a
	if sib == l {
		sib = split.b
	}
	sib.parent = split.parent
	return replace(root, split, sib)
}

// replace puts n where old was and returns the root.
func replace(root, old, n *node) *node {
	p := old.parent
	n.parent = p
	switch {
	case p == nil:
		return n
	case p.a == old:
		p.a = n
	default:
		p.b = n
	}
	return root
}

// resizeTowards moves the divider nearest to p on the given axis by
// cells: positive moves it right or down. It reports whether there was
// one to move.
func resizeTowards(p *pane, n *node, side bool, cells int) bool {
	for l := n.find(p); l != nil && l.parent != nil; l = l.parent {
		s := l.parent
		if s.side != side {
			continue
		}
		total := s.a.size(side) + s.b.size(side) // the room less the divider
		if total < 2 {
			return false
		}
		s.ratio = min(max(s.ratio+float64(cells)/float64(total), 0.05), 0.95)
		return true
	}
	return false
}

// size is n's width (side) or height, from the last layout.
func (n *node) size(side bool) int {
	ps := n.leaves(nil)
	if len(ps) == 0 {
		return 0
	}
	lo, hi := 1<<30, 0
	for _, p := range ps {
		a, z := p.rect.y, p.rect.y+p.rect.h
		if side {
			a, z = p.rect.x, p.rect.x+p.rect.w
		}
		lo, hi = min(lo, a), max(hi, z)
	}
	return hi - lo
}

// even rebuilds the tree as one row (side) or column of equal panes.
func even(ps []*pane, side bool) *node {
	if len(ps) == 1 {
		return &node{leaf: ps[0]}
	}
	n := &node{side: side, ratio: 1 / float64(len(ps))}
	n.a = &node{leaf: ps[0], parent: n}
	n.b = even(ps[1:], side)
	n.b.parent = n
	return n
}

// neighbour is the pane next to from in a direction (dx, dy), the one
// sharing the most of from's edge; nil at the window's edge.
func neighbour(ps []*pane, from *pane, dx, dy int) *pane {
	f := from.rect
	var best *pane
	bestOverlap := 0
	for _, p := range ps {
		r := p.rect
		var touches bool
		var overlap int
		switch {
		case dx > 0:
			touches, overlap = r.x == f.x+f.w+1, span(r.y, r.h, f.y, f.h)
		case dx < 0:
			touches, overlap = r.x+r.w+1 == f.x, span(r.y, r.h, f.y, f.h)
		case dy > 0:
			touches, overlap = r.y == f.y+f.h+1, span(r.x, r.w, f.x, f.w)
		default:
			touches, overlap = r.y+r.h+1 == f.y, span(r.x, r.w, f.x, f.w)
		}
		if p != from && touches && overlap > bestOverlap {
			best, bestOverlap = p, overlap
		}
	}
	return best
}

// span is how much [a, a+an) and [b, b+bn) overlap.
func span(a, an, b, bn int) int { return max(min(a+an, b+bn)-max(a, b), 0) }
