package tui

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/theclifmeister/termilator/internal/config"
	"github.com/theclifmeister/termilator/internal/project"
	"github.com/theclifmeister/termilator/internal/proto"
	"github.com/theclifmeister/termilator/internal/tasks"
	"github.com/theclifmeister/termilator/internal/thread"
	"github.com/theclifmeister/termilator/internal/view"
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
	alpha.Checkouts = map[string]string{"/src/alpha": "local main is 3 behind origin (uncommitted changes)"}
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
		"Project", "Alpha", "Goal", "Ship the alpha", "Repositories", "/src/alpha", "local main is 3 behind origin (uncommitted changes)", "Machines", "this one",
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
	for _, want := range []string{"IN MOTION", "T1    Write the README", "started · 1/2 · t-0002", "✓ Draft", "Review", "ON DECK", "T2    Ship it", "the coordinator changes tasks", "enter show · esc close"} {
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
		"Auto-close finished threads", "when its pull request merges", "Complete tasks", "by you",
		"Pull request follow-up"} {
		if !strings.Contains(out, want) {
			t.Errorf("settings tab lacks %q:\n%s", want, out)
		}
	}
	for range 7 {
		keyPress(m, "down")
	}
	if out := screen(m); !strings.Contains(out, "Remote control") {
		t.Errorf("settings tab lacks Remote control:\n%s", out)
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
	for range 6 {
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

	// Keep my checkout current: on by default, enter turns it off.
	keyPress(m, "down")
	if out := screen(m); !strings.Contains(out, "Keep my checkout current     on") {
		t.Fatalf("checkout row:\n%s", out)
	}
	act(m, src, "enter")
	if cfg, _ := config.Load(); must(cfg.Safety("alpha")).FastForwardCheckout {
		t.Fatal("fast-forward still on")
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
	keyPress(m, "down")
	for _, want := range []string{"when released", "when merged", "by you", "when released"} {
		act(m, src, "enter")
		if out := screen(m); !regexp.MustCompile(`Complete tasks +` + want).MatchString(out) {
			t.Fatalf("complete tasks, want %q:\n%s", want, out)
		}
	}
	cfg, _ := config.Load()
	if s := must(cfg.Safety("alpha")); s.ParallelThreads != 13 || s.AutoClose != config.CloseOff || s.AutoCloseDays != 9 || s.CompleteTasks != config.CompleteReleased {
		t.Fatalf("saved %+v", s)
	}
	path, _ := config.Path()
	data, _ := os.ReadFile(path)
	if want := "[projects.alpha]\nparallel_threads = 13\nauto_close = \"off\"\nauto_close_days = 9\ncomplete_tasks = \"released\"\n"; string(data) != want {
		t.Fatalf("file:\n%s", data)
	}
	for _, bad := range []string{"config", "toml", "auto_close", "parallel_threads", "complete_tasks"} {
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
	if m.top() != m.projectPopupView() || m.msg != "T2 is already waiting on the coordinator to delegate it" {
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
	if out = screen(m); !strings.Contains(out, "the coordinator changes tasks") || strings.Contains(out, "more ↓") {
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

// needsYouData is popupData with a task in review (T3, its PR merged
// but not released) and a blocked one (T4) in NEEDS YOU.
func needsYouData(t *testing.T, width int) (*fakeSource, *dash) {
	t.Helper()
	src, m := popupData(t)
	src.board.Tasks = append(src.board.Tasks,
		&tasks.Task{ID: 3, Title: "Check it", Status: tasks.Review, Thread: "t-0008",
			Notes: "Do the thing.\n\nreview (2026-10-05): Check: the bell shows whole"},
		&tasks.Task{ID: 4, Title: "Pick a licence", Status: tasks.Blocked,
			Notes: "blocked (2026-10-04): old\n\nblocked (2026-10-05): which licence, MIT or Apache?"})
	src.reviews = map[int]Review{3: {Check: []string{"Run tm, press t"}, PR: 61, Ship: ShipUnreleased}}
	m.Update(tea.WindowSizeMsg{Width: width, Height: 40})
	return src, m
}

// TestAcceptTask: in the Tasks tab, a on a task in review asks, then
// drops an accept item; the row waits on the coordinator, and a or x
// again only say so. a on a task not in review says why and keeps the
// popup open.
func TestAcceptTask(t *testing.T) {
	src, m := needsYouData(t, 86+sideDefault)
	m.Update(keyPress(m, "a")())
	keyPress(m, "3")
	if out := screen(m); !strings.Contains(out, "a accept · x send back · enter show · esc close") {
		t.Fatalf("review keys:\n%s", out)
	}
	keyPress(m, "a")
	cv, ok := m.top().(*confirmView)
	if !ok || cv.question != "Accept T3? The coordinator marks T3 Check it done." {
		t.Fatalf("a opened %T", m.top())
	}
	keyPress(m, "n")
	if m.msg != "T3 not accepted" || len(src.asked) != 0 {
		t.Fatalf("n: %q, %v", m.msg, src.asked)
	}
	keyPress(m, "a")
	act(m, src, "y")
	if len(src.asked) != 1 || src.asked[0] != "accept alpha T3" {
		t.Fatalf("asked %v", src.asked)
	}
	out := screen(m)
	if !strings.Contains(out, "review · waiting on the coordinator · t-0008") || !strings.Contains(out, "told the coordinator you accept T3; it marks it done") {
		t.Fatalf("after y:\n%s", out)
	}
	for _, k := range []string{"a", "x"} {
		keyPress(m, k)
		if m.top() != m.projectPopupView() || m.msg != "T3 is already waiting on the coordinator to accept it" || len(src.asked) != 1 {
			t.Fatalf("%s again: %T, %q, %v", k, m.top(), m.msg, src.asked)
		}
	}
	// T1 is started: a says why, and the popup stays.
	keyPress(m, "down")
	keyPress(m, "down")
	keyPress(m, "a")
	if m.top() != m.projectPopupView() || m.msg != "T1 is started, not in review; a accepts tasks in review" {
		t.Fatalf("a on started: %T, %q", m.top(), m.msg)
	}
	// Other tabs: a still closes the popup.
	keyPress(m, "1")
	keyPress(m, "a")
	if m.projectPopupView() != nil {
		t.Fatal("a on the overview kept the popup")
	}
}

// TestSendBack: x on a task in review in the t list asks for a note;
// enter sends it, esc or an empty note cancel.
func TestSendBack(t *testing.T) {
	src, m := needsYouData(t, 86+sideDefault)
	m.Update(keyPress(m, "t")())
	keyPress(m, "x")
	if _, ok := m.top().(*inputView); !ok {
		t.Fatalf("x opened %T", m.top())
	}
	keyPress(m, "esc")
	if _, ok := m.top().(*boardView); !ok || m.msg != "T3 not sent back" {
		t.Fatalf("esc: %T, %q", m.top(), m.msg)
	}
	keyPress(m, "x")
	keyPress(m, "enter")
	if m.msg != "T3 not sent back" || len(src.asked) != 0 {
		t.Fatalf("empty note: %q, %v", m.msg, src.asked)
	}
	keyPress(m, "x")
	if out := screen(m); !strings.Contains(out, "Send T3 back. What should change?") || !strings.Contains(out, "0/200") {
		t.Fatalf("prompt:\n%s", out)
	}
	for _, r := range "bell cut" {
		keyPress(m, string(r))
	}
	run(m, keyPress(m, "enter"))
	m.setData(src.Load())
	if len(src.asked) != 1 || src.asked[0] != "send-back alpha T3 bell cut" || m.msg != "sent T3 back with your note; the coordinator passes it on" {
		t.Fatalf("asked %v, %q", src.asked, m.msg)
	}
	if out := screen(m); !strings.Contains(out, "waiting on coordinator") {
		t.Fatalf("row:\n%s", out)
	}
	// x on a task not in review or done only says why.
	keyPress(m, "down")
	keyPress(m, "x")
	if _, ok := m.top().(*boardView); !ok || m.msg != "T4 is blocked, not in review or done; x sends back tasks in review or done" {
		t.Fatalf("x on blocked: %T, %q", m.top(), m.msg)
	}
}

// TestSendBackDone: the t list shows done tasks last; x sends one back
// (the coordinator reopens it), a only says it isn't in review.
func TestSendBackDone(t *testing.T) {
	src, m := needsYouData(t, 86+sideDefault)
	src.board.Tasks = append(src.board.Tasks, &tasks.Task{ID: 5, Title: "Shipped", Status: tasks.Done, Thread: "t-0009",
		Notes: "done (2026-10-05): released in v0.5.0 (PR #70), by the project's setting"})
	m.Update(keyPress(m, "t")())
	out := screen(m)
	if !strings.Contains(out, "DONE") || !strings.Contains(out, "T5    Shipped") {
		t.Fatalf("no done task:\n%s", out)
	}
	for range 10 {
		keyPress(m, "down")
	}
	if out := screen(m); !strings.Contains(out, "enter show · x send back · esc back") {
		t.Fatalf("done keys:\n%s", out)
	}
	keyPress(m, "a")
	if m.msg != "T5 is done, not in review; a accepts tasks in review" {
		t.Fatalf("a on done: %q", m.msg)
	}
	keyPress(m, "x")
	if out := screen(m); !strings.Contains(out, "Send T5 back. What should change?") {
		t.Fatalf("prompt:\n%s", out)
	}
	for _, r := range "still broken" {
		keyPress(m, string(r))
	}
	run(m, keyPress(m, "enter"))
	if len(src.asked) != 1 || src.asked[0] != "send-back alpha T5 still broken" {
		t.Fatalf("asked %v, %q", src.asked, m.msg)
	}
}

// TestReviewDetail: a task in review, shown, says whether its change
// shipped and how to check it, from the report and the task's notes;
// enter in the Tasks tab shows it too, and esc goes back to the tab.
func TestReviewDetail(t *testing.T) {
	src, m := needsYouData(t, 86+sideDefault)
	m.Update(keyPress(m, "t")())
	keyPress(m, "enter")
	out := screen(m)
	for _, want := range []string{"PR #61 merged, not released: wait for the next release to test it",
		"How to check", "• Run tm, press t", "• the bell shows whole", "a accept · x send back · esc back"} {
		if !strings.Contains(out, want) {
			t.Errorf("review detail lacks %q:\n%s", want, out)
		}
	}
	keyPress(m, "a")
	act(m, src, "y")
	if out := screen(m); !strings.Contains(out, "waiting on the coordinator to accept it") {
		t.Fatalf("shown task after a:\n%s", out)
	}
	keyPress(m, "esc")
	keyPress(m, "esc")

	for ship, want := range map[string]string{ShipReleased: "PR #61 released in v0.4.0", ShipOpen: "PR #61 open, not merged yet",
		ShipMerged: "PR #61 merged", ShipClosed: "PR #61 closed without merging"} {
		if got := ansi.Strip(shipLine(Review{PR: 61, Ship: ship, Tag: "v0.4.0"})); got != want {
			t.Errorf("shipLine(%s) = %q, want %q", ship, got, want)
		}
	}
	if shipLine(Review{Ship: ShipMerged}) != "" {
		t.Error("shipLine without a PR")
	}

	m.Update(keyPress(m, "a")())
	keyPress(m, "3")
	keyPress(m, "enter")
	b, ok := m.top().(*boardView)
	if !ok || !b.open || !strings.Contains(screen(m), "PR #61 merged, not released") {
		t.Fatalf("enter in the Tasks tab: %T\n%s", m.top(), screen(m))
	}
	keyPress(m, "down") // stays on the task
	keyPress(m, "esc")
	if pv := m.projectPopupView(); m.top() != pv || pv.tab != tabTasks {
		t.Fatalf("esc: %T", m.top())
	}
}

// TestBlockedTask: a blocked task, shown, says what it is blocked on
// (its latest blocked note); c opens the project's coordinator.
func TestBlockedTask(t *testing.T) {
	src, m := needsYouData(t, 86+sideDefault)
	m.Update(keyPress(m, "t")())
	keyPress(m, "down")
	if out := screen(m); !strings.Contains(out, "enter show · c coordinator · esc back") {
		t.Fatalf("blocked keys:\n%s", out)
	}
	keyPress(m, "enter")
	out := screen(m)
	if !strings.Contains(out, "Blocked on: which licence, MIT or Apache?") || !strings.Contains(out, "c coordinator · d delegate · esc back") {
		t.Fatalf("blocked detail:\n%s", out)
	}
	run(m, keyPress(m, "c"))
	if len(src.opened) != 1 || src.opened[0] != "alpha" {
		t.Fatalf("c opened %v", src.opened)
	}
	if got := blockedOn(&tasks.Task{Status: tasks.Blocked, Notes: "just notes"}); got != "" {
		t.Fatalf("blockedOn without a note: %q", got)
	}
}

// TestNeedsYouNarrow: beside the default sidebar in the narrowest
// window that shows it whole, the task keys and the waiting label show
// whole.
func TestNeedsYouNarrow(t *testing.T) {
	src, m := needsYouData(t, sideDefault+view.SideRoom)
	if m.sideW() != sideDefault {
		t.Fatalf("sidebar %d", m.sideW())
	}
	m.Update(keyPress(m, "t")())
	foot := func() string {
		lines := strings.Split(screen(m), "\n")
		return strings.Join(lines[len(lines)-3:], "\n")
	}
	if f := foot(); !strings.Contains(f, "enter show · a accept · x send back · esc back") {
		t.Fatalf("t list keys:\n%s", f)
	}
	keyPress(m, "a")
	act(m, src, "y")
	if out := screen(m); !strings.Contains(out, "waiting on coordinator") {
		t.Fatalf("t list row:\n%s", out)
	}
	keyPress(m, "enter")
	if f := foot(); !strings.Contains(f, "a accept · x send back · esc back") {
		t.Fatalf("shown task keys:\n%s", f)
	}
	keyPress(m, "esc")
	keyPress(m, "down")
	if f := foot(); !strings.Contains(f, "enter show · c coordinator · esc back") {
		t.Fatalf("blocked keys:\n%s", f)
	}
	keyPress(m, "enter")
	if f := foot(); !strings.Contains(f, "c coordinator · d delegate · esc back") {
		t.Fatalf("blocked shown keys:\n%s", f)
	}
	keyPress(m, "esc")
	keyPress(m, "esc")
	m.Update(keyPress(m, "a")())
	keyPress(m, "3")
	if f := foot(); !strings.Contains(f, "a accept · x send back · enter show · esc close") {
		t.Fatalf("Tasks tab keys:\n%s", f)
	}
	if out := screen(m); !strings.Contains(out, "review · waiting on the coordinator ") {
		t.Fatalf("Tasks tab row:\n%s", out)
	}
}
