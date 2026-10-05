package tui

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/theclifmeister/termilator/internal/config"
	"github.com/theclifmeister/termilator/internal/project"
	"github.com/theclifmeister/termilator/internal/proto"
	"github.com/theclifmeister/termilator/internal/tasks"
	"github.com/theclifmeister/termilator/internal/thread"
)

// keyPress sends a named key: tab, shift+tab, esc, space, enter, up,
// down, or a character.
func keyPress(m *dash, name string) tea.Cmd {
	var msg tea.KeyPressMsg
	switch name {
	case "tab":
		msg = tea.KeyPressMsg{Code: tea.KeyTab}
	case "shift+tab":
		msg = tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift}
	case "esc":
		msg = tea.KeyPressMsg{Code: tea.KeyEscape}
	case "space":
		msg = tea.KeyPressMsg{Code: tea.KeySpace, Text: " "}
	case "enter":
		msg = tea.KeyPressMsg{Code: tea.KeyEnter}
	case "up":
		msg = tea.KeyPressMsg{Code: tea.KeyUp}
	case "down":
		msg = tea.KeyPressMsg{Code: tea.KeyDown}
	case "left":
		msg = tea.KeyPressMsg{Code: tea.KeyLeft}
	case "right":
		msg = tea.KeyPressMsg{Code: tea.KeyRight}
	case "ctrl+a":
		msg = tea.KeyPressMsg{Code: 'a', Mod: tea.ModCtrl}
	default:
		msg = tea.KeyPressMsg{Code: rune(name[0]), Text: name}
	}
	_, cmd := m.Update(msg)
	return cmd
}

// act presses a key and runs the action it starts, then polls again.
func act(m *dash, src *fakeSource, name string) {
	run(m, keyPress(m, name))
	m.setData(src.Load())
}

func popupData(t *testing.T) (*fakeSource, *dash) {
	t.Helper()
	t.Setenv("TERMILATOR_HOME", t.TempDir())
	src := &fakeSource{data: testData(), agents: []string{"claude", "pi"}}
	alpha := &src.data.Projects[0]
	alpha.Name, alpha.Goal, alpha.Repos = "Alpha", "Ship the alpha", []string{"/src/alpha"}
	alpha.Items = []project.Item{{ID: "x", Kind: "report", Summary: "t-0002 handed in report 1"}}
	// t-0002 is blocked (it counts toward the cap), t-0003 idle.
	alpha.Threads = []ThreadRow{
		{Record: &thread.Record{ID: "t-0002", Title: "README", State: thread.Running, Session: "s-2"}},
		{Record: &thread.Record{ID: "t-0003", Title: "Idle", State: thread.Running, Session: "s-9"}},
	}
	src.data.Sessions = append(src.data.Sessions, proto.SessionInfo{ID: "s-9", Role: proto.RoleThread, Project: "alpha", Thread: "t-0003", State: "idle"})
	src.board = &tasks.Board{Tasks: []*tasks.Task{
		{ID: 1, Title: "Write the README", Status: tasks.Started, Thread: "t-0002",
			Steps: []tasks.Step{{N: 1, Text: "Draft", Done: true}, {N: 2, Text: "Review"}}},
		{ID: 2, Title: "Ship it", Status: tasks.Ready},
	}}
	m := newDash(DashOptions{Source: src, Width: 86 + sideDefault, Height: 40, Cwd: "/work"})
	m.setData(src.Load())
	m.sel = "p:alpha"
	return src, m
}

