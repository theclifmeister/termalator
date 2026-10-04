package view

import (
	"encoding/json"
	"strings"
	"testing"
)

func layout(t *testing.T) *View {
	t.Helper()
	v := &View{Name: Main, Bare: true}
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
	g := v.Lay(81, 24)
	if g.Panes["a"] != (Rect{0, 0, 40, 24}) || g.Panes["b"] != (Rect{41, 0, 40, 12}) || g.Panes["c"] != (Rect{41, 13, 40, 11}) {
		t.Fatalf("rects %+v", g.Panes)
	}
	if len(g.Dividers) != 2 || g.Dividers[0].At != (Rect{40, 0, 1, 24}) || !g.Dividers[0].Side || g.Dividers[1].At != (Rect{41, 12, 40, 1}) {
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

	// The chrome: a shared view has the sidebar and the status bar.
	v.Bare = false
	g = v.Lay(120, 30)
	if g.SideW != SideDefault || g.Status != 1 || g.Area != (Rect{SideDefault, 0, 120 - SideDefault, 29}) {
		t.Fatalf("chrome %+v", g)
	}
	// Narrow: the slim strip.
	if g = v.Lay(70, 30); g.SideW != SideSlim {
		t.Fatalf("narrow: sidebar %d", g.SideW)
	}
}

func TestActions(t *testing.T) {
	v := layout(t)
	// ctrl+→ on c moves the side divider right; ctrl+↓ on a finds no
	// stacked split around a.
	if !v.ResizeTowards(true, 8, 81, 24) {
		t.Fatal("no side split to resize")
	}
	if g := v.Lay(81, 24); g.Panes["a"].W != 48 || g.Panes["b"].X != 49 {
		t.Fatalf("after resize %+v", g.Panes)
	}
	v.FocusOn("a")
	if v.ResizeTowards(false, 1, 81, 24) {
		t.Fatal("resized a stacked split a isn't in")
	}

	// Focus: next, and by direction; a zoomed view unzooms first.
	if !v.FocusNext() || v.Focus != "b" {
		t.Fatalf("next: %s", v.Focus)
	}
	if !v.ToggleZoom() || len(v.Visible()) != 1 || v.Visible()[0] != "b" {
		t.Fatalf("zoom: %v", v.Visible())
	}
	if !v.FocusDir(0, 1, 81, 24) || v.Zoom || v.Focus != "c" {
		t.Fatalf("down from b: %s zoom %v", v.Focus, v.Zoom)
	}

	// Removing b: c takes its place beside a and keeps the focus.
	if !v.Remove("b") || v.Focus != "c" {
		t.Fatalf("remove: focus %s", v.Focus)
	}
	if g := v.Lay(81, 24); len(g.Panes) != 2 || g.Panes["c"].H != 24 || g.Panes["c"].X != 49 {
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
