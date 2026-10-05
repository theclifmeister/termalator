package tui

import (
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"

	"github.com/theclifmeister/termalator/internal/emu"
	"github.com/theclifmeister/termalator/internal/proto"
)

// at is where text first shows right of the sidebar, as a window cell;
// the test fails when it doesn't show.
func at(t *testing.T, m *dash, text string) (int, int) {
	t.Helper()
	return atNth(t, m, text, 0)
}

// atNth is where text shows the nth time (from 0) right of the sidebar.
func atNth(t *testing.T, m *dash, text string, n int) (int, int) {
	t.Helper()
	for y, l := range strings.Split(screen(m), "\n") {
		if i := strings.Index(l, text); i >= 0 {
			if n > 0 {
				n--
				continue
			}
			return m.sideW() + ansi.StringWidth(l[:i]), y
		}
	}
	t.Fatalf("%q isn't on the screen:\n%s", text, whole(m))
	return 0, 0
}

// atSide is where text first shows in the sidebar.
func atSide(t *testing.T, m *dash, text string) (int, int) {
	t.Helper()
	for y, l := range strings.Split(whole(m), "\n") {
		l = string([]rune(l)[:m.sideW()])
		if i := strings.Index(l, text); i >= 0 {
			return ansi.StringWidth(l[:i]), y
		}
	}
	t.Fatalf("%q isn't in the sidebar:\n%s", text, whole(m))
	return 0, 0
}

// mouseAt sends a press of button at window cell (x, y), after drawing
// the window as the program would, and runs what it starts.
func mouseAt(m *dash, button tea.MouseButton, x, y int) {
	m.render()
	var msg tea.Msg = tea.MouseClickMsg{X: x, Y: y, Button: button}
	if button == tea.MouseWheelUp || button == tea.MouseWheelDown {
		msg = tea.MouseWheelMsg{X: x, Y: y, Button: button}
	}
	_, cmd := m.Update(msg)
	run(m, cmd)
}

// clickItem clicks the open menu's item labelled label.
func clickItem(t *testing.T, m *dash, label string) {
	t.Helper()
	mv, ok := m.top().(*menuView)
	if !ok {
		t.Fatalf("no menu open: %T", m.top())
	}
	i := slices.IndexFunc(mv.items, func(it menuItem) bool { return it.label == label })
	if i < 0 {
		t.Fatalf("the menu lacks %q", label)
	}
	m.render()
	g := m.geo
	mouseAt(m, tea.MouseLeft, m.sideW()+g.x+2, 1+g.y+1+i-g.top)
}

// clickOn clicks the first place text shows.
func clickOn(t *testing.T, m *dash, text string) {
	t.Helper()
	x, y := at(t, m, text)
	mouseAt(m, tea.MouseLeft, x, y)
}

// TestEveryKeyHasMousePath: every dashboard action is in the ≡ menu,
// whose items press the very key, or names its mouse path; and every
// prefix command in a session is in the session's ≡ menu, which runs it
// as the key does, or names its mouse path. So the mouse can't fall
// behind the keys.
func TestEveryKeyHasMousePath(t *testing.T) {
	_, m := popupData(t)
	items := m.dashItems()
	for _, a := range actions {
		if a.mouse != "" {
			continue
		}
		if !slices.ContainsFunc(a.menu, func(s string) bool { return s != "" }) {
			t.Errorf("action %v has no mouse path: no menu entry, no mouse note", a.keys)
		}
		for i, word := range a.menu {
			if word == "" {
				continue
			}
			key := a.keys[i]
			if !slices.ContainsFunc(items, func(it menuItem) bool { return it.label == word && it.key == key }) {
				t.Errorf("the ≡ menu lacks %q (%s)", word, key)
			}
			// The item presses exactly the key.
			if got := keyMsg(key).String(); got != key {
				t.Errorf("the menu's %s presses %q", key, got)
			}
		}
	}
	session := map[string]bool{"d": true, "u": true, "r": true}
	for k := range prefixCommands {
		session[k] = true
	}
	for k := range paneCommands {
		session[k] = true
	}
	var prefix chord
	prefix, _ = parseChord(DefaultPrefixKey)
	for k := range session {
		if sessionMouse[k] != "" {
			continue
		}
		if !slices.ContainsFunc(sessionMenu, func(e struct{ label, key string }) bool { return e.key == k }) {
			t.Errorf("the session's ≡ menu lacks prefix+%s", k)
			continue
		}
		if keyName(cmdKey(k)) != k || prefixStep(prefix, true, cmdKey(k), true) == (prefixDo{}) {
			t.Errorf("the menu's prefix+%s does nothing", k)
		}
	}
	if !slices.ContainsFunc(sessionMenu, func(e struct{ label, key string }) bool { return e.key == "prefix" }) {
		t.Error("the session's ≡ menu can't send the prefix key")
	}
}

