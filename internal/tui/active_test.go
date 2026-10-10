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
			if line := treeCells(r, 30, false, false); !strings.Contains(ansi.Strip(line), "beta"+ic().inactive) {
				t.Errorf("row %q, want %q", line, "beta"+ic().inactive)
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
	if !strings.Contains(whole(m), "Deactivate alpha? Its coordinator and t-0002") {
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
