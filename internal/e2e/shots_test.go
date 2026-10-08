package e2e

// TestShots captures every screen, panel and popup of the dashboard and
// the attached session at three window sizes, for the UI review (T93):
// each as plain text (<name>.txt) and as the styled frame tm drew
// (<name>.ansi), into $TM_SHOTS. It checks nothing and runs only when
// TM_SHOTS names a folder.

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/theclifmeister/terminatr/internal/emu"
)

// frame is the window's screen as tm's own renderer would repaint it:
// one cursor move per row, then the cells with their SGR.
func (w *Window) frame() []byte {
	w.mu.Lock()
	defer w.mu.Unlock()
	cols, rows := w.term.Size()
	r, err := emu.NewRenderer(cols, rows)
	if err != nil {
		return nil
	}
	defer r.Close()
	if err := r.Capture(w.term); err != nil {
		return nil
	}
	b, _ := r.Frame(w.term, false)
	return b
}

// shooter saves the shots of one window size.
type shooter struct {
	t    *testing.T
	dir  string
	size string
	w    *Window
	n    int
}

func (s *shooter) shot(name string) {
	s.t.Helper()
	s.w.Quiet(400 * time.Millisecond)
	s.n++
	base := filepath.Join(s.dir, fmt.Sprintf("%s-%02d-%s", s.size, s.n, name))
	os.WriteFile(base+".txt", []byte(s.w.Screen()+"\n"), 0o644)
	os.WriteFile(base+".ansi", s.w.frame(), 0o644)
}

// soft waits for text, logging instead of failing: a shot that misses
// still shows what was there.
func (s *shooter) soft(text string) bool {
	ok := Poll(wait, func() bool { return strings.Contains(s.w.Screen(), text) })
	if !ok {
		s.t.Logf("%s: never saw %q", s.size, text)
	}
	return ok
}

// home closes whatever is open and goes back to the dashboard, so one
// section's miss doesn't spill into the next.
func (s *shooter) home() {
	for range 3 {
		s.w.Key(keyEsc)
		time.Sleep(100 * time.Millisecond)
	}
	s.w.Prefix("d")
	time.Sleep(500 * time.Millisecond)
	s.soft("SESSIONS")
}

func (s *shooter) esc() {
	s.w.Key(keyEsc)
	time.Sleep(150 * time.Millisecond)
}

// at is where text shows at or below row from, or -1, -1.
func (s *shooter) at(text string, from int) (int, int) {
	x, y := -1, -1
	Poll(2*time.Second, func() bool {
		for i, l := range strings.Split(s.w.Screen(), "\n") {
			if j := strings.Index(l, text); i >= from && j >= 0 {
				x, y = len([]rune(l[:j])), i
				return true
			}
		}
		return false
	})
	if y < 0 {
		s.t.Logf("%s: no %q", s.size, text)
	}
	return x, y
}

// clickSoft clicks text if the screen shows it.
func (s *shooter) clickSoft(text string, from int) bool {
	x, y := s.at(text, from)
	if y < 0 {
		return false
	}
	s.w.Click(x, y)
	return true
}

// downTo presses down until the screen shows text, then clicks it
// twice: a click selects a setting, the second changes it.
func (s *shooter) downTo(text string) bool {
	for i := 0; i < 40 && !strings.Contains(s.w.Screen(), text); i++ {
		s.w.Key(keyDown)
		time.Sleep(60 * time.Millisecond)
	}
	if !s.clickSoft(text, 0) {
		return false
	}
	time.Sleep(150 * time.Millisecond)
	return s.clickSoft(text, 0)
}

