package tui

import "testing"

func TestPaneLayout(t *testing.T) {
	a, b, c := &pane{}, &pane{}, &pane{}
	root := &node{leaf: a}
	// a | b, then b split below: a | (b / c).
	root = splitLeaf(root, a, b, true)
	root = splitLeaf(root, b, c, false)
	divs := root.layout(rect{0, 0, 81, 24}, nil)
	if a.rect != (rect{0, 0, 40, 24}) || b.rect != (rect{41, 0, 40, 12}) || c.rect != (rect{41, 13, 40, 11}) {
		t.Fatalf("rects a %+v b %+v c %+v", a.rect, b.rect, c.rect)
	}
	if len(divs) != 2 || divs[0].at != (rect{40, 0, 1, 24}) || !divs[0].side || divs[1].at != (rect{41, 12, 40, 1}) {
		t.Fatalf("dividers %+v", divs)
	}
	if got := root.leaves(nil); len(got) != 3 || got[0] != a || got[1] != b || got[2] != c {
		t.Fatalf("leaves %v", got)
	}

	// Focus moves to the neighbour sharing the most edge.
	ps := root.leaves(nil)
	if neighbour(ps, a, 1, 0) != b || neighbour(ps, c, -1, 0) != a || neighbour(ps, b, 0, 1) != c ||
		neighbour(ps, c, 0, -1) != b || neighbour(ps, a, -1, 0) != nil {
		t.Fatal("neighbours")
	}

	// ctrl+→ on c moves the side divider right; ctrl+↓ on a finds no
	// stacked split around a.
	if !resizeTowards(c, root, true, 8) {
		t.Fatal("no side split to resize")
	}
	root.layout(rect{0, 0, 81, 24}, nil)
	if a.rect.w != 48 || b.rect.x != 49 {
		t.Fatalf("after resize a %+v b %+v", a.rect, b.rect)
	}
	if resizeTowards(a, root, false, 1) {
		t.Fatal("resized a stacked split a isn't in")
	}

	// Removing b: c takes its place beside a.
	root = removeLeaf(root, b)
	root.layout(rect{0, 0, 81, 24}, nil)
	if got := root.leaves(nil); len(got) != 2 || c.rect.h != 24 || c.rect.x != 49 {
		t.Fatalf("after remove: %v c %+v", got, c.rect)
	}
	if removeLeaf(removeLeaf(root, a), c) != nil {
		t.Fatal("removing the last pane leaves a tree")
	}

	// even: three panes stacked, equal.
	root = even([]*pane{a, b, c}, false)
	root.layout(rect{0, 0, 80, 26}, nil)
	if a.rect.h != 8 || b.rect.h != 8 || c.rect.h != 8 || c.rect.y != 18 {
		t.Fatalf("even: a %+v b %+v c %+v", a.rect, b.rect, c.rect)
	}
}

func TestSplitSizes(t *testing.T) {
	for _, c := range []struct {
		total int
		ratio float64
		a, b  int
	}{{81, 0.5, 40, 40}, {80, 0.5, 40, 39}, {3, 0.5, 1, 1}, {3, 0.99, 1, 1}, {2, 0.5, 1, 0}, {10, 0.01, 1, 8}} {
		if a, b := splitSizes(c.total, c.ratio); a != c.a || b != c.b {
			t.Errorf("splitSizes(%d, %v) = %d, %d; want %d, %d", c.total, c.ratio, a, b, c.a, c.b)
		}
	}
}
