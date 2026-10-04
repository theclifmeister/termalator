package view

import (
	"encoding/json"
	"strings"
	"testing"
)

func layout(t *testing.T) *View {
	t.Helper()
	v := &View{Name: Main, Bare: true, Sidebar: Sidebar{Slim: true}}
	v.Attach("a", "proj")
	// a | b, then b split below: a | (b / c).
	if !v.Split("a", "b", true) || !v.Split("b", "c", false) {
		t.Fatal("split")
	}
	return v
}

func TestLay(t *testing.T) {
	v := layout(t)
	if v.Focus != "c" || v.Current != "proj" || v.Mode != ModeLayout {
		t.Fatalf("view %+v", v)
	}
	// Every view has the sidebar: here the 7-column slim strip.
	g := v.Lay(88, 24)
	if g.Panes["a"] != (Rect{7, 0, 40, 24}) || g.Panes["b"] != (Rect{48, 0, 40, 12}) || g.Panes["c"] != (Rect{48, 13, 40, 11}) {
		t.Fatalf("rects %+v", g.Panes)
	}
	if len(g.Dividers) != 2 || g.Dividers[0].At != (Rect{47, 0, 1, 24}) || !g.Dividers[0].Side || g.Dividers[1].At != (Rect{48, 12, 40, 1}) {
		t.Fatalf("dividers %+v", g.Dividers)
	}
	if !g.Dividers[0].Focused || !g.Dividers[1].Focused {
		t.Fatal("c's splits aren't focused")
	}
	if got := strings.Join(v.Root.Leaves(), ""); got != "abc" {
		t.Fatalf("leaves %s", got)
	}
	if g.Neighbour("a", 1, 0) != "b" || g.Neighbour("c", -1, 0) != "a" || g.Neighbour("b", 0, 1) != "c" ||
		g.Neighbour("c", 0, -1) != "b" || g.Neighbour("a", -1, 0) != "" {
		t.Fatal("neighbours")
	}

	// The chrome: a shared view has the sidebar and the status bar; a
	// bare one the sidebar, and the status bar when asked for.
	v.Bare, v.Sidebar.Slim = false, false
	g = v.Lay(120, 30)
	if g.SideW != SideDefault || g.Status != 1 || g.Area != (Rect{SideDefault, 0, 120 - SideDefault, 29}) {
		t.Fatalf("chrome %+v", g)
	}
	// Narrow: the slim strip.
	if g = v.Lay(70, 30); g.SideW != SideSlim {
		t.Fatalf("narrow: sidebar %d", g.SideW)
	}
	v.Bare = true
	if g = v.Lay(120, 30); g.SideW != SideDefault || g.Status != 0 {
		t.Fatalf("bare chrome %+v", g)
	}
	v.StatusBar = true
	if g = v.Lay(120, 30); g.SideW != SideDefault || g.Status != 1 {
		t.Fatalf("bare chrome with a status bar %+v", g)
	}
}

// TestTree: the sidebar's tree state. The current project is always
// expanded, others open and close; showing a project's dashboard makes it
// current with its coordinator's row selected; the highlight follows
// the screen and the focus.
func TestTree(t *testing.T) {
	v := &View{Name: Main}
	v.Normalize()
	if !v.ShowProject("b") || v.Mode != ModeDashboard || v.Current != "b" || v.Selected != "p:b" || v.ShowProject("b") {
		t.Fatalf("show b: %+v", v)
	}
	if p, s := v.Here(); p != "b" || s != "" {
		t.Fatalf("here on the dashboard: %q %q", p, s)
	}
	if !v.IsExpanded("b") || v.IsExpanded("a") {
		t.Fatal("only the current project is expanded")
	}
	if !v.Expand("c", true) || !v.Expand("a", true) || v.Expand("a", true) {
		t.Fatal("expand")
	}
	if strings.Join(v.Expanded, ",") != "a,c" || !v.IsExpanded("a") {
		t.Fatalf("expanded %v", v.Expanded)
	}
	if !v.Expand("a", false) || v.Expand("a", false) || v.IsExpanded("a") {
		t.Fatal("collapse")
	}
	// Collapsing the current project keeps it open.
	v.Expand("b", true)
	v.Expand("b", false)
	if !v.IsExpanded("b") {
		t.Fatal("the current project closed")
	}
	v.Attach("s-1", "a")
	if p, s := v.Here(); p != "a" || s != "s-1" {
		t.Fatalf("here attached: %q %q", p, s)
	}
	// The tree state survives the wire, and a clone owns its list.
	c := v.Clone()
	c.Expanded[0] = "z"
	if v.Expanded[0] != "c" {
		t.Fatal("a clone shares Expanded")
	}
	b, _ := json.Marshal(v)
	var back View
	if err := json.Unmarshal(b, &back); err != nil || !Equal(*v, back) {
		t.Fatalf("round trip: %s", b)
	}
	// Normalize sorts and dedupes.
	v.Expanded = []string{"q", "c", "q"}
	v.Normalize()
	if strings.Join(v.Expanded, ",") != "c,q" {
		t.Fatalf("normalized %v", v.Expanded)
	}
}

