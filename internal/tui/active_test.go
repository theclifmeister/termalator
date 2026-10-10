package tui

import (
	"slices"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/theclifmeister/terminatr/internal/config"
)

// TestInactiveProjectCollapsed: an inactive project is its sidebar row
// alone, marked; ] skips it; space on it activates it, and on an active
// project with running sessions asks first (docs/SPEC.md §4, §5.1).
func TestInactiveProjectCollapsed(t *testing.T) {
	d := testData()
	off := config.Defaults
	off.Active = false
	d.Projects[1].Safety = &off // beta: a thread runs, a coordinator doesn't
	var keys []string
	for _, r := range buildTree(d.Projects, d.Sessions, treeIn{}) {
		keys = append(keys, r.key())
		if r.slug == "beta" && !r.inactive {
			t.Errorf("%s isn't marked inactive", r.key())
		}
	}
	if want := []string{"p:alpha", "c:alpha", "p:beta"}; !slices.Equal(keys, want) {
		t.Errorf("tree %v, want beta collapsed: %v", keys, want)
	}
	// Its row says so in every icon set's glyph.
	for _, r := range buildTree(d.Projects, d.Sessions, treeIn{}) {
		if r.slug == "beta" {
			// At the far right, after the paused slot, before the hint's.
			if line := ansi.Strip(treeCells(r, 30, false, false)); !strings.HasSuffix(line, " "+ic().inactive+" ") {
				t.Errorf("row %q, want it to end in %q", line, ic().inactive)
			}
		}
	}

	src := &fakeSource{data: d}
	m := newDash(DashOptions{Source: src, Width: 120, Height: 30, State: DashState{Current: "alpha"}})
	m.setData(src.data)
	run(m, keyPress(m, "]"))
	if !slices.Equal(src.opened, []string{"alpha"}) {
		t.Errorf("] opened %v; it skips the inactive beta, back to alpha", src.opened)
	}

	// space on beta activates it at once; on alpha, whose coordinator
	// runs, it asks, and n keeps it.
	keyPress(m, "tab")
	m.sideSel = "p:beta"
	run(m, keyPress(m, "space"))
	m.sideSel = "p:alpha"
	run(m, keyPress(m, "space"))
	if len(src.lifecycle) != 1 || src.lifecycle[0] != "beta activate" {
		t.Fatalf("lifecycle %v, want beta activated and alpha asked", src.lifecycle)
	}
	if !strings.Contains(whole(m), "Deactivate alpha? This stops its coordinator and t-0002") {
		t.Fatalf("no question:\n%s", whole(m))
	}
	keyPress(m, "n")
	m.sideSel = "p:alpha"
	run(m, keyPress(m, "space"))
	run(m, keyPress(m, "y"))
	if want := []string{"beta activate", "alpha deactivate"}; !slices.Equal(src.lifecycle, want) {
		t.Errorf("lifecycle %v, want %v", src.lifecycle, want)
	}
}

// TestExpandInactiveProject: → expands an inactive project without
// activating it, its coordinator and threads dormant; a dormant thread
// doesn't attach; ← folds it; enter on its coordinator activates the
// project and opens the coordinator (docs/SPEC.md §4, §5.1).
func TestExpandInactiveProject(t *testing.T) {
	d := testData()
	off := config.Defaults
	off.Active = false
	d.Projects[1].Safety = &off
	src := &fakeSource{data: d}
	m := newDash(DashOptions{Source: src, Width: 120, Height: 30, State: DashState{Current: "alpha"}})
	m.setData(src.data)
	keyPress(m, "tab")
	m.sideSel = "p:beta"
	run(m, keyPress(m, "right"))
	var keys []string
	for _, r := range m.tree() {
		keys = append(keys, r.key())
		if r.slug == "beta" && r.kind != treeProject && !r.dormant {
			t.Errorf("%s isn't dormant", r.key())
		}
	}
	if want := []string{"p:alpha", "c:alpha", "p:beta", "c:beta", "t:beta/t-0005", "t:beta/t-0006"}; !slices.Equal(keys, want) {
		t.Fatalf("expanded tree %v, want %v", keys, want)
	}
	if m.sideSel != "c:beta" || len(src.lifecycle) != 0 {
		t.Fatalf("cursor %q, lifecycle %v: expanding moves onto the coordinator and activates nothing", m.sideSel, src.lifecycle)
	}
	if !strings.Contains(whole(m), "coordinator") || !strings.Contains(whole(m), "T4 ") {
		t.Fatalf("expanded beta:\n%s", whole(m))
	}
	// A dormant thread says why it doesn't attach.
	m.sideSel = "t:beta/t-0005"
	run(m, keyPress(m, "enter"))
	if !strings.Contains(m.msg, "t-0005 is dormant") || m.result.Attach != "" {
		t.Fatalf("enter on a dormant thread: msg %q attach %q", m.msg, m.result.Attach)
	}
	// ← on the project folds it.
	m.sideSel = "p:beta"
	run(m, keyPress(m, "left"))
	if slices.ContainsFunc(m.tree(), func(r treeRow) bool { return r.key() == "c:beta" }) {
		t.Fatal("← didn't fold beta")
	}
	// Expanded again, enter on its coordinator activates it and opens it.
	run(m, keyPress(m, "right"))
	run(m, keyPress(m, "enter"))
	if !slices.Equal(src.lifecycle, []string{"beta activate"}) || !slices.Contains(src.opened, "beta") {
		t.Fatalf("lifecycle %v, opened %v: want beta activated and its coordinator opened", src.lifecycle, src.opened)
	}
}

// TestDashboardActiveToggle: the project's section header names its
// state, the footer says what space does, and space on the dashboard
// toggles the project shown, asking first while sessions run.
func TestDashboardActiveToggle(t *testing.T) {
	d := testData()
	off := config.Defaults
	off.Active = false
	d.Projects[1].Safety = &off // beta
	src := &fakeSource{data: d}
	m := newDash(DashOptions{Source: src, Width: 120 + sideDefault, Height: 30, State: DashState{Current: "beta"}})
	m.layout.Details = false
	m.setData(src.data)
	m.sel = "p:beta"
	out := whole(m)
	if !strings.Contains(out, " beta · inactive ─") || !strings.Contains(out, "space activate") {
		t.Fatalf("inactive project: header or footer missing:\n%s", out)
	}
	run(m, keyPress(m, "space"))
	if !slices.Equal(src.lifecycle, []string{"beta activate"}) {
		t.Fatalf("lifecycle %v, want beta activated", src.lifecycle)
	}
	if out := whole(m); !strings.Contains(out, " beta · active ─") || !strings.Contains(out, "space deactivate") {
		t.Fatalf("after activating:\n%s", out)
	}
	m.current, m.sel = "alpha", "p:alpha"
	m.rebuild()
	on := config.Defaults
	m.projectData("alpha").Safety = &on
	run(m, keyPress(m, "space"))
	if len(src.lifecycle) != 1 || !strings.Contains(whole(m), "Deactivate alpha?") {
		t.Fatalf("alpha runs sessions: want a question, lifecycle %v:\n%s", src.lifecycle, whole(m))
	}
	run(m, keyPress(m, "y"))
	if !slices.Equal(src.lifecycle, []string{"beta activate", "alpha deactivate"}) {
		t.Errorf("lifecycle %v", src.lifecycle)
	}
}