func TestShots(t *testing.T) {
	dir := os.Getenv("TM_SHOTS")
	if dir == "" {
		t.Skip("set TM_SHOTS to a folder to capture the screens")
	}
	os.MkdirAll(dir, 0o755)
	env, _, _ := tickerEnv(t)
	state := fakeGH(t, env)

	// An empty dashboard first, before the tasks.
	for _, sz := range shotSizes {
		w := env.Window(sz.cols, sz.rows, "--own")
		s := &shooter{t: t, dir: dir, size: sz.name, w: w}
		s.soft("SESSIONS")
		s.shot("dashboard-project-only")
		w.Quit()
		w.WaitExit(wait)
	}

	// The board: a task in every state, threads in several.
	p := func(args ...string) { env.MustCLI(append(args, "--project", "demo")...) }
	p("task", "add", "Fix the login", "--status", "ready", "--step", "Reproduce the bug", "--step", "Fix it", "--step", "Test it")
	p("task", "add", "Write the docs")
	p("task", "add", "Get the API keys", "--status", "blocked")
	p("task", "add", "Ship the release", "--step", "Tag", "--step", "Publish")
	p("task", "add", "Clean up the cache")
	p("task", "add", "Old chore")
	p("task", "status", "T4", "review")
	p("task", "status", "T6", "done")
	p("task", "steps", "T1", "check", "1")
	p("thread", "start", "--task", "T1", "Fix the login")
	th := threadSession(t, env, "demo", "t-0001")
	env.WaitState(th, "idle", agentWait)
	p("thread", "start", "--task", "T5", "Clean up the cache")
	th2 := threadSession(t, env, "demo", "t-0002")
	env.WaitState(th2, "idle", agentWait)
	p("thread", "prompt", "t-0002", "run thread-block")
	env.WaitState(th2, "blocked", agentWait)
	setPR(t, state, `{"number":7,"url":"https://github.com/o/r/pull/7","state":"OPEN","title":"Fix the login","statusCheckRollup":[{"status":"IN_PROGRESS"}]}`)
	os.WriteFile(filepath.Join(env.scriptsDir(), "shot-report.toml"), []byte(`
[[step]]
do = "run"
cmd = 'printf "PR: https://github.com/o/r/pull/7\n\n## Report\nFixed the login redirect.\n\n## Next\nMerge the PR\n" | "$TERMINATR_BIN" report > "$OUT/report" 2>&1'
`), 0o644)
	p("thread", "prompt", "t-0001", "run shot-report")
	p("task", "steps", "T1", "check", "2")
	p("task", "steps", "T5", "add", "Find stale entries")
	// A second project, and a shell of the user's own.
	newProject(env, "website")
	sh := env.Start("shell")
	env.WaitFor(sh, "$", wait)
	own := env.Workdir()
	env.Trust(own)
	ownAgent = env.StartAgent("claude", own).ID
	time.Sleep(2 * time.Second)

	for _, sz := range shotSizes {
		if only := os.Getenv("TM_SHOTS_SIZES"); only != "" && !strings.Contains(only, sz.name) {
			continue
		}
		shootSize(t, env, dir, sz)
	}
}

type shotSize struct {
	name       string
	cols, rows uint16
}

var shotSizes = []shotSize{{"narrow", 80, 24}, {"normal", 120, 36}, {"wide", 200, 50}}