func TestActions(t *testing.T) {
	v := layout(t)
	// ctrl+→ on c moves the side divider right; ctrl+↓ on a finds no
	// stacked split around a.
	if !v.ResizeTowards(true, 8, 88, 24) {
		t.Fatal("no side split to resize")
	}
	if g := v.Lay(88, 24); g.Panes["a"].W != 48 || g.Panes["b"].X != 56 {
		t.Fatalf("after resize %+v", g.Panes)
	}
	v.FocusOn("a")
	if v.ResizeTowards(false, 1, 88, 24) {
		t.Fatal("resized a stacked split a isn't in")
	}

	// Focus: next, and by direction; a zoomed view unzooms first.
	if !v.FocusNext() || v.Focus != "b" {
		t.Fatalf("next: %s", v.Focus)
	}
	if !v.ToggleZoom() || len(v.Visible()) != 1 || v.Visible()[0] != "b" {
		t.Fatalf("zoom: %v", v.Visible())
	}
	if !v.FocusDir(0, 1, 88, 24) || v.Zoom || v.Focus != "c" {
		t.Fatalf("down from b: %s zoom %v", v.Focus, v.Zoom)
	}

	// Removing b: c takes its place beside a and keeps the focus.
	if !v.Remove("b") || v.Focus != "c" {
		t.Fatalf("remove: focus %s", v.Focus)
	}
	if g := v.Lay(88, 24); len(g.Panes) != 2 || g.Panes["c"].H != 24 || g.Panes["c"].X != 56 {
		t.Fatalf("after remove: %+v", g.Panes)
	}
	// Removing the focused pane moves the focus.
	v.Remove("c")
	if v.Focus != "a" || v.Mode != ModeLayout {
		t.Fatalf("after removing c: %+v", v)
	}
	if !v.Remove("a") || v.Root != nil || v.Mode != ModeDashboard || v.Focus != "" {
		t.Fatalf("removing the last pane: %+v", v)
	}

	// Even: three panes stacked, equal.
	v = layout(t)
	v.Even()
	if g := v.Lay(80, 26); g.Panes["a"].H != 8 || g.Panes["b"].H != 8 || g.Panes["c"].H != 8 || g.Panes["c"].Y != 18 {
		t.Fatalf("even: %+v", g.Panes)
	}

	// Attach: a session in the layout gets the focus, the layout stays;
	// another one replaces it.
	v.Dashboard()
	if !v.Attach("a", "") || v.Mode != ModeLayout || v.Focus != "a" || len(v.Root.Leaves()) != 3 {
		t.Fatalf("attach a: %+v", v)
	}
	if v.Attach("a", "") {
		t.Fatal("attaching again changed the view")
	}
	v.Attach("d", "other")
	if got := v.Root.Leaves(); len(got) != 1 || got[0] != "d" || v.Current != "other" {
		t.Fatalf("attach d: %v %+v", got, v)
	}

	// Prune drops the sessions that are gone.
	v = layout(t)
	if !v.Prune(func(id string) bool { return id == "b" }) || strings.Join(v.Root.Leaves(), "") != "b" || v.Focus != "b" {
		t.Fatalf("prune: %+v", v)
	}
}

func TestSplitSizes(t *testing.T) {
	for _, c := range []struct {
		total int
		ratio float64
		a, b  int
	}{{81, 0.5, 40, 40}, {80, 0.5, 40, 39}, {3, 0.5, 1, 1}, {3, 0.99, 1, 1}, {2, 0.5, 1, 0}, {10, 0.01, 1, 8}} {
		if a, b := SplitSizes(c.total, c.ratio); a != c.a || b != c.b {
			t.Errorf("SplitSizes(%d, %v) = %d, %d; want %d, %d", c.total, c.ratio, a, b, c.a, c.b)
		}
	}
}

