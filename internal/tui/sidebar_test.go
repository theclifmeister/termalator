package tui

import (
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

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
		l := ansi.Strip(treeLine(r, 23, false, false))
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

// TestDashboardSidebarKeys: tab gives the sidebar the keyboard; ↑ ↓ move
// its cursor, → ← open and close projects (← on a row under a project
// goes up to it), enter does what a click does and esc goes back to the
// list. Keys that aren't the sidebar's still run the list's actions.
func TestDashboardSidebarKeys(t *testing.T) {
	src := &fakeSource{data: testData()}
	m := newDash(DashOptions{Source: src, Width: 120, Height: 30, State: DashState{Current: "beta"}})
	m.layout.Details = false
	m.setData(src.data)
	cursor := func() string {
		for _, r := range m.tree() {
			if r.cursor {
				return r.key()
			}
		}
		return ""
	}
	if cursor() != "" {
		t.Fatal("a cursor without the focus")
	}
	keyPress(m, "tab")
	if m.focus != focusSide || cursor() != "p:beta" || !strings.Contains(whole(m), "sidebar: ↑ ↓ move") {
		t.Fatalf("tab: focus %d cursor %q:\n%s", m.focus, cursor(), whole(m))
	}
	steps := []struct{ key, cursor string }{
		{"up", "p:alpha"}, {"up", "p:alpha"}, {"right", "p:alpha"}, {"right", "c:alpha"},
		{"left", "p:alpha"}, {"left", "p:alpha"}, {"j", "p:beta"}, {"down", "c:beta"}, {"down", "t:beta/t-0005"},
	}
	for i, s := range steps {
		keyPress(m, s.key)
		if got := cursor(); got != s.cursor {
			t.Fatalf("step %d (%s): cursor %q, want %q", i, s.key, got, s.cursor)
		}
		if i == 2 && !slices.Contains(m.expanded, "alpha") {
			t.Fatalf("→ didn't open alpha: %v", m.expanded)
		}
		if i == 5 && len(m.expanded) != 0 {
			t.Fatalf("← didn't close alpha: %v", m.expanded)
		}
	}
	// The cursor's row is highlighted, not the one you are on.
	if l := strings.Split(whole(m), "\n")[4]; !strings.Contains(l, "Write docs") {
		t.Fatalf("row 4 %q", l)
	}
	// The current project stays open.
	keyPress(m, "left")
	keyPress(m, "left")
	if !strings.Contains(m.msg, "stays open") {
		t.Fatalf("← on the current project: %q", m.msg)
	}
	// Not a sidebar key: the list's ? opens the help.
	keyPress(m, "?")
	if _, ok := m.top().(*helpView); !ok {
		t.Fatal("? with the sidebar focused")
	}
	keyPress(m, "esc")
	if m.focus != focusSide {
		t.Fatal("esc closing the help left the sidebar")
	}
	// enter on a thread watches it.
	keyPress(m, "down")
	keyPress(m, "down")
	run(m, keyPress(m, "enter"))
	if m.result.Attach != "s-5" {
		t.Fatalf("enter on t-0005: attach %q", m.result.Attach)
	}
	// esc goes back to the list; shift+tab from the list to the sidebar.
	m.result = DashResult{}
	keyPress(m, "esc")
	if m.focus != focusList || cursor() != "" {
		t.Fatalf("esc: focus %d", m.focus)
	}
	keyPress(m, "shift+tab")
	if m.focus != focusSide {
		t.Fatalf("shift+tab: focus %d", m.focus)
	}
}

// TestDashboardFocusCycle: tab goes list → details → sidebar → list when
// the details panel shows, and the arrows scroll the panel while it has
// the keyboard.
func TestDashboardFocusCycle(t *testing.T) {
	src := &fakeSource{data: testData()}
	m := newDash(DashOptions{Source: src, Width: 140 + sideDefault, Height: 12, State: DashState{Current: "beta"}})
	m.setData(src.data)
	m.sel = "th:beta:t-0005"
	for _, want := range []int{focusDetails, focusSide, focusList} {
		keyPress(m, "tab")
		if m.focus != want {
			t.Fatalf("tab: focus %d, want %d", m.focus, want)
		}
	}
	keyPress(m, "shift+tab")
	keyPress(m, "shift+tab")
	if m.focus != focusDetails {
		t.Fatalf("shift+tab twice: focus %d", m.focus)
	}
	keyPress(m, "down")
	screen(m)
	if m.detailTop != 1 || m.sel != "th:beta:t-0005" {
		t.Fatalf("↓ in the details: top %d sel %q", m.detailTop, m.sel)
	}
	keyPress(m, "esc")
	if m.focus != focusList {
		t.Fatalf("esc: focus %d", m.focus)
	}
}

// TestEverySidebarClickHasKey: the reverse of TestEveryKeyHasMousePath
// for the sidebar. Each click on it (▸ ▾, every kind of row) has a key
// path doing the same, and every item of a sidebar row's menu names one.
func TestEverySidebarClickHasKey(t *testing.T) {
	type outcome struct {
		current, sel, attach, msg string
		opened, expanded          []string
	}
	fresh := func() (*dash, *fakeSource) {
		src := &fakeSource{data: testData()}
		m := newDash(DashOptions{Source: src, Width: 120, Height: 30, State: DashState{Current: "beta"}})
		m.setData(src.data)
		return m, src
	}
	of := func(m *dash, src *fakeSource) outcome {
		return outcome{m.current, m.sel, m.result.Attach, m.msg, slices.Clone(src.opened), slices.Clone(m.expanded)}
	}
	rows := (func() []treeRow { m, _ := fresh(); return m.tree() })()
	for i, r := range rows {
		clicks := map[string]int{"row": 5}
		if r.kind == treeProject {
			clicks["toggle"] = 0
		}
		for what, x := range clicks {
			m, src := fresh()
			run(m, m.sideClick(tea.Mouse{X: x, Y: i + 1, Button: tea.MouseLeft}))
			byMouse := of(m, src)

			k, ksrc := fresh()
			keyPress(k, "tab")
			k.sideSel = r.key()
			key := "enter"
			if what == "toggle" {
				key = map[bool]string{true: "left", false: "right"}[r.open]
			}
			run(k, keyPress(k, key))
			byKey := of(k, ksrc)
			byKey.sel, byMouse.sel = "", "" // the cursor isn't the list's selection
			if fmt.Sprint(byKey) != fmt.Sprint(byMouse) {
				t.Errorf("%s %s: mouse %+v, keys (%s) %+v", r.key(), what, byMouse, key, byKey)
			}
		}
	}
	// Every sidebar menu item has a key path.
	keyPaths := map[string]string{
		"show its dashboard": "enter on the project", "open its coordinator": "enter on its coordinator",
		"open in the tree": "→", "close in the tree": "←",
		"project popup": "enter, then a", "tasks": "enter, then t", "inbox": "enter, then i",
		"open the coordinator": "enter", "watch": "enter", "take over…": "enter, then prefix+u",
	}
	m, _ := fresh()
	m.expand("alpha", true)
	for i := range m.tree() {
		m.stack = nil
		m.sideMenu(tea.Mouse{X: 5, Y: i + 1, Button: tea.MouseRight})
		mv, ok := m.top().(*menuView)
		if !ok {
			continue
		}
		for _, it := range mv.items {
			if keyPaths[it.label] == "" {
				t.Errorf("sidebar menu item %q has no key path", it.label)
			}
		}
	}
}
