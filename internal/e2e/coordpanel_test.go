package e2e

import (
	"strings"
	"testing"
)

// TestSmokeCoordinatorPanel: the info panel beside a coordinator's pane
// (docs/SPEC.md §4, T90). Attaching the coordinator shows its project on
// the right, as the /tm pane does: what needs you, the threads with
// their state and steps, the tasks on deck; the session gets the columns
// left. prefix+| hides and shows it, a narrow window hides it, a click on
// a thread shows its pane and a click on a task opens the task view.
func TestSmokeCoordinatorPanel(t *testing.T) {
	env, _, _ := threadEnv(t)
	env.MustCLI("task", "add", "Fix the login", "--status", "ready", "--project", "demo")
	env.MustCLI("task", "steps", "T1", "add", "Find the bug", "--project", "demo")
	env.MustCLI("task", "steps", "T1", "add", "Fix it", "--project", "demo")
	env.MustCLI("task", "steps", "T1", "check", "1", "--project", "demo")
	env.MustCLI("task", "add", "Write the docs", "--status", "ready", "--project", "demo")
	env.MustCLI("task", "add", "Get the keys", "--status", "blocked", "--project", "demo")
	env.MustCLI("thread", "start", "--task", "T1", "Fix the login", "--project", "demo")
	th := threadSession(t, env, "demo", "t-0001")
	env.WaitState(th, "idle", agentWait)

	const cols = 160
	w := env.Window(cols, 30)
	w.WaitFor("SESSIONS", wait)
	clickCoordinator(t, w, "demo")
	w.WaitFor("Fake Claude Code", agentWait)
	coord := coordinatorOf(t, env, "demo")
	shown := func(sc string) bool {
		return strings.Contains(sc, "│ NEEDS YOU") && strings.Contains(sc, "│ ON DECK")
	}
	w.WaitUntil("the panel", wait, shown)
	waitPaneSize(t, env, coord, threadCols(cols), 28)
	panel := cols - int(threadCols(cols)) - SideCols(cols)
	// The panel's columns, the inbox handled first: what lands there
	// depends on timing.
	panelOnly := func() string {
		clearInbox(env)
		var out []string
		// The ticker's section (ages, so never the same twice), the
		// blank row before it included, is left out, from its heading to
		// the blank row that ends it; the unit tests cover it.
		skip, dropped := false, 0
		for _, l := range strings.Split(w.Screen(), "\n") {
			r := []rune(l)
			cut := strings.TrimSpace(string(r[min(cols-panel, len(r)):]))
			cut = strings.TrimPrefix(cut, "│ ")
			if strings.HasPrefix(cut, "TICKER") {
				skip = true
				if n := len(out); n > 0 && strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(out[n-1]), "│")) == "" {
					out = out[:n-1]
					dropped++
				}
			}
			if skip {
				skip = cut != "" && cut != "│"
				dropped++
				continue
			}
			out = append(out, string(r[min(cols-panel, len(r)):]))
		}
		// Its rows (its wrapping depends on the ages) are given back as
		// empty ones above the last line, so the screen keeps its height.
		if n := len(out); dropped > 0 && n > 0 {
			pad := strings.Repeat("│\n", dropped)
			return strings.Join(out[:n-1], "\n") + "\n" + pad + out[n-1]
		}
		return strings.Join(out, "\n")
	}
	WaitGolden(t, agentWait, panelOnly, "infopanel-coordinator")

	// prefix+| hides it, and the pane takes its columns; again shows it.
	w.Prefix("|")
	w.WaitUntil("no panel", wait, func(sc string) bool { return !shown(sc) })
	waitPaneSize(t, env, coord, paneCols(cols), 28)
	w.Prefix("|")
	w.WaitUntil("the panel again", wait, shown)
	waitPaneSize(t, env, coord, threadCols(cols), 28)

	// A narrow window hides it: the pane keeps its 60 columns.
	w.Resize(110, 30)
	w.WaitUntil("narrow: no panel", wait, func(sc string) bool { return !shown(sc) })
	waitPaneSize(t, env, coord, threadCols(110), 28)
	w.Resize(cols, 30)
	w.WaitUntil("wide: the panel", wait, shown)

	// A click on the thread's row shows the thread's pane, with its own
	// panel.
	w.ClickText("t-0001 T1", 0)
	w.WaitUntil("on t-0001", wait, func(sc string) bool {
		return lastLine(sc, "demo T1 ·") && strings.Contains(sc, "│ T1 Fix the login")
	})
	// Back on the coordinator, a click on a task on deck opens the task
	// view over the session; esc goes back to the Tasks tab, and esc
	// again to the session.
	w.Click(4, treeRow(w.Screen(), "demo", "coordinator"))
	w.WaitUntil("the coordinator's panel", wait, shown)
	w.ClickText("T2 ○ ready", 0)
	w.WaitUntil("the task view", wait, func(sc string) bool {
		return strings.Contains(sc, "tm "+coord.ID) && strings.Contains(sc, "╭─ Task · demo ")
	})
	w.Key(keyEsc)
	w.WaitFor("enter show · D delegate · esc close", wait)
	w.Key(keyEsc)
	// The panel shows beside the popup too: back means the popup and
	// the dashboard's header are gone.
	w.WaitUntil("back on the coordinator", wait, func(sc string) bool {
		return shown(sc) && !strings.Contains(sc, "╭─") && !strings.Contains(sc, "tm "+coord.ID)
	})
	w.Quit()
	w.WaitExit(wait)
}

// threadSession is the session of a project's thread.
func threadSession(t *testing.T, env *Env, slug, id string) *Session {
	t.Helper()
	var s *Session
	Poll(agentWait, func() bool {
		for _, info := range env.Sessions() {
			if info.Role == "thread" && info.Project == slug && info.Thread == id {
				s = &Session{ID: info.ID, PID: info.PID}
			}
		}
		return s != nil
	})
	if s == nil {
		t.Fatalf("no session for %s's %s", slug, id)
	}
	return s
}

// clearInbox marks every item in demo's inbox handled.
func clearInbox(env *Env) {
	out := env.CLI("inbox", "list", "--project", "demo").Stdout
	for _, l := range strings.Split(strings.TrimSpace(out), "\n") {
		if id, _, ok := strings.Cut(l, "  "); ok {
			env.CLI("inbox", "done", id, "--project", "demo")
		}
	}
}