// TestNumberSettingButtons: every setting changed with + and - (the
// project popup's numbers) has − and + buttons after its value, and a
// click on them does what - and + do.
func TestNumberSettingButtons(t *testing.T) {
	src, m := popupData(t)
	m.Update(tea.WindowSizeMsg{Width: 160, Height: 60})
	keyPress(m, "a")
	keyPress(m, "4")
	pv := m.top().(*projectView)
	n := 0
	for _, r := range pv.settings.rows {
		if r.adjust == nil {
			continue
		}
		n++
		var got []string
		for _, b := range []string{"+", "−"} {
			_, y := at(t, m, r.label)
			line := strings.Split(screen(m), "\n")[y]
			i := strings.Index(line, r.label)
			j := strings.Index(line[i:], b)
			if j < 0 {
				t.Fatalf("%s lacks %s: %q", r.label, b, line)
			}
			before := len(src.settings)
			mouseAt(m, tea.MouseLeft, m.sideW()+ansi.StringWidth(line[:i+j]), y)
			if len(src.settings) != before+1 {
				t.Fatalf("a click on %s's %s changed nothing (%v)", r.label, b, src.settings)
			}
			got = append(got, src.settings[len(src.settings)-1])
			m.setData(src.Load())
		}
		// + then −: back to where it was, one up first.
		_, up, _ := strings.Cut(got[0], "=")
		_, down, _ := strings.Cut(got[1], "=")
		if up == down || len(up) == 0 {
			t.Errorf("%s: + gave %s, − gave %s", r.label, got[0], got[1])
		}
	}
	if n == 0 {
		t.Fatal("no setting with + and -")
	}
}

// TestDashboardClicks: a click selects a row and a double-click opens
// it; the wheel moves the selection; the footer's hints are buttons.
func TestDashboardClicks(t *testing.T) {
	src, m := popupData(t)
	clickOn(t, m, "s-4")
	if m.sel != "n:s-4" {
		t.Fatalf("click selected %q:\n%s", m.sel, screen(m))
	}
	coordX, coordY := atNth(t, m, "coordinator", 1) // the project's own row, after NEEDS YOU
	mouseAt(m, tea.MouseLeft, coordX, coordY)
	if m.sel != "p:alpha" {
		t.Fatalf("click on the coordinator selected %q", m.sel)
	}
	mouseAt(m, tea.MouseLeft, coordX, coordY) // the second click of a double-click
	if !m.busy && len(src.opened) == 0 && m.result.Attach == "" {
		t.Fatalf("double-click didn't open the coordinator: opened %v result %+v", src.opened, m.result)
	}
	m.busy = false
	m.result = DashResult{}
	m.Update(tea.WindowSizeMsg{Width: 150, Height: 40}) // the whole footer shows

	// The wheel moves the selection.
	before := m.sel
	mouseAt(m, tea.MouseWheelDown, coordX, coordY)
	if m.sel == before {
		t.Fatal("the wheel didn't move the selection")
	}

	// Footer buttons: ? help, which has no ×, and a click outside
	// closes it; , settings, and a click outside closes it.
	clickOn(t, m, "? help")
	if _, ok := m.top().(*helpView); !ok {
		t.Fatalf("? help didn't open the help: %T", m.top())
	}
	if strings.Contains(screen(m), "×") {
		t.Fatalf("the help has a ×:\n%s", screen(m))
	}
	mouseAt(m, tea.MouseLeft, m.sideW(), 2)
	if m.top() != nil {
		t.Fatalf("a click outside didn't close the help: %T", m.top())
	}
	clickOn(t, m, ", settings")
	if _, ok := m.top().(*settingsView); !ok {
		t.Fatalf(", settings didn't open: %T", m.top())
	}
	mouseAt(m, tea.MouseLeft, m.sideW()+1, 2)
	if m.top() != nil {
		t.Fatalf("a click outside didn't close the settings: %T", m.top())
	}
	clickOn(t, m, "q quit")
	// q quits: tea.Quit can't be told apart here, but nothing opened.
	if m.top() != nil {
		t.Fatalf("q opened %T", m.top())
	}
}