// TestProjectPopup: a opens the selected project's popup, its tabs
// switch with tab, shift+tab and 1-5, and esc closes it.
func TestProjectPopup(t *testing.T) {
	src, m := popupData(t)
	cmd := keyPress(m, "a")
	pv, ok := m.top().(*projectView)
	if !ok || pv.slug != "alpha" {
		t.Fatalf("a opened %T", m.top())
	}
	m.Update(cmd()) // the board
	out := screen(m)
	for _, want := range []string{"─ alpha ─", "1 Overview", "2 Inbox 1", "5 Keys",
		"Project", "Alpha", "Goal", "Ship the alpha", "Repositories", "/src/alpha", "Machines", "this one",
		"Coordinator", "claude · s-1 blocked", "1 needs you (3 → Tasks) · 1 in motion"} {
		if !strings.Contains(out, want) {
			t.Errorf("overview lacks %q:\n%s", want, out)
		}
	}

	keyPress(m, "tab")
	if out := screen(m); pv.tab != tabInbox || !strings.Contains(out, "t-0002 handed in report 1") || !strings.Contains(out, "Read-only") {
		t.Fatalf("inbox tab:\n%s", out)
	}
	keyPress(m, "tab")
	out = screen(m)
	for _, want := range []string{"IN MOTION", "T1    Write the README", "started · 1/2 · t-0002", "✓ Draft", "Review", "ON DECK", "T2    Ship it", "d asks the coordinator to delegate a task", "d delegate"} {
		if !strings.Contains(out, want) {
			t.Errorf("tasks tab lacks %q:\n%s", want, out)
		}
	}
	keyPress(m, "5")
	if pv.tab != tabKeys {
		t.Fatalf("5: tab %d", pv.tab)
	}
	keyPress(m, "shift+tab")
	if pv.tab != tabSettings {
		t.Fatalf("shift+tab: tab %d", pv.tab)
	}
	out = screen(m)
	for _, want := range []string{"Start threads", "ask first", "Yolo mode", "Coordinator approves", "Parallel threads", "10 · 1 working now",
		"Auto-close finished threads", "when its pull request merges",
		"Pull request follow-up", "Remote control"} {
		if !strings.Contains(out, want) {
			t.Errorf("settings tab lacks %q:\n%s", want, out)
		}
	}
	keyPress(m, "esc")
	if m.top() != nil {
		t.Fatalf("esc left %T", m.top())
	}
	if len(src.settings) != 0 {
		t.Fatalf("looking changed settings: %v", src.settings)
	}
}

