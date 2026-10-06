package tui

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/theclifmeister/terminatr/internal/config"
	"github.com/theclifmeister/terminatr/internal/project"
	"github.com/theclifmeister/terminatr/internal/proto"
	"github.com/theclifmeister/terminatr/internal/tasks"
	"github.com/theclifmeister/terminatr/internal/thread"
	"github.com/theclifmeister/terminatr/internal/view"
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
	t.Setenv("TERMINATR_HOME", t.TempDir())
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
// switch with ← →, and 1-6, and esc closes it.
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

	keyPress(m, "right")
	if out := screen(m); pv.tab != tabInbox || !strings.Contains(out, "t-0002 handed in report 1") || !strings.Contains(out, "Read-only") {
		t.Fatalf("inbox tab:\n%s", out)
	}
	keyPress(m, "right")
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
	keyPress(m, "left")
	if pv.tab != tabSettings {
		t.Fatalf("left: tab %d", pv.tab)
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

// TestProjectLifecycleRows: Paused toggles at once and marks the project
// in the sidebar; Archive and Delete ask first, then close the popup.
func TestProjectLifecycleRows(t *testing.T) {
	src, m := popupData(t)
	keyPress(m, "a")
	keyPress(m, "4")
	pick := func(label string) {
		t.Helper()
		pv := m.projectPopupView()
		for i, r := range pv.settings.rows {
			if r.label == label {
				pv.settings.sel = i
				return
			}
		}
		t.Fatalf("no %q row", label)
	}
	paused := func() bool {
		cfg, err := config.Load()
		if err != nil {
			t.Fatal(err)
		}
		return must(cfg.Safety("alpha")).Paused
	}
	pick("Paused")
	act(m, src, "enter")
	if !paused() {
		t.Fatal("not paused")
	}
	rows := buildTree(m.data.Projects, m.data.Sessions, treeIn{})
	if !rows[0].paused || !strings.Contains(treeCells(rows[0], 30, false, false), "alpha"+ic().paused) ||
		!strings.Contains(treeCells(rows[0], 12, true, false), "alpha"+ic().paused) {
		t.Fatalf("sidebar lacks the paused mark: %+v %q", rows[0], treeCells(rows[0], 30, false, false))
	}
	act(m, src, "enter")
	if paused() {
		t.Fatal("still paused")
	}

	pick("Archive")
	act(m, src, "enter")
	if _, ok := m.top().(*confirmView); !ok {
		t.Fatalf("archive didn't ask: %T", m.top())
	}
	act(m, src, "n")
	if len(src.lifecycle) != 2 || m.projectPopupView() == nil {
		t.Fatalf("n archived: %v", src.lifecycle)
	}
	act(m, src, "enter")
	act(m, src, "y")
	if got := src.lifecycle[len(src.lifecycle)-1]; got != "alpha archive" || m.projectPopupView() != nil {
		t.Fatalf("archive: %v, popup %v", src.lifecycle, m.projectPopupView())
	}

	keyPress(m, "a")
	keyPress(m, "4")
	pick("Delete")
	act(m, src, "enter")
	act(m, src, "y")
	if got := src.lifecycle[len(src.lifecycle)-1]; got != "alpha delete" || m.projectPopupView() != nil {
		t.Fatalf("delete: %v", src.lifecycle)
	}
}

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
	for _, want := range []string{"when merged", "by you", "when merged"} {
		act(m, src, "enter")
		if out := screen(m); !regexp.MustCompile(`Complete tasks +` + want).MatchString(out) {
			t.Fatalf("complete tasks, want %q:\n%s", want, out)
		}
	}
	cfg, _ := config.Load()
	if s := must(cfg.Safety("alpha")); s.ParallelThreads != 13 || s.AutoClose != config.CloseOff || s.AutoCloseDays != 9 || s.CompleteTasks != config.CompleteMerged {
		t.Fatalf("saved %+v", s)
	}
	path, _ := config.Path()
	data, _ := os.ReadFile(path)
	if want := "[projects.alpha]\nparallel_threads = 13\nauto_close = \"off\"\nauto_close_days = 9\ncomplete_tasks = \"merged\"\n"; string(data) != want {
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
	keyPress(m, "D")
	if m.top() != m.projectPopupView() || !strings.Contains(m.msg, "T1 is started") || len(src.delegated) != 0 {
		t.Fatalf("d on a started task: %T, %q, %v", m.top(), m.msg, src.delegated)
	}
	keyPress(m, "down")
	keyPress(m, "D")
	cv, ok := m.top().(*confirmView)
	if !ok || !strings.HasPrefix(cv.question, "Delegate T2 to the coordinator?") {
		t.Fatalf("d on T2 opened %T", m.top())
	}
	keyPress(m, "n")
	if m.msg != "T2 not delegated" || len(src.delegated) != 0 {
		t.Fatalf("n: %q, %v", m.msg, src.delegated)
	}
	keyPress(m, "D")
	act(m, src, "y")
	if len(src.delegated) != 1 || src.delegated[0] != "alpha T2" {
		t.Fatalf("delegated %v", src.delegated)
	}
	out := screen(m)
	if !strings.Contains(out, "ready · waiting on the coordinator") || !strings.Contains(out, "asked the coordinator to delegate T2") {
		t.Fatalf("after y:\n%s", out)
	}
	keyPress(m, "D")
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
	keyPress(m, "D")
	if _, ok := m.top().(*boardView); !ok || !strings.Contains(m.msg, "T3 is in review") {
		t.Fatalf("d on review: %T, %q", m.top(), m.msg)
	}
	keyPress(m, "down")
	keyPress(m, "down")
	keyPress(m, "enter") // T2, shown
	if out := screen(m); !strings.Contains(out, "D delegate") {
		t.Fatalf("shown task lacks d:\n%s", out)
	}
	keyPress(m, "D")
	act(m, src, "y")
	if len(src.delegated) != 1 || src.delegated[0] != "alpha T2" {
		t.Fatalf("delegated %v", src.delegated)
	}
	if out := screen(m); !strings.Contains(out, "waiting on the coordinator") {
		t.Fatalf("shown task:\n%s", out)
	}
	keyPress(m, "esc")
	if out := screen(m); !strings.Contains(out, "waiting on coordinator") || !strings.Contains(out, "D delegate") {
		t.Fatalf("board:\n%s", out)
	}
}

// TestKeysParity: the Keys tab and the help are the same list, and it
// names every dashboard action and session command.
func TestKeysParity(t *testing.T) {
	_, m := popupData(t)
	keyPress(m, "?")
	help := m.top().(*helpView).box(m).body
	keyPress(m, "esc")
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
		keyPress(m, "right")
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

// needsYouData is popupData with a task in review (T3, its PR merged) and a blocked one (T4) in NEEDS YOU.
func needsYouData(t *testing.T, width int) (*fakeSource, *dash) {
	t.Helper()
	src, m := popupData(t)
	src.board.Tasks = append(src.board.Tasks,
		&tasks.Task{ID: 3, Title: "Check it", Status: tasks.Review, Thread: "t-0008",
			Notes: "Do the thing.\n\nreview (2026-10-05): Check: the bell shows whole"},
		&tasks.Task{ID: 4, Title: "Pick a licence", Status: tasks.Blocked,
			Notes: "blocked (2026-10-04): old\n\nblocked (2026-10-05): which licence, MIT or Apache?"})
	src.reviews = map[int]Review{3: {Check: []string{"Run tm, press t"}, PR: 61, Ship: ShipMerged}}
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
	if out := screen(m); !strings.Contains(out, "A accept · x send back · enter show · esc close") {
		t.Fatalf("review keys:\n%s", out)
	}
	keyPress(m, "A")
	cv, ok := m.top().(*confirmView)
	if !ok || cv.question != "Accept T3? The coordinator marks T3 Check it done." {
		t.Fatalf("a opened %T", m.top())
	}
	keyPress(m, "n")
	if m.msg != "T3 not accepted" || len(src.asked) != 0 {
		t.Fatalf("n: %q, %v", m.msg, src.asked)
	}
	keyPress(m, "A")
	act(m, src, "y")
	if len(src.asked) != 1 || src.asked[0] != "accept alpha T3" {
		t.Fatalf("asked %v", src.asked)
	}
	out := screen(m)
	if !strings.Contains(out, "review · waiting on the coordinator · t-0008") || !strings.Contains(out, "told the coordinator you accept T3; it marks it done") {
		t.Fatalf("after y:\n%s", out)
	}
	for _, k := range []string{"A", "x"} {
		keyPress(m, k)
		if m.top() != m.projectPopupView() || m.msg != "T3 is already waiting on the coordinator to accept it" || len(src.asked) != 1 {
			t.Fatalf("%s again: %T, %q, %v", k, m.top(), m.msg, src.asked)
		}
	}
	// T1 is started: a says why, and the popup stays.
	keyPress(m, "down")
	keyPress(m, "down")
	keyPress(m, "A")
	if m.top() != m.projectPopupView() || m.msg != "T1 is started, not in review; A accepts tasks in review" {
		t.Fatalf("a on started: %T, %q", m.top(), m.msg)
	}
	// Other tabs: a does nothing; only esc closes the popup.
	keyPress(m, "1")
	keyPress(m, "a")
	if m.projectPopupView() == nil {
		t.Fatal("a on the overview closed the popup")
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
// (the coordinator reopens it), A only says it isn't in review.
func TestSendBackDone(t *testing.T) {
	src, m := needsYouData(t, 86+sideDefault)
	src.board.Tasks = append(src.board.Tasks, &tasks.Task{ID: 5, Title: "Shipped", Status: tasks.Done, Thread: "t-0009",
		Notes: "done (2026-10-05): merged (PR #70), by the project's setting"})
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
	keyPress(m, "A")
	if m.msg != "T5 is done, not in review; A accepts tasks in review" {
		t.Fatalf("A on done: %q", m.msg)
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
	for _, want := range []string{"PR #61 merged",
		"How to check", "• Run tm, press t", "• the bell shows whole", "A accept · x send back · esc back"} {
		if !strings.Contains(out, want) {
			t.Errorf("review detail lacks %q:\n%s", want, out)
		}
	}
	keyPress(m, "A")
	act(m, src, "y")
	if out := screen(m); !strings.Contains(out, "waiting on the coordinator to accept it") {
		t.Fatalf("shown task after a:\n%s", out)
	}
	keyPress(m, "esc")
	keyPress(m, "esc")

	for ship, want := range map[string]string{ShipOpen: "PR #61 open, not merged yet",
		ShipMerged: "PR #61 merged", ShipClosed: "PR #61 closed without merging"} {
		if got := ansi.Strip(shipLine(Review{PR: 61, Ship: ship})); got != want {
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
	if !ok || !b.open || !strings.Contains(screen(m), "PR #61 merged") {
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
	if out := screen(m); !strings.Contains(out, "enter show · c coordinator · D delegate · esc back") {
		t.Fatalf("blocked keys:\n%s", out)
	}
	keyPress(m, "enter")
	out := screen(m)
	if !strings.Contains(out, "Blocked on: which licence, MIT or Apache?") || !strings.Contains(out, "c coordinator · D delegate · esc back") {
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
	if f := foot(); !strings.Contains(f, "enter show · A accept · x send back · esc back") {
		t.Fatalf("t list keys:\n%s", f)
	}
	keyPress(m, "A")
	act(m, src, "y")
	if out := screen(m); !strings.Contains(out, "waiting on coordinator") {
		t.Fatalf("t list row:\n%s", out)
	}
	keyPress(m, "enter")
	if f := foot(); !strings.Contains(f, "A accept · x send back · esc back") {
		t.Fatalf("shown task keys:\n%s", f)
	}
	keyPress(m, "esc")
	keyPress(m, "down")
	if f := foot(); !strings.Contains(f, "enter show · c coordinator · D delegate · esc back") {
		t.Fatalf("blocked keys:\n%s", f)
	}
	keyPress(m, "enter")
	if f := foot(); !strings.Contains(f, "c coordinator · D delegate · esc back") {
		t.Fatalf("blocked shown keys:\n%s", f)
	}
	keyPress(m, "esc")
	keyPress(m, "esc")
	m.Update(keyPress(m, "a")())
	keyPress(m, "3")
	if f := foot(); !strings.Contains(f, "A accept · x send back · enter show · esc close") {
		t.Fatalf("Tasks tab keys:\n%s", f)
	}
	if out := screen(m); !strings.Contains(out, "review · waiting on the coordinator ") {
		t.Fatalf("Tasks tab row:\n%s", out)
	}
}

// TestAllProjectsSettings: the , popup's All projects tab writes the
// settings every project follows; a project's Settings tab says which
// of its settings follow them, a change there makes it the project's
// own, and x makes it follow all projects again.
func TestAllProjectsSettings(t *testing.T) {
	src, m := popupData(t)
	keyPress(m, ",")
	keyPress(m, "2")
	if out := screen(m); !strings.Contains(out, "2 All projects") || !strings.Contains(out, "Parallel threads") {
		t.Fatalf("all projects tab:\n%s", out)
	}
	// Pausing, archiving and deleting are each project's own.
	for _, r := range m.top().(*settingsView).tabs[1].rows {
		if r.label == "Paused" || r.label == "Archive" || r.label == "Delete" {
			t.Fatalf("all projects has a %s row", r.label)
		}
	}
	for range 3 {
		keyPress(m, "down")
	}
	act(m, src, "enter") // 10 → 15
	if out := screen(m); !strings.Contains(out, "Parallel threads             15") || strings.Contains(out, "working now") {
		t.Fatalf("all projects' parallel threads:\n%s", out)
	}
	cfg, _ := config.Load()
	if s := must(cfg.Safety("beta")); s.ParallelThreads != 15 {
		t.Fatalf("beta doesn't follow all projects: %+v", s)
	}

	// The project's tab: the value and where it comes from.
	keyPress(m, "esc")
	keyPress(m, "a")
	keyPress(m, "4")
	for range 3 {
		keyPress(m, "down")
	}
	if out := screen(m); !strings.Contains(out, "15 · 1 working now  −  + · all projects") {
		t.Fatalf("project follows all projects:\n%s", out)
	}
	act(m, src, "-")
	if out := screen(m); !strings.Contains(out, "14 · 1 working now  −  +") || strings.Contains(out, "+ · all projects") || !strings.Contains(out, "x follow all projects") {
		t.Fatalf("project's own value:\n%s", out)
	}
	if cfg, _ := config.Load(); must(cfg.Safety("alpha")).ParallelThreads != 14 || must(cfg.Safety("beta")).ParallelThreads != 15 {
		t.Fatal("alpha's own value not saved, or reached beta")
	}

	// All projects names the projects that set their own.
	keyPress(m, "esc")
	keyPress(m, ",")
	keyPress(m, "2")
	if out := screen(m); !strings.Contains(out, "alpha sets its own") {
		t.Fatalf("no own note:\n%s", out)
	}
	keyPress(m, "esc")

	// x: alpha follows all projects again.
	keyPress(m, "a")
	keyPress(m, "4")
	act(m, src, "x")
	if !strings.Contains(m.msg, "already follows all projects") {
		t.Fatalf("x on a following setting: %q", m.msg)
	}
	for range 3 {
		keyPress(m, "down")
	}
	// Only x: delete and backspace aren't its aliases.
	act(m, src, "delete")
	act(m, src, "backspace")
	if cfg, _ := config.Load(); must(cfg.Safety("alpha")).ParallelThreads != 14 {
		t.Fatal("delete or backspace dropped alpha's own value")
	}
	act(m, src, "x")
	if !strings.Contains(m.msg, "alpha follows all projects in parallel threads: 15") {
		t.Fatalf("x message: %q", m.msg)
	}
	if out := screen(m); !strings.Contains(out, "15 · 1 working now  −  + · all projects") {
		t.Fatalf("after x:\n%s", out)
	}
	path, _ := config.Path()
	data, _ := os.ReadFile(path)
	if want := "[defaults]\nparallel_threads = 15\n"; !strings.HasPrefix(string(data), want) || strings.Contains(string(data), "parallel_threads = 14") {
		t.Fatalf("file:\n%s", data)
	}

	// Yolo for all projects asks first, naming the projects it reaches.
	keyPress(m, "esc")
	keyPress(m, ",")
	keyPress(m, "2")
	keyPress(m, "down")
	act(m, src, "enter")
	cv, ok := m.top().(*confirmView)
	if !ok || !strings.Contains(cv.question, "alpha, beta") && !strings.Contains(cv.question, "alpha and beta") {
		t.Fatalf("yolo for all projects didn't ask: %T %+v", m.top(), cv)
	}
	act(m, src, "y")
	if cfg, _ := config.Load(); !must(cfg.Safety("beta")).Yolo {
		t.Fatal("yolo for all projects not saved")
	}
}

// TestMemoryTab: tab 6 shows the project's CONTEXT.md, MEMORY.md and
// memory notes' titles, read-only, as text: a link shows its text only,
// the files' own titles are left out, and no file path shows.
func TestMemoryTab(t *testing.T) {
	src, m := popupData(t)
	src.memory = project.Memory{
		Context: "# Context\n\nThe plan, see [the doc](docs/plan.md).\n\n## Where things stand\n\n- " + strings.Repeat("a long item ", 12) + "end\n",
		Index:   "# Memory\n\n- [Decisions](memory/decisions.md): host choices\n",
		Notes:   []string{"Design decisions"},
	}
	m.Update(keyPress(m, "a")())
	pv := m.top().(*projectView)
	keyPress(m, "6")
	if pv.tab != tabMemory {
		t.Fatalf("6: tab %d", pv.tab)
	}
	out := screen(m)
	for _, want := range []string{"6 Memory", "CONTEXT", "The plan, see the doc.", "Where things stand", "- a long item",
		"  a long item", "MEMORY", "- Decisions: host choices", "NOTES", "- Design decisions", "Read-only", "↑ ↓ scroll"} {
		if !strings.Contains(out, want) {
			t.Errorf("memory tab lacks %q:\n%s", want, out)
		}
	}
	for _, not := range []string{"docs/plan.md", "memory/decisions.md", "# Context", ".md"} {
		if strings.Contains(out, not) {
			t.Errorf("memory tab shows %q:\n%s", not, out)
		}
	}
	keyPress(m, "right")
	if pv.tab != tabOverview {
		t.Fatalf("right from the last tab: %d", pv.tab)
	}
	src.memory = project.Memory{}
	m.Update(m.loadPopup("alpha")())
	keyPress(m, "6")
	if out := screen(m); !strings.Contains(out, "no context yet") || !strings.Contains(out, "no notes yet") {
		t.Fatalf("empty memory:\n%s", out)
	}
}

// TestNoKeyAliases: each key means one thing (docs/SPEC.md §4): h and l
// don't switch the project popup's tabs, n, delete and backspace don't
// add or remove a repository, and = doesn't step a number.
func TestNoKeyAliases(t *testing.T) {
	src, m := popupData(t)
	m.Update(keyPress(m, "a")())
	pv := m.top().(*projectView)
	for _, k := range []string{"l", "h", "n", "delete", "backspace"} {
		keyPress(m, k)
		if m.top() != pv || pv.tab != tabOverview {
			t.Errorf("%s on the overview: %T, tab %d", k, m.top(), pv.tab)
		}
	}
	keyPress(m, "4")
	keyPress(m, "down")
	keyPress(m, "down")
	keyPress(m, "down") // Parallel threads
	run(m, keyPress(m, "="))
	if len(src.settings) != 0 {
		t.Errorf("= stepped a number: %v", src.settings)
	}
	run(m, keyPress(m, "+"))
	if len(src.settings) != 1 {
		t.Errorf("+ didn't step the number: %v", src.settings)
	}
}

// TestListsPage: pgup and pgdown move through every list: the inbox,
// the switcher, the task view and the settings.
func TestListsPage(t *testing.T) {
	src, m := popupData(t)
	src.data.Projects[0].Items = nil
	for i := range 12 {
		src.data.Projects[0].Items = append(src.data.Projects[0].Items, project.Item{ID: fmt.Sprint(i), Kind: "report", Subject: fmt.Sprint("t-", i), Summary: "item"})
	}
	m.setData(src.Load())
	keyPress(m, "i")
	in := m.top().(*inboxView)
	keyPress(m, "pgdown")
	if in.sel != 10 {
		t.Errorf("inbox pgdown: %d", in.sel)
	}
	keyPress(m, "pgup")
	if in.sel != 0 {
		t.Errorf("inbox pgup: %d", in.sel)
	}
	keyPress(m, "esc")
	keyPress(m, "p")
	sw := m.top().(*switchView)
	keyPress(m, "pgdown")
	if sw.sel != len(m.data.Projects)-1 {
		t.Errorf("switcher pgdown: %d", sw.sel)
	}
	keyPress(m, "esc")
	m.Update(keyPress(m, "t")())
	b := m.top().(*boardView)
	keyPress(m, "pgdown")
	if b.sel != len(b.list)-1 {
		t.Errorf("task view pgdown: %d of %d", b.sel, len(b.list))
	}
	keyPress(m, "esc")
	keyPress(m, ",")
	sv := m.top().(*settingsView)
	keyPress(m, "pgdown")
	if sv.tabs[sv.tab].sel == 0 {
		t.Errorf("settings pgdown: %d", sv.tabs[sv.tab].sel)
	}
}

// TestHelpNamesPrefix: the help's header names the prefix key.
func TestHelpNamesPrefix(t *testing.T) {
	_, m := popupData(t)
	keyPress(m, "?")
	if out := screen(m); !strings.Contains(out, "keys · prefix = ctrl+b") {
		t.Fatalf("help header:\n%s", out)
	}
}

// TestSettingsKeysQueueDuringSave: keys pressed while a save is in flight
// wait their turn and build on the saved value; none is dropped.
func TestSettingsKeysQueueDuringSave(t *testing.T) {
	src, m := popupData(t)
	keyPress(m, "a")
	keyPress(m, "4")
	for range 3 {
		keyPress(m, "down")
	}
	run(m, keyPress(m, "enter")) // 10 → 15, saved
	m.setData(src.Load())
	cmd := keyPress(m, "-") // 14, in flight
	if !m.busy || cmd == nil {
		t.Fatal("the first press should start a save")
	}
	for range 3 {
		if c := keyPress(m, "-"); c != nil {
			t.Fatal("a press during a save should wait")
		}
	}
	if len(m.queued) != 3 {
		t.Fatalf("queued %d, want 3", len(m.queued))
	}
	// Each save's end starts the next, until none is left.
	for i := 0; cmd != nil && i < 10; i++ {
		msg := cmd()
		if _, ok := msg.(actionMsg); !ok {
			break
		}
		_, cmd = m.Update(msg)
	}
	m.setData(src.Load())
	if len(m.queued) != 0 {
		t.Fatalf("still queued: %d", len(m.queued))
	}
	if out := screen(m); !strings.Contains(out, "11 · 1 working now") {
		t.Fatalf("parallel threads:\n%s", out)
	}
	cfg, _ := config.Load()
	if s := must(cfg.Safety("alpha")); s.ParallelThreads != 11 {
		t.Fatalf("saved %+v", s)
	}
}

// TestModelsSetting: the Thread models row opens a list; leaving a model
// out saves the rest, the last one can't go, and all projects drops the
// line once every model is allowed again.
func TestModelsSetting(t *testing.T) {
	src, m := popupData(t)
	m.Update(tea.WindowSizeMsg{Width: 160, Height: 60})
	keyPress(m, "a")
	keyPress(m, "4")
	pv := m.top().(*projectView)
	i := slices.IndexFunc(pv.settings.rows, func(r setting) bool { return r.label == "Thread models" })
	if i < 0 {
		t.Fatal("no Thread models row")
	}
	pv.settings.sel = i
	if got := pv.settings.rows[i].value(m); got != "any" {
		t.Fatalf("value %q", got)
	}
	keyPress(m, "enter")
	if _, ok := m.top().(*modelsView); !ok {
		t.Fatalf("enter opened %T", m.top())
	}
	keyPress(m, "j")
	keyPress(m, "j")
	run(m, keyPress(m, "enter")) // haiku out
	if n := len(src.settings); n != 1 || src.settings[0] != "projects.alpha.models=[opus sonnet]" {
		t.Fatalf("settings %v", src.settings)
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if s, _ := cfg.Safety("alpha"); !slices.Equal(s.Models, []string{"opus", "sonnet"}) {
		t.Fatalf("saved %v", s.Models)
	}
	keyPress(m, "k")
	run(m, keyPress(m, "enter")) // sonnet out
	keyPress(m, "k")
	run(m, keyPress(m, "enter")) // the last one stays
	mv := m.top().(*modelsView)
	if mv.err == "" || !slices.Equal(mv.allow, []string{"opus"}) {
		t.Fatalf("last model left out: %q %v", mv.err, mv.allow)
	}
}

// TestTasksTabCompact: the Tasks tab lists steps only under the selected
// active task (the others show n/n), none under done tasks, the done
// group newest first and capped at the newest ten, with m to list all.
func TestTasksTabCompact(t *testing.T) {
	src, m := popupData(t)
	src.board.Tasks[1].Steps = []tasks.Step{{N: 1, Text: "Other step"}}
	for i := range 12 {
		src.board.Tasks = append(src.board.Tasks, &tasks.Task{ID: 20 + i, Title: fmt.Sprintf("Old job %d", 20+i), Status: tasks.Done,
			Updated: fmt.Sprintf("2026-09-%02d", 1+i), Steps: []tasks.Step{{N: 1, Text: "Finished step", Done: true}}})
	}
	m.Update(keyPress(m, "a")())
	pv := m.top().(*projectView)
	keyPress(m, "right")
	keyPress(m, "right")
	out := screen(m)
	if !strings.Contains(out, "✓ Draft") || strings.Contains(out, "Other step") || strings.Contains(out, "Finished step") {
		t.Fatalf("steps only under the selected active task:\n%s", out)
	}
	if !strings.Contains(out, "ready · 0/1") || !strings.Contains(out, "done · 1/1") {
		t.Fatalf("rows keep n/n:\n%s", out)
	}
	if !strings.Contains(out, "… 2 more done") || strings.Contains(out, "Old job 20 ") || !strings.Contains(out, "Old job 31") {
		t.Fatalf("done capped at the newest ten:\n%s", out)
	}
	if strings.Index(out, "Old job 31") > strings.Index(out, "Old job 30") {
		t.Fatalf("done not newest first:\n%s", out)
	}
	keyPress(m, "down")
	if out = screen(m); !strings.Contains(out, "Other step") || strings.Contains(out, "Draft") {
		t.Fatalf("steps follow the selection:\n%s", out)
	}
	keyPress(m, "m")
	if !pv.doneAll || len(pv.tasks()) != 14 {
		t.Fatalf("m: doneAll %v, %d tasks", pv.doneAll, len(pv.tasks()))
	}
	keyPress(m, "m")
	if pv.doneAll || len(pv.tasks()) != 12 {
		t.Fatalf("m again: doneAll %v, %d tasks", pv.doneAll, len(pv.tasks()))
	}
}