func shootSize(t *testing.T, env *Env, dir string, sz shotSize) {
	w := env.Window(sz.cols, sz.rows, "--own")
	s := &shooter{t: t, dir: dir, size: sz.name, w: w}
	s.soft("SESSIONS")
	s.soft("T1 Fix the login")
	s.shot("dashboard")
	// The list's rows, one by one, for the details panel / footer.
	w.Key(keyDown)
	s.shot("dashboard-row-review")
	for i := 0; i < 3; i++ {
		w.Key(keyDown)
	}
	s.shot("dashboard-row-thread")
	w.Key(keyDown)
	s.shot("dashboard-row-thread2")

	// Help.
	w.Type("?")
	s.soft("On the dashboard")
	s.shot("help")
	w.Key(emu.Key{Special: emu.KeyPageDown})
	s.shot("help-page2")
	s.esc()

	// The menu.
	if s.clickSoft(menuGlyph+" menu", 0) {
		s.shot("menu")
		s.esc()
	}
	// Right-click on a row.
	if x, y := s.at("Fix the login", 1); y >= 0 {
		w.RightClick(x, y)
		s.shot("menu-row")
		s.esc()
	}

	s.home()
	// The project popup, every tab.
	w.Type("a")
	s.soft("1 overview")
	s.shot("popup-overview")
	for i, tab := range []string{"inbox", "tasks", "settings", "keys", "memory"} {
		w.Type(fmt.Sprint(i + 2))
		time.Sleep(200 * time.Millisecond)
		s.shot("popup-" + tab)
	}
	// Overview: add a repository.
	w.Type("1")
	time.Sleep(200 * time.Millisecond)
	w.Type("+")
	time.Sleep(300 * time.Millisecond)
	s.shot("input-add-repo")
	s.esc()
	w.Type("x")
	time.Sleep(300 * time.Millisecond)
	s.shot("confirm-remove-repo")
	s.esc()
	// Settings tab: the lists behind a row (a click on a row is enter).
	w.Type("4")
	time.Sleep(200 * time.Millisecond)
	for _, row := range []struct{ text, name string }{
		{"Thread models", "settings-models"},
		{"Keep history", "settings-history"},
	} {
		if s.downTo(row.text) {
			time.Sleep(300 * time.Millisecond)
			s.shot(row.name)
			s.esc()
		}
	}
	// Tasks tab: T3 (blocked) first, then T4 (review), T1, T5, T2.
	s.home()
	w.Type("a")
	s.soft("1 overview")
	w.Type("3")
	s.soft("T4")
	w.Key(keyDown)
	time.Sleep(200 * time.Millisecond)
	s.shot("popup-tasks-sel")
	w.Key(Enter)
	time.Sleep(300 * time.Millisecond)
	s.shot("task-detail-review")
	s.esc()
	w.Type("A")
	time.Sleep(300 * time.Millisecond)
	s.shot("confirm-accept")
	s.esc()
	w.Type("x")
	time.Sleep(300 * time.Millisecond)
	s.shot("input-send-back")
	s.esc()
	w.Key(keyUp)
	w.Key(Enter)
	time.Sleep(300 * time.Millisecond)
	s.shot("task-detail-blocked")
	s.esc()
	for range 4 {
		w.Key(keyDown)
	}
	w.Key(Enter)
	time.Sleep(300 * time.Millisecond)
	s.shot("task-detail-open")
	s.esc()
	w.Type("D")
	time.Sleep(300 * time.Millisecond)
	s.shot("confirm-delegate")
	s.esc()
	s.esc()

	s.home()
	// t: the project popup on its Tasks tab; inbox, new project.
	w.Type("t")
	s.soft("3 tasks")
	s.shot("tasks")
	w.Key(Enter)
	time.Sleep(300 * time.Millisecond)
	s.shot("tasks-detail")
	s.esc()
	s.esc()
	w.Type("i")
	s.soft("The coordinator handles these")
	s.shot("inbox")
	s.esc()
	w.Type("n")
	time.Sleep(300 * time.Millisecond)
	s.shot("input-new-project")
	s.esc()

	s.home()
	// Settings, both tabs, and their dialogs.
	w.Type(",")
	time.Sleep(300 * time.Millisecond)
	s.shot("settings-general")
	w.Key(Enter) // the first row: the prefix key
	time.Sleep(300 * time.Millisecond)
	s.shot("settings-prefix-capture")
	s.esc()
	w.Key(emu.Key{Special: emu.KeyRight})
	time.Sleep(300 * time.Millisecond)
	s.shot("settings-all-projects")
	if s.downTo("Yolo mode") {
		time.Sleep(300 * time.Millisecond)
		s.shot("confirm-yolo")
		s.esc()
	}
	w.Key(emu.Key{Special: emu.KeyPageDown})
	w.Key(emu.Key{Special: emu.KeyPageDown})
	time.Sleep(300 * time.Millisecond)
	s.shot("settings-all-projects-end")
	s.esc()

	s.home()
	// Adopt an agent session of the user's own.
	if x, y := s.at(ownAgent+" ", 1); y >= 0 {
		w.Click(x, y)
		time.Sleep(200 * time.Millisecond)
		s.shot("dashboard-row-own-agent")
		w.Type("T")
		time.Sleep(300 * time.Millisecond)
		s.shot("confirm-adopt")
		s.esc()
	}

	s.home()
	// Focus areas: the details panel, the sidebar; the slim strip.
	w.Key(emu.Key{Special: emu.KeyTab})
	time.Sleep(200 * time.Millisecond)
	s.shot("focus-next")
	w.Key(emu.Key{Special: emu.KeyTab})
	time.Sleep(200 * time.Millisecond)
	s.shot("focus-next2")
	s.esc()
	w.Type("b")
	time.Sleep(300 * time.Millisecond)
	s.shot("sidebar-slim")
	w.Type("b")
	time.Sleep(300 * time.Millisecond)

	// Attached: the thread with its info panel, its menus.
	attach := func(label string) bool {
		x, y := s.at(label, 1)
		if y < 0 {
			return false
		}
		w.DoubleClick(x+2, y)
		time.Sleep(1500 * time.Millisecond)
		return true
	}
	if attach("T1 Fix the login") {
		s.soft("Fake Claude Code")
		s.shot("attach-thread")
		if x, y := s.at(menuGlyph, int(sz.rows)-1); y >= 0 {
			w.Click(x, y)
			s.shot("attach-menu")
			s.esc()
		}
		w.Prefix("\t")
		time.Sleep(300 * time.Millisecond)
		s.shot("attach-sidebar-focus")
		w.Prefix("\t")
		time.Sleep(300 * time.Millisecond)
		s.shot("attach-panel-focus")
		w.Prefix("\t")
		w.Prefix("a")
		time.Sleep(400 * time.Millisecond)
		s.shot("attach-popup")
		s.esc()
		w.Prefix("?")
		time.Sleep(400 * time.Millisecond)
		s.shot("attach-help")
		s.esc()
	}
	w.Prefix("d")
	time.Sleep(500 * time.Millisecond)
	if attach("T5 Clean up") {
		s.shot("attach-blocked")
	}
	// The coordinator, with its panel.
	w.Prefix("d")
	time.Sleep(500 * time.Millisecond)
	if x, y := s.at(" coordinator", 1); y >= 0 {
		w.DoubleClick(x+3, y)
		time.Sleep(2 * time.Second)
		s.soft("Fake Claude Code")
		s.shot("attach-coordinator")
		w.Prefix("r")
		time.Sleep(400 * time.Millisecond)
		s.shot("confirm-remote")
		s.esc()
	}
	// The shell.
	w.Prefix("d")
	time.Sleep(500 * time.Millisecond)
	if attach("/bin/sh") {
		s.shot("attach-shell")
	}
	w.Prefix("d")
	time.Sleep(500 * time.Millisecond)
	s.shot("back-on-dashboard")
	w.Quit()
	w.WaitExit(wait)
	// A quick look at the raw frames.
	if b, err := os.ReadFile(filepath.Join(dir, sz.name+"-01-dashboard.ansi")); err == nil && !bytes.Contains(b, []byte("\x1b[")) {
		t.Logf("%s: the frame has no escapes", sz.name)
	}
}

const menuGlyph = "≡"

// ownAgent is the agent session outside the projects.
var ownAgent string