// TestProjectSettingsToggle: enter and space change a setting in place;
// it is written to the settings file and shows at once. Yolo asks.
func TestProjectSettingsToggle(t *testing.T) {
	src, m := popupData(t)
	keyPress(m, "a")
	keyPress(m, "4")
	act(m, src, "enter") // start threads
	if !strings.Contains(screen(m), "automatically") {
		t.Fatalf("start threads didn't change:\n%s", screen(m))
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if s, _ := cfg.Safety("alpha"); s.StartThreads != config.StartAuto {
		t.Fatalf("saved %+v", s)
	}

	// Yolo asks first: n keeps it off, y turns it on.
	keyPress(m, "down")
	act(m, src, "space")
	if _, ok := m.top().(*confirmView); !ok {
		t.Fatalf("yolo didn't ask: %T", m.top())
	}
	act(m, src, "n")
	if cfg, _ := config.Load(); must(cfg.Safety("alpha")).Yolo {
		t.Fatal("n turned yolo on")
	}
	act(m, src, "space")
	act(m, src, "y")
	if cfg, _ := config.Load(); !must(cfg.Safety("alpha")).Yolo {
		t.Fatal("y left yolo off")
	}

	// Remote control: the row, and a note when the running coordinator
	// differs.
	for range 5 {
		keyPress(m, "down")
	}
	src.data.Sessions[0].RemoteControl = true
	m.setData(src.Load())
	if out := screen(m); !strings.Contains(out, "the running coordinator has it on (prefix+r)") {
		t.Fatalf("no running note:\n%s", out)
	}
	act(m, src, "enter")
	if cfg, _ := config.Load(); !must(cfg.Safety("alpha")).CoordinatorRemoteControl {
		t.Fatal("remote control not saved")
	}
	if out := screen(m); strings.Contains(out, "the running coordinator has it") {
		t.Fatalf("note after the setting matches:\n%s", out)
	}
	path, _ := config.Path()
	data, _ := os.ReadFile(path)
	if want := "[projects.alpha]\nstart_threads = \"auto\"\nyolo = true\ncoordinator_remote_control = true\n"; string(data) != want {
		t.Fatalf("file:\n%s", data)
	}
}

func must(s config.Safety, _ error) config.Safety { return s }

// TestProjectSettingsNumbers: parallel threads steps with enter and
// + / -; auto-close cycles off, merged, days, and + / - set the days.
func TestProjectSettingsNumbers(t *testing.T) {
	src, m := popupData(t)
	keyPress(m, "a")
	keyPress(m, "4")
	for range 3 {
		keyPress(m, "down")
	}
	act(m, src, "enter") // 10 → 15
	act(m, src, "-")
	act(m, src, "-")
	if out := screen(m); !strings.Contains(out, "13 · 1 working now") {
		t.Fatalf("parallel threads:\n%s", out)
	}
	keyPress(m, "down")
	act(m, src, "enter")
	if out := screen(m); !strings.Contains(out, "7 days after it finishes") {
		t.Fatalf("auto-close days:\n%s", out)
	}
	act(m, src, "+")
	act(m, src, "+")
	if out := screen(m); !strings.Contains(out, "9 days after it finishes") {
		t.Fatalf("auto-close +:\n%s", out)
	}
	act(m, src, "enter")
	if out := screen(m); !strings.Contains(out, "Auto-close finished threads  off") {
		t.Fatalf("auto-close off:\n%s", out)
	}
	cfg, _ := config.Load()
	if s := must(cfg.Safety("alpha")); s.ParallelThreads != 13 || s.AutoClose != config.CloseOff || s.AutoCloseDays != 9 {
		t.Fatalf("saved %+v", s)
	}
	path, _ := config.Path()
	data, _ := os.ReadFile(path)
	if want := "[projects.alpha]\nparallel_threads = 13\nauto_close = \"off\"\nauto_close_days = 9\n"; string(data) != want {
		t.Fatalf("file:\n%s", data)
	}
	for _, bad := range []string{"config", "toml", "auto_close", "parallel_threads"} {
		if strings.Contains(screen(m), bad) {
			t.Fatalf("the popup names %q:\n%s", bad, screen(m))
		}
	}
}

// TestProjectRepos: + adds a repository from a typed path, x removes the
// selected one after a y.
func TestProjectRepos(t *testing.T) {
	src, m := popupData(t)
	dir := t.TempDir()
	keyPress(m, "a")
	keyPress(m, "+")
	in, ok := m.top().(*inputView)
	if !ok {
		t.Fatalf("+ opened %T", m.top())
	}
	in.text = dir
	act(m, src, "enter")
	real, _ := filepath.EvalSymlinks(dir)
	if len(src.repos) != 1 || src.repos[0] != "+"+real {
		t.Fatalf("repos %v", src.repos)
	}
	if !strings.Contains(screen(m), real[:30]) {
		t.Fatalf("added repo not shown:\n%s", screen(m))
	}
	keyPress(m, "x")
	act(m, src, "y")
	if len(src.repos) != 2 || src.repos[1] != "-/src/alpha" {
		t.Fatalf("repos %v", src.repos)
	}
}

// TestDelegateTask: d on a ready task in the Tasks tab asks, then drops
// a delegate item; the row shows it waits on the coordinator. A started
// task, or one already asked, only gets a footer message.
func TestDelegateTask(t *testing.T) {
	src, m := popupData(t)
	m.Update(keyPress(m, "a")())
	keyPress(m, "3")
	// T1 is started: d says why it does nothing.
	keyPress(m, "d")
	if m.top() != m.projectPopupView() || !strings.Contains(m.msg, "T1 is started") || len(src.delegated) != 0 {
		t.Fatalf("d on a started task: %T, %q, %v", m.top(), m.msg, src.delegated)
	}
	keyPress(m, "down")
	keyPress(m, "d")
	cv, ok := m.top().(*confirmView)
	if !ok || !strings.HasPrefix(cv.question, "Delegate T2 to the coordinator?") {
		t.Fatalf("d on T2 opened %T", m.top())
	}
	keyPress(m, "n")
	if m.msg != "T2 not delegated" || len(src.delegated) != 0 {
		t.Fatalf("n: %q, %v", m.msg, src.delegated)
	}
	keyPress(m, "d")
	act(m, src, "y")
	if len(src.delegated) != 1 || src.delegated[0] != "alpha T2" {
		t.Fatalf("delegated %v", src.delegated)
	}
	out := screen(m)
	if !strings.Contains(out, "ready · waiting on the coordinator") || !strings.Contains(out, "asked the coordinator to delegate T2") {
		t.Fatalf("after y:\n%s", out)
	}
	keyPress(m, "d")
	if m.top() != m.projectPopupView() || m.msg != "T2 is already waiting on the coordinator" {
		t.Fatalf("d again: %T, %q", m.top(), m.msg)
	}
}

// TestDelegateFromBoard: the t list takes d too, on a row and on a
// shown task; a task in review isn't delegated.
func TestDelegateFromBoard(t *testing.T) {
	src, m := popupData(t)
	src.board.Tasks = append(src.board.Tasks, &tasks.Task{ID: 3, Title: "Check it", Status: tasks.Review})
	m.Update(keyPress(m, "t")())
	// Needs you first: T3 (review).
	keyPress(m, "d")
	if _, ok := m.top().(*boardView); !ok || !strings.Contains(m.msg, "T3 is in review") {
		t.Fatalf("d on review: %T, %q", m.top(), m.msg)
	}
	keyPress(m, "down")
	keyPress(m, "down")
	keyPress(m, "enter") // T2, shown
	if out := screen(m); !strings.Contains(out, "d delegate") {
		t.Fatalf("shown task lacks d:\n%s", out)
	}
	keyPress(m, "d")
	act(m, src, "y")
	if len(src.delegated) != 1 || src.delegated[0] != "alpha T2" {
		t.Fatalf("delegated %v", src.delegated)
	}
	if out := screen(m); !strings.Contains(out, "waiting on the coordinator") {
		t.Fatalf("shown task:\n%s", out)
	}
	keyPress(m, "esc")
	if out := screen(m); !strings.Contains(out, "waiting on coordinator") || !strings.Contains(out, "d delegate") {
		t.Fatalf("board:\n%s", out)
	}
}

// TestKeysParity: the Keys tab and the help are the same list, and it
// names every dashboard action and session command.
func TestKeysParity(t *testing.T) {
	_, m := popupData(t)
	keyPress(m, "?")
	help := m.top().(*helpView).box(m).body
	keyPress(m, "x")
	keyPress(m, "a")
	keyPress(m, "5")
	// Both are keyLines, each wrapped to its own box's width.
	b := m.top().(*projectView).box(m)
	if want := keyLines(m.inner(b.width)); strings.Join(b.body, "\n") != strings.Join(want, "\n") {
		t.Fatalf("keys tab isn't the keys:\n%s", strings.Join(b.body, "\n"))
	}
	if want := keyLines(m.inner(m.w)); strings.Join(help, "\n") != strings.Join(want, "\n") {
		t.Fatalf("help isn't the keys:\n%s", strings.Join(help, "\n"))
	}
	text := ansi.Strip(strings.Join(help, "\n"))
	for _, a := range actions {
		if a.label != "" && !strings.Contains(text, a.label+" ") {
			t.Errorf("keys lack %q", a.label)
		}
	}
	for k := range prefixCommands {
		if !strings.Contains(text, "prefix+") || !strings.Contains(text, k) {
			t.Errorf("keys lack prefix+%s", k)
		}
	}
	for k := range paneCommands {
		name := strings.TrimPrefix(k, "ctrl+")
		if name == "left" || name == "right" || name == "up" || name == "down" {
			name = "arrows"
		}
		if !strings.Contains(text, name) {
			t.Errorf("keys lack the pane command %s", k)
		}
	}
	// Wide windows: nothing is cut off; lines wrap instead.
	for _, l := range keyLines(60) {
		if w := ansi.StringWidth(l); w > 60 {
			t.Errorf("line %d cells wide: %q", w, l)
		}
	}
}

// TestNoFileNamesInUI: no screen of the dashboard, its popups or the
// status bar names the settings file, TOML or a setting's key.
func TestNoFileNamesInUI(t *testing.T) {
	src, m := popupData(t)
	src.data.Sessions[0].RemoteControl = true
	m.setData(src.Load())
	var screens []string
	snap := func() { screens = append(screens, whole(m)) }
	snap()
	for _, k := range []string{"?", "esc", ",", "enter", "esc", "esc", "i", "esc", "t", "esc", "p", "esc"} {
		keyPress(m, k)
		snap()
	}
	keyPress(m, "a")
	for range tabCount {
		snap()
		keyPress(m, "tab")
	}
	keyPress(m, "4")
	keyPress(m, "down")
	keyPress(m, "enter") // the yolo question
	snap()
	screens = append(screens, statusLine(proto.SessionInfo{ID: "s-1", Project: "alpha", Role: proto.RoleCoordinator, RemoteControl: true}, nil, false, 200, ""),
		statusLine(proto.SessionInfo{ID: "s-1"}, nil, true, 400, ""))
	// "Yolo mode" is the setting's label; its key is only ever yolo =.
	keys := []string{"config", "toml", "ui.json", "$EDITOR", "$VISUAL", "yolo =", "yolo:", "[projects", "[keys"}
	for _, k := range config.ProjectKeys {
		if strings.Contains(k, "_") {
			keys = append(keys, k)
		}
	}
	for _, s := range screens {
		low := strings.ToLower(s)
		for _, bad := range keys {
			if strings.Contains(low, strings.ToLower(bad)) {
				t.Errorf("a screen names %q:\n%s", bad, s)
			}
		}
	}
	// A broken file is reported by line, without its name or keys.
	if e := settingsErr(errors.New("/h/config.toml: toml: line 3 (last key \"projects.a.yolo\"): bad")); e.Error() != "the settings can't be read: line 3 is broken" {
		t.Errorf("broken file: %v", e)
	}
}

// TestPrefixCapture: the , popup's prefix row takes the next ctrl+key,
// the current prefix too, and saves it; the default agent steps through
// the known agents.
func TestPrefixCapture(t *testing.T) {
	src, m := popupData(t)
	keyPress(m, ",")
	keyPress(m, "enter")
	if _, ok := m.top().(*captureView); !ok {
		t.Fatalf("enter on the prefix opened %T", m.top())
	}
	keyPress(m, "x")
	if !strings.Contains(screen(m), "can't be the prefix") {
		t.Fatalf("x taken as the prefix:\n%s", screen(m))
	}
	act(m, src, "ctrl+a")
	if m.prefix != "ctrl+a" {
		t.Fatalf("prefix %q", m.prefix)
	}
	if c, err := prefixKey(); err != nil || c.String() != "ctrl+a" {
		t.Fatalf("saved prefix %v %v", c, err)
	}
	keyPress(m, "down")
	act(m, src, "enter")
	if got := config.DefaultAgent(DefaultAgent); got != "pi" {
		t.Fatalf("default agent %q", got)
	}
	if !strings.Contains(screen(m), "pi") {
		t.Fatalf("default agent not shown:\n%s", screen(m))
	}
}

// TestProjectPopupFixed: the project popup's size and place come from
// the window, not a tab's content, so they stay put across tabs; it has
// no ×; the tab bar stays at its top while long content scrolls under
// it, keeping the selection in view; and esc closes it from every tab.
func TestProjectPopupFixed(t *testing.T) {
	src, m := popupData(t)
	for i := range 40 {
		src.board.Tasks = append(src.board.Tasks, &tasks.Task{ID: 10 + i, Title: fmt.Sprintf("Task number %d", 10+i), Status: tasks.Ready})
	}
	for _, size := range [][2]int{{110, 40}, {200, 60}, {80, 24}, {60, 16}} {
		m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
		m.Update(keyPress(m, "a")()) // the board
		pv := m.top().(*projectView)
		var geo *boxGeo
		for tab := range tabCount {
			pv.tab = tab
			out := screen(m)
			if geo == nil {
				geo = m.geo
			} else if g := m.geo; g.x != geo.x || g.y != geo.y || g.w != geo.w || g.h != geo.h {
				t.Fatalf("%v: tab %d is at %d,%d %d×%d, tab 0 at %d,%d %d×%d", size, tab, g.x, g.y, g.w, g.h, geo.x, geo.y, geo.w, geo.h)
			}
			if strings.Contains(out, "×") {
				t.Fatalf("%v: tab %d has a ×:\n%s", size, tab, out)
			}
		}
		keyPress(m, "esc")
		if m.top() != nil {
			t.Fatalf("%v: esc left %T", size, m.top())
		}
	}

	m.Update(tea.WindowSizeMsg{Width: 110, Height: 40})
	m.Update(keyPress(m, "a")())
	pv := m.top().(*projectView)
	keyPress(m, "3")
	for range 30 {
		keyPress(m, "down")
		m.render()
	}
	out := screen(m)
	sel := pv.tasks()[pv.sel[tabTasks]].Title
	if pv.sel[tabTasks] != 30 || !strings.Contains(out, sel) || !strings.Contains(out, "1 Overview") || !strings.Contains(out, "more ↑ ↓") {
		t.Fatalf("down 30 times: sel %d (%s), tab bar and both arrows wanted:\n%s", pv.sel[tabTasks], sel, out)
	}
	if strings.Contains(out, "IN MOTION") {
		t.Fatalf("the content didn't scroll:\n%s", out)
	}
	// The wheel inside the content moves the selection, which stays in
	// view.
	x, y := at(t, m, sel)
	for range 15 {
		mouseAt(m, tea.MouseWheelDown, x, y)
	}
	out = screen(m)
	if sel = pv.tasks()[pv.sel[tabTasks]].Title; pv.sel[tabTasks] != 41 || !strings.Contains(out, sel) || !strings.Contains(out, "1 Overview") {
		t.Fatalf("wheel: sel %d (%s):\n%s", pv.sel[tabTasks], sel, out)
	}
	// Past the last task the arrows scroll on to the content's end.
	for range 5 {
		keyPress(m, "down")
		m.render()
	}
	if out = screen(m); !strings.Contains(out, "done: 0") || strings.Contains(out, "more ↓") {
		t.Fatalf("the end of the tasks:\n%s", out)
	}
	// Back up to the top: the first group shows again.
	for range 50 {
		keyPress(m, "up") // several keys may come between frames
	}
	if out = screen(m); pv.sel[tabTasks] != 0 || !strings.Contains(out, "IN MOTION") {
		t.Fatalf("back up: sel %d:\n%s", pv.sel[tabTasks], out)
	}
	// A click on a tab still picks it, wherever the content is scrolled.
	clickOn(t, m, "5 Keys")
	if pv.tab != tabKeys {
		t.Fatalf("click on 5 Keys: tab %d", pv.tab)
	}
	for range 3 {
		mouseAt(m, tea.MouseWheelDown, x, y)
	}
	if out = screen(m); pv.top[tabKeys] != 9 || !strings.Contains(out, "1 Overview") {
		t.Fatalf("wheel on the keys: top %d:\n%s", pv.top[tabKeys], out)
	}
}
