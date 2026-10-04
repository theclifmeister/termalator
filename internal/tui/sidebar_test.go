package tui

import (
	"github.com/charmbracelet/x/ansi"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

// TestDashboardSidebar: the project tree. Every project with its
// coordinator's glyph, a hint for a blocked or waiting thread and its
// open threads; the current one expanded with its coordinator and
// threads. ▸ ▾ open and close projects, a project row shows its
// dashboard, the coordinator row opens the coordinator, a thread row
// watches it; the border drags and is saved, and a narrow window gets
// the slim strip.
func TestDashboardSidebar(t *testing.T) {
	src := &fakeSource{data: testData()}
	ui := filepath.Join(t.TempDir(), "ui.json")
	m := newDash(DashOptions{Source: src, Width: 120, Height: 30, UIFile: ui, State: DashState{Current: "beta"}})
	m.setData(src.data)
	want := []string{
		" PROJECTS 2            │ tm dashboard",
		"▸▲ alpha             0 │",
		"▾· beta            ◆ 2 │", // t-0005 waits on a question
		"  · coordinator        │",
		"  ● Write docs      60%│",
		"  · Old work           │",
	}
	lines := strings.Split(whole(m), "\n")
	for i, w := range want {
		if !strings.HasPrefix(lines[i], w) {
			t.Errorf("row %d is %q, want %q…", i, lines[i], w)
		}
	}
	if here := hereRow(m.tree()); here.kind != treeProject || here.slug != "beta" {
		t.Errorf("on the dashboard the current project's row is highlighted, not %+v", here)
	}
	if m.w != 120-sideDefault {
		t.Fatalf("the dashboard is %d wide", m.w)
	}

	// ▸ opens alpha, ▾ closes it again; the current project stays open.
	m.Update(tea.MouseClickMsg{X: 0, Y: 1, Button: tea.MouseLeft})
	if l := strings.Split(whole(m), "\n")[2]; !strings.HasPrefix(l, "  ▲ coordinator") {
		t.Fatalf("alpha didn't open:\n%s", whole(m))
	}
	m.Update(tea.MouseClickMsg{X: 0, Y: 1, Button: tea.MouseLeft})
	m.Update(tea.MouseClickMsg{X: 1, Y: 2, Button: tea.MouseLeft})
	if len(m.expanded) != 0 || !strings.Contains(m.msg, "stays open") || !strings.HasPrefix(strings.Split(whole(m), "\n")[3], "  · coordinator") {
		t.Fatalf("expanded %v, msg %q:\n%s", m.expanded, m.msg, whole(m))
	}

	// A thread row watches its session, even from under a popup; one
	// without a session says why.
	press(m, "?")
	run(m, m.sideClick(tea.Mouse{X: 5, Y: 4, Button: tea.MouseLeft}))
	if m.top() != nil || m.result.Attach != "s-5" {
		t.Fatalf("thread click: attach %q", m.result.Attach)
	}
	m.result = DashResult{}
	m.Update(tea.MouseClickMsg{X: 5, Y: 5, Button: tea.MouseLeft})
	if m.result.Attach != "" || !strings.Contains(m.msg, "t-0006 has no running session") {
		t.Fatalf("thread without a session: attach %q msg %q", m.result.Attach, m.msg)
	}

	// A project row shows its dashboard: alpha becomes current, listed and
	// expanded, nothing is attached.
	m.Update(tea.MouseClickMsg{X: 5, Y: 1, Button: tea.MouseLeft})
	if m.current != "alpha" || m.sel != "p:alpha" || len(src.opened) != 0 || m.result.Attach != "" ||
		!strings.Contains(screen(m), " alpha ─") {
		t.Fatalf("project click: current %q sel %q opened %v:\n%s", m.current, m.sel, src.opened, whole(m))
	}
	// Its coordinator row opens the coordinator.
	run(m, m.sideClick(tea.Mouse{X: 5, Y: 2, Button: tea.MouseLeft}))
	if len(src.opened) != 1 || src.opened[0] != "alpha" || m.result.Attach != "s-alpha" {
		t.Fatalf("coordinator click opened %v, attach %q", src.opened, m.result.Attach)
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

	// A narrow window: the slim strip of projects, never nothing. alpha
	// is current; beta's glyph is the hint.
	m.Update(tea.WindowSizeMsg{Width: 70, Height: 30})
	if out := whole(m); m.sideW() != sideSlim || !strings.Contains(out, "▸▲alph│") || !strings.Contains(out, " ◆beta│") || !strings.HasPrefix(strings.Split(out, "\n")[3], "      │") {
		t.Fatalf("narrow window, sidebar %d:\n%s", m.sideW(), out)
	}
	// A click on a project in the strip shows its dashboard.
	m.Update(tea.MouseClickMsg{X: 1, Y: 2, Button: tea.MouseLeft})
	if m.current != "beta" {
		t.Fatalf("slim click: current %q", m.current)
	}
}

// TestTreeHere: attached, the focused session's row is the one you are
// on: a thread's or a coordinator's; a plain shell leaves it on the
// current project.
func TestTreeHere(t *testing.T) {
	d := testData()
	for focus, want := range map[string]string{"s-5": "t-0005", "s-1": "coordinator", "s-3": "beta"} {
		rows := buildTree(d.Projects, d.Sessions, treeIn{current: "beta", focus: focus,
			expanded: func(slug string) bool { return slug == "alpha" }})
		h := hereRow(rows)
		got := map[int]string{treeProject: h.slug, treeCoordinator: "coordinator", treeThread: h.thread}[h.kind]
		if got != want {
			t.Errorf("focus %s: on %q, want %q", focus, got, want)
		}
	}
}

func hereRow(rows []treeRow) treeRow {
	for _, r := range rows {
		if r.here {
			return r
		}
	}
	return treeRow{}
}

// TestTreeThreadRows: every row is the same width with the percent in a
// column of its own, so titles are cut alike whatever the percent, and never leave a space before the "…".
func TestTreeThreadRows(t *testing.T) {
	for _, r := range []treeRow{
		{kind: treeThread, thread: "t-0003", title: "Key the CI cache on the Zig version", state: "blocked", pct: 0},
		{kind: treeThread, thread: "t-0002", title: "Rewrite the README", state: "done", pct: 100},
		{kind: treeThread, thread: "t-0001", title: "Fix the login session expiry", state: "working", pct: -1},
	} {
		l := ansi.Strip(treeLine(r, 23, false))
		if strings.Contains(l, " …") || strings.Contains(l, "t-000") {
			t.Errorf("row %q", l)
		}
		if w := ansi.StringWidth(l); w != 23 {
			t.Errorf("row %q is %d cells", l, w)
		}
		// The title column is the same on every row: 4 cells in, 14 wide,
		// then the percent's 5.
		if title := []rune(l)[4:18]; strings.TrimSpace(string(title)) == "" {
			t.Errorf("row %q: no title", l)
		}
	}
}