// TestDashboardMenus: ≡ menu lists every action and a click on one runs
// it; a right-click on a row opens its menu; a right-click on the
// sidebar opens the project's.
func TestDashboardMenus(t *testing.T) {
	_, m := popupData(t)
	clickOn(t, m, "≡ menu")
	mv, ok := m.top().(*menuView)
	if !ok || len(mv.items) != len(m.dashItems()) {
		t.Fatalf("≡ didn't open the menu of every action: %T", m.top())
	}
	clickItem(t, m, "switch project")
	if _, ok := m.top().(*switchView); !ok {
		t.Fatalf("the menu's switch project opened %T", m.top())
	}
	// The switcher: a click selects, a double-click opens.
	x, y := at(t, m, "beta")
	mouseAt(m, tea.MouseLeft, x, y)
	if sw := m.top().(*switchView); sw.sel != 1 {
		t.Fatalf("click selected project %d", sw.sel)
	}
	keyPress(m, "esc")

	// A row's menu: right-click the coordinator.
	x, y = atNth(t, m, "coordinator", 1)
	mouseAt(m, tea.MouseRight, x, y)
	mv, ok = m.top().(*menuView)
	if !ok || m.sel != "p:alpha" {
		t.Fatalf("right-click: %T, selected %q", m.top(), m.sel)
	}
	var labels []string
	for _, it := range mv.items {
		labels = append(labels, it.label)
	}
	if got := strings.Join(labels, ","); got != "attach,project popup,tasks,inbox" {
		t.Fatalf("the coordinator's menu: %s", got)
	}
	clickItem(t, m, "tasks")
	if b, ok := m.top().(*boardView); !ok || b.slug != "alpha" {
		t.Fatalf("the menu's tasks opened %T", m.top())
	}
	keyPress(m, "esc")

	// A thread's menu has take over.
	m.current = "beta"
	m.rebuild()
	x, y = at(t, m, "t-0005")
	mouseAt(m, tea.MouseRight, x, y)
	mv = m.top().(*menuView)
	if !slices.ContainsFunc(mv.items, func(it menuItem) bool { return it.label == "take over…" }) {
		t.Fatalf("a thread's menu lacks take over: %+v", mv.items)
	}
	clickItem(t, m, "take over…")
	if m.result.TakeOver != "s-5" || m.result.Attach != "s-5" {
		t.Fatalf("take over didn't ask to attach s-5: %+v", m.result)
	}
	m.busy, m.result = false, DashResult{}

	// The sidebar: a right-click on beta opens beta's menu.
	x, y = atSide(t, m, "beta")
	mouseAt(m, tea.MouseRight, x, y)
	mv, ok = m.top().(*menuView)
	if !ok || mv.title != "beta" || mv.items[0].label != "show its dashboard" {
		t.Fatalf("sidebar right-click: %T %+v", m.top(), mv)
	}
	clickItem(t, m, "project popup")
	if pv, ok := m.top().(*projectView); !ok || pv.slug != "beta" {
		t.Fatalf("the sidebar menu's project popup: %T", m.top())
	}
}

// TestPopupClicks: the project popup's tabs, rows and settings take
// clicks; the wheel scrolls the keys.
func TestPopupClicks(t *testing.T) {
	src, m := popupData(t)
	m.Update(keyPress(m, "a")()) // the board
	clickOn(t, m, "3 Tasks")
	pv := m.top().(*projectView)
	if pv.tab != tabTasks {
		t.Fatalf("click on 3 Tasks: tab %d", pv.tab)
	}
	clickOn(t, m, "Ship it")
	if pv.sel[tabTasks] != 1 {
		t.Fatalf("click on a task selected %d", pv.sel[tabTasks])
	}
	clickOn(t, m, "4 Settings")
	clickOn(t, m, "Coordinator approves")
	if pv.settings.sel != 2 || len(src.settings) != 1 || !strings.HasPrefix(src.settings[0], "projects.alpha.coordinator_approves=") {
		t.Fatalf("click on a setting: sel %d, settings %v", pv.settings.sel, src.settings)
	}
	clickOn(t, m, "5 Keys")
	mouseAt(m, tea.MouseWheelDown, m.sideW()+10, 10)
	m.render()
	if pv.top[tabKeys] == 0 {
		t.Fatal("the wheel didn't scroll the keys")
	}
	// The footer's buttons go to the popup: esc closes it.
	clickOn(t, m, "esc close")
	if m.top() != nil {
		t.Fatalf("esc close left %T", m.top())
	}
}

// TestDetailsWheel: the wheel over the details panel scrolls it, and a
// newly selected row's details start at the top.
func TestDetailsWheel(t *testing.T) {
	src := &fakeSource{data: testData()}
	m := newDash(DashOptions{Source: src, Width: 170, Height: 14, State: DashState{Current: "beta"}})
	m.setData(src.data)
	m.sel = "th:beta:t-0005"
	split, lw := m.split()
	if !split {
		t.Fatal("no details panel")
	}
	first := screen(m)
	mouseAt(m, tea.MouseWheelDown, m.sideW()+lw+5, 5)
	if m.sel != "th:beta:t-0005" || screen(m) == first {
		t.Fatalf("the wheel over the details didn't scroll them: sel %s", m.sel)
	}
	keyPress(m, "up")
	if m.render(); m.detailKey == m.sel {
		t.Fatal("another row kept the scroll")
	}
}