func TestValidNormalize(t *testing.T) {
	v := layout(t)
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var back View
	if err := json.Unmarshal(b, &back); err != nil || !Equal(*v, back) || back.Valid() != nil {
		t.Fatalf("round trip: %v %v\n%s", err, back.Valid(), b)
	}
	for _, bad := range []string{
		`{"root":{"session":"a","a":{"session":"b"}}}`,
		`{"root":{"side":true,"a":{"session":"a"}}}`,
		`{"root":{"a":{"session":"a"},"b":{"session":"a"}}}`,
		`{"root":{"session":"a"},"focus":"b"}`,
	} {
		var v View
		if err := json.Unmarshal([]byte(bad), &v); err != nil {
			t.Fatal(err)
		}
		if v.Valid() == nil {
			t.Errorf("%s is valid", bad)
		}
	}
	v = &View{Mode: "odd", Root: &Node{A: &Node{Session: "a"}, B: &Node{Session: "b"}, Ratio: 7}}
	v.Normalize()
	if v.Mode != ModeDashboard || v.Root.Ratio != 0.5 || v.Focus != "a" || v.Sidebar.Width != SideDefault {
		t.Fatalf("normalize: %+v", v)
	}
	v = &View{Mode: ModeLayout}
	v.Normalize()
	if v.Mode != ModeDashboard {
		t.Fatal("a layout without panes")
	}
}

func TestSidebar(t *testing.T) {
	def := Sidebar{}.Clamp()
	cases := []struct {
		name string
		l    Sidebar
		w    int
		want int
	}{
		{"default", def, 120, SideDefault},
		{"narrow window: slim strip", def, SideDefault + SideRoom - 1, SideSlim},
		{"slim asked for", Sidebar{Width: 30, Slim: true}, 200, SideSlim},
		{"tiny window: still there", def, 4, 3},
		{"too wide a setting", Sidebar{Width: 99}, 300, SideMax},
	}
	for _, c := range cases {
		if got := c.l.Cols(c.w); got != c.want {
			t.Errorf("%s: %d columns, want %d", c.name, got, c.want)
		}
	}

	l, msg := def.Key("}", 120)
	if l.Width != SideDefault+SideStep || msg != "" {
		t.Fatalf("} gave %+v %q", l, msg)
	}
	if l, _ = l.Key("{", 120); l.Width != SideDefault {
		t.Fatalf("{ gave %+v", l)
	}
	if l, _ = l.Key("b", 120); !l.Slim || l.Cols(120) != SideSlim {
		t.Fatalf("b gave %+v", l)
	}
	if l, _ = l.Key("}", 120); l.Slim {
		t.Fatalf("} kept the slim strip: %+v", l)
	}
	if _, msg = def.Key("}", 70); !strings.Contains(msg, "too narrow") {
		t.Fatalf("} in a narrow window: %q", msg)
	}
	// Widening never leaves the panes less than SideRoom.
	wide := Sidebar{Width: 40}
	if l, _ = wide.Key("}", 101); l.Width != 41 {
		t.Fatalf("} in 101 columns: %+v", l)
	}
	if l = def.DragTo(3, 120); !l.Slim {
		t.Fatalf("drag to 3: %+v", l)
	}
	if l = def.DragTo(30, 120); l.Slim || l.Width != 31 {
		t.Fatalf("drag to 30: %+v", l)
	}
}

// TestDragDivider: dragging the side divider at column 47 to 60 moves it
// there; dragging b/c's divider moves only that one; a cell that is no
// divider, and a zoomed view, move nothing.
func TestDragDivider(t *testing.T) {
	v := layout(t)
	if !v.DragDivider(47, 5, 60, 88, 24) {
		t.Fatal("side divider didn't move")
	}
	g := v.Lay(88, 24)
	if g.Dividers[0].At.X != 60 || g.Panes["a"].W != 53 || g.Panes["b"].X != 61 {
		t.Fatalf("after drag %+v %+v", g.Dividers, g.Panes)
	}
	if !v.DragDivider(70, 12, 5, 88, 24) {
		t.Fatal("stacked divider didn't move")
	}
	g = v.Lay(88, 24)
	if g.Dividers[1].At.Y != 5 || g.Panes["b"].H != 5 || g.Dividers[0].At.X != 60 {
		t.Fatalf("after second drag %+v", g.Dividers)
	}
	if v.DragDivider(20, 5, 30, 88, 24) {
		t.Fatal("a pane's cell moved a divider")
	}
	// Past the edge: the pane keeps a sliver.
	v.DragDivider(60, 0, 0, 88, 24)
	if g := v.Lay(88, 24); g.Panes["a"].W < 1 {
		t.Fatalf("a vanished: %+v", g.Panes)
	}
	v.ToggleZoom()
	if v.DragDivider(60, 0, 70, 88, 24) {
		t.Fatal("dragged in a zoomed view")
	}
}
