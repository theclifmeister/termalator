package e2e

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/theclifmeister/terminatr/internal/view"
)

// TestSmokeInfoPanel: the info panel beside a thread's pane (docs/SPEC.md
// §4). Attaching a thread shows its task and steps on the right, the one
// under way marked, and the session gets the columns left; prefix+|
// hides and shows it; a narrow window hides it; dragging its border
// resizes it and ui.json keeps the width; a click on the task opens the
// task view over the session.
func TestSmokeInfoPanel(t *testing.T) {
	env, projDir, _ := threadEnv(t)
	env.MustCLI("task", "add", "Fix the login", "--status", "ready", "--project", "demo")
	env.MustCLI("task", "steps", "T1", "add", "Find the bug", "--project", "demo")
	env.MustCLI("task", "steps", "T1", "add", "Fix it", "--project", "demo")
	env.MustCLI("task", "steps", "T1", "add", "Test it", "--project", "demo")
	env.MustCLI("task", "steps", "T1", "check", "1", "--project", "demo")
	env.MustCLI("thread", "start", "--task", "T1", "Fix the login", "--project", "demo")
	var rec struct{ Session string }
	readTOML(t, filepath.Join(projDir, "threads", "t-0001", "thread.toml"), &rec)
	th := &Session{ID: rec.Session}
	env.WaitState(th, "idle", agentWait)

	const cols = 160
	panel := view.InfoDefault
	w := env.Window(cols, 30)
	w.WaitFor("SESSIONS", wait)
	w.ClickText("T1 Fix the login", 2) // the sidebar row (row 1 has the details' title)
	w.WaitUntil("the panel", wait, func(sc string) bool {
		return strings.Contains(sc, "│ T1 Fix the login") && strings.Contains(sc, "◐ Fix it")
	})
	waitPaneSize(t, env, th, uint16(cols-sideDefault-panel), 28)
	// The panel's columns: the pane shows temporary paths.
	panelOnly := func() string {
		var out []string
		for _, l := range strings.Split(w.Screen(), "\n") {
			r := []rune(l)
			out = append(out, string(r[min(cols-panel, len(r)):]))
		}
		return strings.Join(out, "\n")
	}
	WaitGolden(t, wait, panelOnly, "infopanel-thread", Mask{"worktree", regexp.MustCompile(`worktree  \S+`)})

	// prefix+| hides it, and the pane takes its columns; again shows it.
	w.Prefix("|")
	w.WaitUntil("no panel", wait, func(sc string) bool { return !strings.Contains(sc, "│ T1 Fix the login") })
	waitPaneSize(t, env, th, uint16(cols-sideDefault), 28)
	w.Prefix("|")
	w.WaitFor("│ T1 Fix the login", wait)
	waitPaneSize(t, env, th, uint16(cols-sideDefault-panel), 28)

	// A narrow window hides it: the pane keeps its 60 columns.
	w.Resize(110, 30)
	w.WaitUntil("narrow: no panel", wait, func(sc string) bool { return !strings.Contains(sc, "│ T1 Fix the login") })
	waitPaneSize(t, env, th, uint16(110-sideDefault), 28)
	w.Resize(cols, 30)
	w.WaitFor("│ T1 Fix the login", wait)
	waitPaneSize(t, env, th, uint16(cols-sideDefault-panel), 28)

	// Dragging its border 6 columns left makes it 6 wider, kept in
	// ui.json.
	border := cols - panel
	w.Drag(border, border-6, 5)
	waitPaneSize(t, env, th, uint16(cols-sideDefault-panel-6), 28)
	if !Poll(wait, func() bool {
		return strings.Contains(readFile(env.Home, "ui.json"), fmt.Sprintf(`"width": %d`, panel+6))
	}) {
		t.Fatalf("ui.json after the drag: %s", readFile(env.Home, "ui.json"))
	}

	// A click on the task: the task view over the session; esc comes
	// back to it.
	w.ClickText("T1 Fix the login", 0)
	w.WaitUntil("the task view", wait, func(sc string) bool {
		return strings.Contains(sc, "tm "+th.ID) && strings.Contains(sc, "╭─ Task · demo ") && strings.Contains(sc, "○ 2 Fix it")
	})
	w.Key(keyEsc)
	w.WaitFor("│ T1 Fix the login", wait)
	w.Quit()
	w.WaitExit(wait)
}