// TestHints: a key list's buttons, a hint naming several keys has one
// per key, and the status bar's buttons sit where they show.
func TestHints(t *testing.T) {
	hs := hints("enter attach · p ] [ projects · ↑ ↓ scroll · y yes · any other key no", 1)
	want := []hint{{1, 13, "enter"}, {16, 17, "p"}, {18, 19, "]"}, {20, 21, "["}, {22, 30, "p"}, {46, 51, "y"}, {54, 70, "n"}}
	if !slices.Equal(hs, want) {
		t.Fatalf("hints %v, want %v", hs, want)
	}
	info := proto.SessionInfo{ID: "s-4", Role: proto.RoleThread, Project: "p", Thread: "t-1"}
	line, hits := statusBar(info, nil, false, 100, "watch-only, "+takeOverHint)
	plain := []rune(ansi.Strip(line))
	got := map[string]string{}
	for _, h := range hits {
		got[h.key] = string(plain[h.x0:min(h.x1, len(plain))])
	}
	for key, text := range map[string]string{"menu": "≡ ", "d": "prefix+d dashboard", "u": takeOverHint} {
		if got[key] != text {
			t.Errorf("status button %s shows %q, want %q", key, got[key], text)
		}
	}
	// After the prefix, each command is a button.
	line, hits = statusBar(info, nil, true, 300, "")
	plain = []rune(ansi.Strip(line))
	for _, h := range hits {
		if h.key == "tab" && !strings.HasPrefix(string(plain[h.x0:]), "tab sidebar keys") {
			t.Errorf("z at %d: %q", h.x0, string(plain[h.x0:]))
		}
	}
	if hintAt(hits, len(plain)-3) != "prefix" {
		t.Error("prefix again sends it isn't a button")
	}
}

// TestAttachMenu: the session's ≡ menu leaves out what doesn't apply,
// a click on an item picks it and anywhere else closes it; the menu's
// box is where at says.
func TestAttachMenu(t *testing.T) {
	c := &client{cols: 100, rows: 30, sideW: 24, dashboard: true,
		focus: &pane{watch: true, info: proto.SessionInfo{ID: "s-2", Role: proto.RoleThread}}}
	labels := func(items []aitem) string {
		var out []string
		for _, it := range items {
			out = append(out, it.key)
		}
		return strings.Join(out, " ")
	}
	got := labels(c.sessionItems())
	if !strings.Contains(got, "prefix+u") || strings.Contains(got, "prefix+r") || !strings.Contains(got, "prefix+a") {
		t.Fatalf("a watched thread's menu: %s", got)
	}
	c.bare, c.dashboard = true, false
	c.focus = &pane{info: proto.SessionInfo{ID: "s-1", Role: proto.RoleCoordinator}}
	got = labels(c.sessionItems())
	if strings.Contains(got, "prefix+u") || !strings.Contains(got, "prefix+r") || strings.Contains(got, "prefix+a") {
		t.Fatalf("a bare coordinator's menu: %s", got)
	}
	picked := ""
	c.openMenu("", []aitem{{label: "one", run: func() { picked = "one" }}, {label: "two", run: func() { picked = "two" }}}, 98, 29, true)
	mn := c.menu
	w, h := mn.size()
	if mn.x+w > 100 || mn.y+h != 29 {
		t.Fatalf("menu at %d,%d size %dx%d: not above the status bar", mn.x, mn.y, w, h)
	}
	if i, in := mn.at(mn.x+2, mn.y+2); !in || i != 1 {
		t.Fatalf("at the second line: %d %v", i, in)
	}
	if l := mn.lines(); len(l) != h || ansi.StringWidth(l[1]) != w {
		t.Fatalf("menu lines %q", l)
	}
	c.mu.Lock()
	c.menuMouse(toEmu(uv.MouseClickEvent{X: mn.x + 2, Y: mn.y + 2, Button: uv.MouseLeft}))
	if picked != "two" || c.menu != nil {
		t.Fatalf("click on two: picked %q, menu %v", picked, c.menu)
	}
	c.openMenu("", []aitem{{label: "one", run: func() { picked = "one again" }}}, 0, 0, false)
	c.mu.Lock()
	c.menuMouse(toEmu(uv.MouseClickEvent{X: 60, Y: 20, Button: uv.MouseLeft}))
	if picked != "two" || c.menu != nil {
		t.Fatalf("click outside: picked %q, menu %v", picked, c.menu)
	}
}

func toEmu(ev uv.Event) emu.Mouse {
	m, _ := toMouse(ev)
	return m
}
