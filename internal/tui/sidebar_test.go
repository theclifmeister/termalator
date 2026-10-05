package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	uv "github.com/charmbracelet/ultraviolet"

	tea "charm.land/bubbletea/v2"

	"github.com/theclifmeister/termilator/internal/view"
)

// TestDashboardSidebar: the project tree. Every project, always
// expanded, with its open threads and a hint for a blocked or waiting
// thread; under it, on tree connectors, its coordinator and threads with
// their progress and state glyphs in columns. A project row shows its
// dashboard, the coordinator row opens the coordinator, a thread row
// attaches it; the border drags and is saved, and a narrow window gets
// the slim strip.
func TestDashboardSidebar(t *testing.T) {
	src := &fakeSource{data: testData()}
	ui := filepath.Join(t.TempDir(), "ui.json")
	m := newDash(DashOptions{Source: src, Width: 120, Height: 30, UIFile: ui, State: DashState{Current: "beta"}})
	m.setData(src.data)
	want := []string{
		" PROJECTS                    2 │ tm dashboard",
		" ■ alpha                   0 ◆ │", // a task needs you; a blank column before the border
		" └─ coordinator              ▲ │",
		" ■ beta                    2 ◆ │", // t-0005 waits on a question
		" └─ coordinator              · │",
		"    ├─ t-0005 Write do…  60% ● │", // threads hang under the coordinator, id and title
		"    └─ t-0006 Old work       · │",
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

	// A thread row attaches its session, even from under a popup; one
	// without a session says why.
	press(m, "?")
	run(m, m.sideClick(tea.Mouse{X: 5, Y: 5, Button: tea.MouseLeft}))
	if m.top() != nil || m.result.Attach != "s-5" {
		t.Fatalf("thread click: attach %q", m.result.Attach)
	}
	m.result = DashResult{}
	m.Update(tea.MouseClickMsg{X: 5, Y: 6, Button: tea.MouseLeft})
	if m.result.Attach != "" || !strings.Contains(m.msg, "t-0006 has no running session") {
		t.Fatalf("thread without a session: attach %q msg %q", m.result.Attach, m.msg)
	}

	// A project row shows its dashboard: alpha becomes current and
	// listed, nothing is attached.
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
	if out := whole(m); m.sideW() != sideSlim || !strings.Contains(out, "▸▲alp │") || !strings.Contains(out, " ◆bet │") || !strings.HasPrefix(strings.Split(out, "\n")[3], "      │") {
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
		rows := buildTree(d.Projects, d.Sessions, treeIn{current: "beta", focus: focus})
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
// column of its own, so titles are cut alike whatever the percent, and
// never leave a space before the "…". Each row leads with the thread's
// id, which is never cut: the title gives way.
func TestTreeThreadRows(t *testing.T) {
	for _, r := range []treeRow{
		{kind: treeThread, thread: "t-0003", title: "Key the CI cache on the Zig version", state: "blocked", pct: 0},
		{kind: treeThread, thread: "t-0002", title: "Rewrite the README", state: "done", pct: 100},
		{kind: treeThread, thread: "t-0001", title: "Fix the login session expiry", state: "working", pct: -1},
	} {
		l := ansi.Strip(treeLine(r, 24, false, false))
		if strings.Contains(l, " …") {
			t.Errorf("row %q", l)
		}
		if w := ansi.StringWidth(l); w != 24 {
			t.Errorf("row %q is %d cells", l, w)
		}
		// The label column is the same on every row: 7 cells in (a level
		// under the coordinator), 9 wide, the id, a space and what fits
		// of the title; then the percent's 5, the state glyph and the
		// blank column before the border.
		label := string([]rune(l)[7:16])
		if !strings.HasPrefix(label, r.thread+" ") || strings.TrimSpace(label[len(r.thread):]) == "" {
			t.Errorf("row %q: label %q", l, label)
		}
	}
}

// sideWidest is the widest sidebar the width sweeps draw: there is no
// maximum, so well past the old 48.
const sideWidest = 160

// TestTreeThreadIDNeverCut: at every sidebar width and in every icon set
// a thread row is as wide as the sidebar, ends in its state glyph and a
// blank column, and shows its whole id or none of it: none only at the
// narrowest widths, where it doesn't fit beside the state glyph. From
// 25 columns the title shows too, and a wide sidebar (there is no
// maximum width) shows a long one whole.
func TestTreeThreadIDNeverCut(t *testing.T) {
	defer setIcons(IconsUnicode)
	const title = "Prefix each thread row with its id, and keep the title whole when the sidebar is wide"
	r := treeRow{kind: treeThread, thread: "t-0042", title: title, state: "working", pct: 40}
	for _, set := range IconChoices[1:] {
		setIcons(set)
		for w := view.SideMin; w <= sideWidest; w++ {
			l := ansi.Strip(treeLine(r, w-1, false, false))
			if got := ansi.StringWidth(l); got != w-1 {
				t.Errorf("%s width %d: row %q is %d cells", set, w, l, got)
			}
			if r := []rune(l); r[len(r)-1] != ' ' || r[len(r)-2] == ' ' {
				t.Errorf("%s width %d: row %q: no glyph and gap at its end", set, w, l)
			}
			whole := strings.Contains(l, "t-0042")
			if !whole && (w >= view.SideMin+2 || strings.Contains(l, "t-")) {
				t.Errorf("%s width %d: row %q lacks the whole id", set, w, l)
			}
			if w >= 25 && !strings.Contains(l, "t-0042 P") {
				t.Errorf("%s width %d: row %q lacks the title", set, w, l)
			}
			if w >= 120 && (!strings.Contains(l, title) || strings.Contains(l, "…")) {
				t.Errorf("%s width %d: row %q cuts the title", set, w, l)
			}
		}
	}
	if got := threadLabel("t-0042", "Title", 4); got != "    " {
		t.Errorf("an id wider than its room: %q", got)
	}
}

// TestDashboardSidebarKeys: tab gives the sidebar the keyboard; ↑ ↓ move
// its cursor, → goes into a project and ← on a row under a project up
// to it, enter does what a click does and esc goes back to the
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
		{"up", "c:alpha"}, {"up", "p:alpha"}, {"up", "p:alpha"}, {"right", "c:alpha"}, {"right", "c:alpha"},
		{"left", "p:alpha"}, {"left", "p:alpha"}, {"j", "c:alpha"}, {"down", "p:beta"}, {"down", "c:beta"}, {"down", "t:beta/t-0005"},
	}
	for i, s := range steps {
		keyPress(m, s.key)
		if got := cursor(); got != s.cursor {
			t.Fatalf("step %d (%s): cursor %q, want %q", i, s.key, got, s.cursor)
		}
	}
	// The cursor's row is highlighted, not the one you are on.
	if l := strings.Split(whole(m), "\n")[5]; !strings.Contains(l, "t-0005 ") {
		t.Fatalf("row 5 %q", l)
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
	// enter on a thread attaches it.
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
// for the sidebar. Each click on it (every kind of row) has a key
// path doing the same, and every item of a sidebar row's menu names one.
func TestEverySidebarClickHasKey(t *testing.T) {
	type outcome struct {
		current, sel, attach, msg string
		opened                    []string
	}
	fresh := func() (*dash, *fakeSource) {
		src := &fakeSource{data: testData()}
		m := newDash(DashOptions{Source: src, Width: 120, Height: 30, State: DashState{Current: "beta"}})
		m.setData(src.data)
		return m, src
	}
	of := func(m *dash, src *fakeSource) outcome {
		return outcome{m.current, m.sel, m.result.Attach, m.msg, slices.Clone(src.opened)}
	}
	rows := (func() []treeRow { m, _ := fresh(); return m.tree() })()
	for i, r := range rows {
		for what, x := range map[string]int{"row": 5, "first column": 0} {
			m, src := fresh()
			run(m, m.sideClick(tea.Mouse{X: x, Y: i + 1, Button: tea.MouseLeft}))
			byMouse := of(m, src)

			k, ksrc := fresh()
			keyPress(k, "tab")
			k.sideSel = r.key()
			key := "enter"
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
		"project popup": "enter, then a", "tasks": "enter, then t", "inbox": "enter, then i",
		"open the coordinator": "enter", "attach": "enter",
	}
	m, _ := fresh()
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

// TestSidebarDefaultKeepsSavedWidth: the default width is 32, room for a
// thread's id and a few words of its title; a width ui.json already
// keeps stays, and one without a width starts at the default.
func TestSidebarDefaultKeepsSavedWidth(t *testing.T) {
	if sideDefault != 32 {
		t.Fatalf("default width %d", sideDefault)
	}
	dir := t.TempDir()
	for name, c := range map[string]struct {
		json string
		want int
	}{
		"saved":    {`{"sidebar":{"width":24}}`, 24},
		"no width": {`{"details":true}`, sideDefault},
		"zero":     {`{"sidebar":{"width":0}}`, sideDefault},
	} {
		path := filepath.Join(dir, name+".json")
		if err := os.WriteFile(path, []byte(c.json), 0o600); err != nil {
			t.Fatal(err)
		}
		if got := LoadLayout(path).Sidebar.Width; got != c.want {
			t.Errorf("%s: width %d, want %d", name, got, c.want)
		}
	}
	if got := LoadLayout(filepath.Join(dir, "none.json")).Sidebar.Width; got != sideDefault {
		t.Errorf("no ui.json: width %d", got)
	}
}

// TestTreeHighlightRunsToBorder: in every icon set, on every kind of
// row and in both widths, the highlighted row is in reverse video up to
// the border, through the blank column before it, so a Nerd Font icon
// in the last column (the bell) that draws wider than its cell shows
// whole (T16); other rows leave both last cells plain. The blank column
// stays blank either way, and the glyph sits right before it.
func TestTreeHighlightRunsToBorder(t *testing.T) {
	defer setIcons(IconsUnicode)
	rows := []treeRow{
		{kind: treeProject, slug: "termilator", state: "idle", hint: true, threads: 1, current: true},
		{kind: treeProject, slug: "todo", state: "", threads: 0},
		{kind: treeCoordinator, slug: "termilator", state: "idle", last: true},
		{kind: treeThread, slug: "termilator", thread: "t-0008", title: "Small follow-ups", state: "working", pct: 65, last: true},
	}
	reverse := func(line string, w int) []bool {
		buf := uv.NewScreenBuffer(w, 1)
		uv.NewStyledString(line).Draw(buf, uv.Rect(0, 0, w, 1))
		out := make([]bool, w)
		for x := range w {
			if c := buf.CellAt(x, 0); c != nil {
				out[x] = c.Style.Attrs&uv.AttrReverse != 0
			}
		}
		return out
	}
	for _, set := range IconChoices[1:] {
		setIcons(set)
		for _, w := range []int{sideSlim, view.SideMin, sideDefault, 48, 80, sideWidest} {
			cw := w - 1
			slim := w <= sideSlim
			for _, r := range rows {
				if slim && r.kind != treeProject {
					continue
				}
				for _, mode := range []string{"plain", "here", "cursor"} {
					r := r
					r.here, r.cursor = mode == "here", mode == "cursor"
					focused := mode == "cursor"
					l := treeLine(r, cw, slim, focused)
					plain := []rune(ansi.Strip(l))
					name := fmt.Sprintf("%s width %d %s row %q (%s)", set, w, mode, string(plain), r.slug+r.thread)
					if got := ansi.StringWidth(string(plain)); got != cw {
						t.Errorf("%s: %d cells", name, got)
						continue
					}
					if plain[len(plain)-1] != ' ' {
						t.Errorf("%s: the last column isn't blank", name)
					}
					rev := reverse(l, cw)
					hl := mode != "plain"
					if rev[cw-1] != hl || rev[cw-2] != hl {
						t.Errorf("%s: last two cells reverse %v %v, want %v", name, rev[cw-2], rev[cw-1], hl)
					}
				}
			}
		}
	}
	// The bell is the project row's last glyph, right before the blank.
	setIcons(IconsNerd)
	l := []rune(ansi.Strip(treeLine(treeRow{kind: treeProject, slug: "termilator", hint: true, threads: 1, here: true}, sideDefault-1, false, false)))
	if string(l[len(l)-2:]) != nerdIcons.hint+" " {
		t.Errorf("project row %q doesn't end in the bell and the blank", string(l))
	}
}

// TestSidebarWiderThanWindow: the sidebar has no maximum width. A wide
// one shows a long title whole; in a window too narrow for it, it is cut
// to leave the panes their room, and ui.json keeps the saved width, so
// it comes back in a wider window. Keys and drags work on the width
// shown.
func TestSidebarWiderThanWindow(t *testing.T) {
	src := &fakeSource{data: testData()}
	ui := filepath.Join(t.TempDir(), "ui.json")
	if err := os.WriteFile(ui, []byte(`{"sidebar":{"width":90}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	m := newDash(DashOptions{Source: src, Width: 200, Height: 30, UIFile: ui, State: DashState{Current: "beta"}})
	m.setData(src.data)
	if m.sideW() != 90 || m.w != 110 || !strings.Contains(whole(m), "t-0005 Write docs") {
		t.Fatalf("wide window: sidebar %d, dashboard %d:\n%s", m.sideW(), m.w, whole(m))
	}
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	if m.sideW() != 120-sideRoom || m.w != sideRoom || LoadLayout(ui).Sidebar.Width != 90 {
		t.Fatalf("narrow window: sidebar %d, dashboard %d, ui.json %+v", m.sideW(), m.w, LoadLayout(ui))
	}
	m.Update(tea.WindowSizeMsg{Width: 200, Height: 30})
	if m.sideW() != 90 {
		t.Fatalf("wider again: sidebar %d", m.sideW())
	}

	// Dragging past the old 48 columns, then a narrower window and }: the
	// saved width stays.
	m.Update(tea.MouseClickMsg{X: 89, Y: 5, Button: tea.MouseLeft})
	m.Update(tea.MouseMotionMsg{X: 69, Y: 5, Button: tea.MouseLeft})
	m.Update(tea.MouseReleaseMsg{X: 69, Y: 5, Button: tea.MouseLeft})
	if m.sideW() != 70 || LoadLayout(ui).Sidebar.Width != 70 {
		t.Fatalf("drag: sidebar %d, ui.json %+v", m.sideW(), LoadLayout(ui))
	}
	m.Update(tea.WindowSizeMsg{Width: 110, Height: 30})
	press(m, "}")
	if m.sideW() != 110-sideRoom || LoadLayout(ui).Sidebar.Width != 70 {
		t.Fatalf("} at the window's limit: sidebar %d, ui.json %+v", m.sideW(), LoadLayout(ui))
	}
	press(m, "{")
	if m.sideW() != 110-sideRoom-2 || LoadLayout(ui).Sidebar.Width != 110-sideRoom-2 {
		t.Fatalf("{ from the width shown: sidebar %d, ui.json %+v", m.sideW(), LoadLayout(ui))
	}
}
