package tui

import (
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestSidebarLayout(t *testing.T) {
	def := SidebarLayout{}.clamp()
	cases := []struct {
		name string
		l    SidebarLayout
		w    int
		want int
	}{
		{"default", def, 120, sideDefault},
		{"narrow window: slim strip", def, sideDefault + sideRoom - 1, sideSlim},
		{"slim asked for", SidebarLayout{Width: 30, Slim: true}, 200, sideSlim},
		{"tiny window: still there", def, 4, 3},
		{"too wide a setting", SidebarLayout{Width: 99}, 300, sideMax},
	}
	for _, c := range cases {
		if got := c.l.cols(c.w); got != c.want {
			t.Errorf("%s: %d columns, want %d", c.name, got, c.want)
		}
	}

	l, msg := def.sideKey("}", 120)
	if l.Width != sideDefault+sideStep || msg != "" {
		t.Fatalf("} gave %+v %q", l, msg)
	}
	if l, _ = l.sideKey("{", 120); l.Width != sideDefault {
		t.Fatalf("{ gave %+v", l)
	}
	if l, _ = l.sideKey("b", 120); !l.Slim || l.cols(120) != sideSlim {
		t.Fatalf("b gave %+v", l)
	}
	if l, _ = l.sideKey("}", 120); l.Slim {
		t.Fatalf("} kept the slim strip: %+v", l)
	}
	if _, msg = def.sideKey("}", 70); !strings.Contains(msg, "too narrow") {
		t.Fatalf("} in a narrow window: %q", msg)
	}
	// Widening never leaves the panes less than sideRoom.
	wide := SidebarLayout{Width: 40}
	if l, _ = wide.sideKey("}", 101); l.Width != 41 {
		t.Fatalf("} in 101 columns: %+v", l)
	}
	if l = def.dragTo(3, 120); !l.Slim {
		t.Fatalf("drag to 3: %+v", l)
	}
	if l = def.dragTo(30, 120); l.Slim || l.Width != 31 {
		t.Fatalf("drag to 30: %+v", l)
	}
}

// TestDashboardSidebar: every project with its glyph and thread count,
// the current one marked; a click opens a project's coordinator, the
// border drags and is saved, and a narrow window gets the slim strip.
func TestDashboardSidebar(t *testing.T) {
	src := &fakeSource{data: testData()}
	ui := filepath.Join(t.TempDir(), "ui.json")
	m := newDash(DashOptions{Source: src, Width: 120, Height: 30, UIFile: ui, State: DashState{Current: "beta"}})
	m.setData(src.data)
	out := whole(m)
	for _, want := range []string{" PROJECTS 2            │ termalator", " ▲ alpha             0 │", "▸· beta              2 │"} {
		if !strings.Contains(out, want) {
			t.Errorf("sidebar lacks %q:\n%s", want, out)
		}
	}
	if m.w != 120-sideDefault {
		t.Fatalf("the dashboard is %d wide", m.w)
	}

	// A click on alpha opens its coordinator, even under a popup.
	press(m, "?")
	cmd := m.sideClick(tea.Mouse{X: 3, Y: 1, Button: tea.MouseLeft})
	run(m, cmd)
	if len(src.opened) != 1 || src.opened[0] != "alpha" || m.top() != nil || m.result.Attach != "s-alpha" {
		t.Fatalf("click opened %v, attach %q", src.opened, m.result.Attach)
	}

	// Dragging the border resizes and saves; } widens; b slims.
	m.Update(tea.MouseClickMsg{X: sideDefault - 1, Y: 5, Button: tea.MouseLeft})
	m.Update(tea.MouseMotionMsg{X: 29, Y: 5, Button: tea.MouseLeft})
	m.Update(tea.MouseReleaseMsg{X: 29, Y: 5, Button: tea.MouseLeft})
	if m.sideW() != 30 || m.w != 90 || LoadLayout(ui).Sidebar.Width != 30 {
		t.Fatalf("drag: sidebar %d, dashboard %d, ui.json %+v", m.sideW(), m.w, LoadLayout(ui))
	}
	press(m, "}")
	if LoadLayout(ui).Sidebar.Width != 32 {
		t.Fatalf("}: ui.json %+v", LoadLayout(ui))
	}
	press(m, "b")
	if m.sideW() != sideSlim || !LoadLayout(ui).Sidebar.Slim {
		t.Fatalf("b: sidebar %d, ui.json %+v", m.sideW(), LoadLayout(ui))
	}
	press(m, "b")

	// A narrow window: the slim strip, never nothing. alpha is current
	// since the click.
	m.Update(tea.WindowSizeMsg{Width: 70, Height: 30})
	if out := whole(m); m.sideW() != sideSlim || !strings.Contains(out, "▸▲alph│") || !strings.Contains(out, " ·beta│") {
		t.Fatalf("narrow window, sidebar %d:\n%s", m.sideW(), out)
	}
}

// TestDashboardThenOpen: a project clicked in a session's sidebar opens
// once the dashboard's first poll is in.
func TestDashboardThenOpen(t *testing.T) {
	src := &fakeSource{data: testData()}
	m := newDash(DashOptions{Source: src, Width: 120, Height: 30, State: DashState{Then: ThenOpen + "beta"}})
	msg := m.setData(src.data)()
	for _, c := range msg.(tea.BatchMsg) {
		if c == nil {
			continue
		}
		if am, ok := c().(actionMsg); ok {
			m.Update(am)
		}
	}
	if len(src.opened) != 1 || src.opened[0] != "beta" {
		t.Fatalf("opened %v", src.opened)
	}
}
