package view

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func layout(t *testing.T) *View {
	t.Helper()
	v := &View{Name: Main, Bare: true, Sidebar: Sidebar{Slim: true}}
	v.Attach("a", "proj")
	return v
}

// TestLay: the one pane gets the window less the sidebar, the status bar
// and the empty row above it.
func TestLay(t *testing.T) {
	v := layout(t)
	if v.Focus != "a" || v.Current != "proj" || v.Mode != ModeLayout {
		t.Fatalf("view %+v", v)
	}
	// Every view has the sidebar: here the 10-column slim strip; a bare
	// view has no status bar.
	g := v.Lay(88, 24)
	if g.Pane != "a" || g.Area != (Rect{10, 0, 78, 24}) {
		t.Fatalf("geometry %+v", g)
	}

	// A shared view has the sidebar and the status bar, with an empty
	// row between the pane and it.
	v.Bare, v.Sidebar.Slim = false, false
	g = v.Lay(120, 30)
	if g.SideW != SideDefault || g.Status != 2 || g.Area != (Rect{SideDefault, 0, 120 - SideDefault, 28}) || g.Pane != "a" {
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
	if g = v.Lay(120, 30); g.SideW != SideDefault || g.Status != 2 {
		t.Fatalf("bare chrome with a status bar %+v", g)
	}
	// On the dashboard nothing is laid out.
	v.Dashboard()
	if g = v.Lay(120, 30); g.Pane != "" {
		t.Fatalf("dashboard pane %q", g.Pane)
	}
}

// TestTree: the sidebar's tree state. Showing a project's dashboard makes
// it current with its coordinator's row selected; the highlight follows
// the screen and the focus. Every project is always expanded, so the
// view keeps no expanded projects: views saved with them load.
func TestTree(t *testing.T) {
	v := &View{Name: Main}
	v.Normalize()
	if !v.ShowProject("b") || v.Mode != ModeDashboard || v.Current != "b" || v.Selected != "p:b" || v.ShowProject("b") {
		t.Fatalf("show b: %+v", v)
	}
	if p, s := v.Here(); p != "b" || s != "" {
		t.Fatalf("here on the dashboard: %q %q", p, s)
	}
	v.Attach("s-1", "a")
	if p, s := v.Here(); p != "a" || s != "s-1" {
		t.Fatalf("here attached: %q %q", p, s)
	}
	// The tree state survives the wire.
	b, _ := json.Marshal(v)
	var back View
	if err := json.Unmarshal(b, &back); err != nil || !Equal(*v, back) {
		t.Fatalf("round trip: %s", b)
	}
	// A view saved by protocol 6 and before, with expanded projects.
	old := View{}
	if err := json.Unmarshal([]byte(`{"name":"main","mode":"dashboard","current":"a","expanded":["b","c"]}`), &old); err != nil || old.Current != "a" {
		t.Fatalf("old view: %v %+v", err, old)
	}
	if b, _ := json.Marshal(old); strings.Contains(string(b), "expanded") {
		t.Fatalf("old view kept its expanded projects: %s", b)
	}
}

// TestActions: attach shows a session, replacing the one shown; a
// session that ends, or is gone after a restart, takes the view back to
// the dashboard.
func TestActions(t *testing.T) {
	v := layout(t)
	v.Dashboard()
	if !v.Attach("a", "") || v.Mode != ModeLayout || v.Focus != "a" {
		t.Fatalf("attach a: %+v", v)
	}
	if v.Attach("a", "") {
		t.Fatal("attaching again changed the view")
	}
	v.Attach("d", "other")
	if got := v.Shown(); got != "d" || v.Current != "other" {
		t.Fatalf("attach d: %v %+v", got, v)
	}
	if v.Remove("a") {
		t.Fatal("removed a session it doesn't show")
	}
	if !v.Remove("d") || v.Mode != ModeDashboard || v.Focus != "" {
		t.Fatalf("removing the pane: %+v", v)
	}
	v = layout(t)
	if v.Prune(func(id string) bool { return id == "a" }) || !v.Prune(func(string) bool { return false }) || v.Mode != ModeDashboard {
		t.Fatalf("prune: %+v", v)
	}
}

// TestValidNormalize: a view round-trips; one saved with a split tree
// (before panes were single) keeps the session it had in front.
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
	if strings.Contains(string(b), "root") || strings.Contains(string(b), "zoom") {
		t.Fatalf("split fields on the wire: %s", b)
	}
	old := `{"name":"main","mode":"layout","root":{"side":true,"ratio":0.5,"a":{"session":"s-1"},"b":{"session":"s-2"}},"focus":"s-2","zoom":true}`
	var o View
	if err := json.Unmarshal([]byte(old), &o); err != nil || o.Valid() != nil {
		t.Fatal(err)
	}
	o.Normalize()
	if o.Mode != ModeLayout || o.Focus != "s-2" || o.Shown() != "s-2" {
		t.Fatalf("an old split view: %+v", o)
	}
	if err := (&View{Focus: strings.Repeat("x", MaxKey+1)}).Valid(); err == nil {
		t.Error("an overlong session id is valid")
	}
	v = &View{Mode: "odd"}
	v.Normalize()
	if v.Mode != ModeDashboard || v.Sidebar.Width != SideDefault {
		t.Fatalf("normalize: %+v", v)
	}
	v = &View{Mode: ModeLayout}
	v.Normalize()
	if v.Mode != ModeDashboard {
		t.Fatal("a layout without a pane")
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
		{"no maximum width", Sidebar{Width: 99}, 300, 99},
		{"wider than the window allows: cut to fit", Sidebar{Width: 99}, 120, 120 - SideRoom},
		{"wide setting, narrow window: down to the default", Sidebar{Width: 99}, SideDefault + SideRoom, SideDefault},
		{"wide setting, narrower window: slim strip", Sidebar{Width: 99}, SideDefault + SideRoom - 1, SideSlim},
		{"narrow setting keeps its own threshold", Sidebar{Width: 20}, 80, 20},
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
	// No maximum: } widens past 48 in a wide window.
	if l, _ = (Sidebar{Width: 48}).Key("}", 200); l.Width != 50 {
		t.Fatalf("} past 48: %+v", l)
	}
	// A saved width too wide for the window is shown cut and kept: } at
	// the window's limit leaves it, { steps down from the width shown.
	saved := Sidebar{Width: 100}
	if l, _ = saved.Key("}", 120); l.Width != 100 {
		t.Fatalf("} at the limit lost the saved width: %+v", l)
	}
	if l, _ = saved.Key("{", 120); l.Width != 120-SideRoom-SideStep {
		t.Fatalf("{ from a cut width: %+v", l)
	}
	if _, msg = (Sidebar{Width: 100, Slim: true}).Key("b", SideDefault+SideRoom-1); !strings.Contains(msg, fmt.Sprint(SideDefault+SideRoom)) {
		t.Fatalf("too narrow for a wide setting: %q", msg)
	}
	if l = def.DragTo(3, 120); !l.Slim {
		t.Fatalf("drag to 3: %+v", l)
	}
	if l = def.DragTo(30, 120); l.Slim || l.Width != 31 {
		t.Fatalf("drag to 30: %+v", l)
	}
	// Dragging has no maximum but the window's: the panes keep SideRoom.
	if l = def.DragTo(89, 200); l.Width != 90 {
		t.Fatalf("drag to 89: %+v", l)
	}
	if l = def.DragTo(150, 200); l.Width != 200-SideRoom {
		t.Fatalf("drag to 150: %+v", l)
	}
}

// TestInfo: the info panel beside a thread's pane takes its columns from
// the pane, shrinks to leave the pane SideRoom, and hides when the window
// is too narrow for InfoMin, when it is off, or beside a session that
// isn't a thread's.
func TestInfo(t *testing.T) {
	v := &View{Name: Main}
	v.Attach("a", "proj")
	v.Normalize()
	if v.Info.Width != InfoDefault {
		t.Fatalf("default width %d", v.Info.Width)
	}
	// No thread: no panel.
	if g := v.Lay(200, 40); g.InfoW != 0 || g.Area.W != 200-SideDefault {
		t.Fatalf("not a thread: %+v", g)
	}
	v.Panel = true
	for _, c := range []struct{ cols, side, info int }{
		{200, SideDefault, InfoDefault},                                  // room for everything
		{SideDefault + SideRoom + InfoDefault, SideDefault, InfoDefault}, // just room
		{SideDefault + SideRoom + 30, SideDefault, 30},                   // shrinks
		{SideDefault + SideRoom + InfoMin, SideDefault, InfoMin},
		{SideDefault + SideRoom + InfoMin - 1, SideDefault, 0}, // hides
		{80, SideSlim, 0}, // narrow: the slim strip, no panel
	} {
		g := v.Lay(c.cols, 30)
		if g.SideW != c.side || g.InfoW != c.info || g.Area.W != c.cols-c.side-c.info || g.Pane != "a" {
			t.Errorf("%d columns: %+v, want sidebar %d, panel %d", c.cols, g, c.side, c.info)
		}
		if g.InfoW > 0 && g.Area.W < SideRoom {
			t.Errorf("%d columns: the pane gets %d", c.cols, g.Area.W)
		}
	}
	// Off: hidden; on again in a narrow window says why it shows nothing.
	off, msg := v.Info.Toggle(200, SideDefault)
	if !off.Off || msg != "" {
		t.Fatalf("toggle off: %+v %q", off, msg)
	}
	v.Info = off
	if g := v.Lay(200, 30); g.InfoW != 0 {
		t.Fatalf("off: %+v", g)
	}
	on, msg := off.Toggle(80, SideSlim)
	if on.Off || !strings.Contains(msg, "too narrow") {
		t.Fatalf("toggle on, narrow: %+v %q", on, msg)
	}
	// Dragging its border: from the right edge, at least InfoMin, never
	// past SideRoom for the pane; a wider width is kept for wider windows.
	v.Info = Info{}.Clamp()
	if d := v.Info.DragTo(150, 200, SideDefault); d.Width != 50 {
		t.Fatalf("drag to 150: %+v", d)
	}
	if d := v.Info.DragTo(199, 200, SideDefault); d.Width != InfoMin {
		t.Fatalf("drag to the edge: %+v", d)
	}
	if d := v.Info.DragTo(10, 200, SideDefault); d.Width != 200-SideDefault-SideRoom {
		t.Fatalf("drag over the pane: %+v", d)
	}
	v.Info.Width = 150
	if g := v.Lay(200, 30); g.InfoW != 200-SideDefault-SideRoom {
		t.Fatalf("a wide panel in a narrower window: %+v", g)
	}
	// The dashboard and an emptied view lay out no panel.
	v.Dashboard()
	if g := v.Lay(200, 30); g.InfoW != 0 {
		t.Fatalf("dashboard: %+v", g)
	}
	v.Remove("a")
	if v.Panel {
		t.Fatal("Thread stays without a session")
	}
}
