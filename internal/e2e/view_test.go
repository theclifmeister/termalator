package e2e

// Server-owned views (docs/SPEC.md §3.3, Views): every console running
// tm shows view main, so what one console does the others show; tm --own
// keeps a view of its own; the view outlives a server restart.

import (
	"strings"
	"testing"
	"time"
)

// TestSmokeViewsShared: two consoles on view main. Opening a project's
// coordinator in one shows it in both; the sidebar mirrors; the console typed in sizes the panes, and the other
// shows the same frame padded; a console with --own stays apart.
func TestSmokeViewsShared(t *testing.T) {
	env := New(t)
	env.FakeClaude()
	alpha, alphaDir := newProject(env, "Alpha")
	beta, betaDir := newProject(env, "Beta")
	env.Trust(alphaDir, betaDir)

	w1 := env.Window(120, 30)
	w1.WaitFor("▸· beta", wait)
	w2 := env.Window(100, 26)
	w2.WaitFor("▸· beta", wait)

	// w1 opens alpha's coordinator: both consoles show it.
	clickCoordinator(t, w1, alpha)
	for _, w := range []*Window{w1, w2} {
		w.WaitUntil("attached to alpha", agentWait, func(sc string) bool { return lastLine(sc, alpha+" coordinator") })
		w.WaitFor("Fake Claude Code", agentWait)
	}
	a := coordinatorOf(t, env, alpha)
	// Started at w1's size, the window less the sidebar and status bar.
	waitPaneSize(t, env, a, 96, 28)
	// Watching from w2 resized nothing.
	assertPaneSize(t, env, a, 96, 28)

	// Typing in w2 claims the view's size: the pane takes w2's
	// rectangle, and w1 shows the same frame, padded on the right.
	w2.Type("x")
	waitPaneSize(t, env, a, 76, 24)
	w1.WaitUntil("w1 padded", wait, func(sc string) bool {
		lines := strings.Split(sc, "\n")
		for _, l := range lines[:24] {
			if r := []rune(l); len(r) > 100 && strings.TrimSpace(string(r[100:])) != "" {
				return false
			}
		}
		return lastLine(sc, alpha+" coordinator")
	})
	// And typing in w1 claims it back.
	w1.Type("y")
	waitPaneSize(t, env, a, 96, 28)

	// The sidebar: prefix } in w2 widens it in w1 too (24 → 26 columns).
	w2.Prefix("}")
	w1.WaitUntil("a wider sidebar", wait, func(sc string) bool {
		r := []rune(strings.Split(sc, "\n")[0])
		return len(r) > 25 && r[25] == '│' && r[23] != '│'
	})

	// A console of its own: it starts on its dashboard, and what it does
	// stays there.
	w3 := env.Window(100, 26, "--own")
	w3.WaitFor("SESSIONS", wait)
	clickCoordinator(t, w3, beta)
	w3.WaitUntil("w3 on beta", agentWait, func(sc string) bool { return lastLine(sc, beta+" coordinator") })
	for _, w := range []*Window{w1, w2} {
		if sc := w.Screen(); !lastLine(sc, alpha+" coordinator") {
			t.Fatalf("an own view's attach reached main:\n%s", sc)
		}
	}
	w3.Detach()
	w3.WaitFor("SESSIONS", wait)
	if sc := w1.Screen(); !lastLine(sc, alpha+" coordinator") {
		t.Fatalf("an own view's detach reached main:\n%s", sc)
	}
	w3.Type("q")
	w3.WaitExit(wait)

	// Back to the dashboard from w2: w1 follows.
	w2.Detach()
	w1.WaitFor("SESSIONS", wait)
	w2.WaitFor("SESSIONS", wait)
	// Opening beta's coordinator from w2's switcher shows it in w1.
	w2.Type("]")
	w1.WaitUntil("w1 on beta", agentWait, func(sc string) bool { return lastLine(sc, beta+" coordinator") })
	w2.Detach()
	w1.WaitFor("SESSIONS", wait)
	w1.Type("q")
	w1.WaitExit(wait)
	w2.Type("q")
	w2.WaitExit(wait)
}

// TestSmokeViewSurvivesRestart: the view is the server's, kept in
// views.json: after tm server restart a new console opens on the same
// coordinator, with the same sidebar.
func TestSmokeViewSurvivesRestart(t *testing.T) {
	env := New(t)
	env.FakeClaude()
	alpha, alphaDir := newProject(env, "Alpha")
	env.Trust(alphaDir)

	w := env.Window(120, 30)
	w.WaitFor("▾· alpha", wait)
	clickCoordinator(t, w, alpha)
	w.WaitUntil("attached to alpha", agentWait, func(sc string) bool { return lastLine(sc, alpha+" coordinator") })
	a := coordinatorOf(t, env, alpha)
	env.WaitState(a, "idle", agentWait)
	w.Prefix("}")
	w.WaitUntil("a wider sidebar", wait, func(sc string) bool {
		r := []rune(strings.Split(sc, "\n")[0])
		return len(r) > 25 && r[25] == '│'
	})
	if !Poll(wait, func() bool { return strings.Contains(readFile(env.Home, "state/views.json"), `"width": 26`) }) {
		t.Fatalf("views.json: %s", readFile(env.Home, "state/views.json"))
	}
	w.CloseWindow()

	env.MustCLI("server", "restart", "--yes")
	env.WaitState(a, "idle", agentWait)
	w2 := env.Window(120, 30)
	w2.WaitUntil("the coordinator again", agentWait, func(sc string) bool {
		r := []rune(strings.Split(sc, "\n")[0])
		return lastLine(sc, alpha+" coordinator") && len(r) > 25 && r[25] == '│'
	})
	w2.Detach()
	w2.WaitFor("SESSIONS", wait)
	w2.Type("q")
	w2.WaitExit(wait)
}

// TestSmokeFirstViewFills: a pane no console has sized yet fills the
// first console that shows it (docs/SPEC.md §3.3), a coordinator and a
// watch-only thread alike; a console showing it later doesn't resize it,
// and once someone types the console typed in sizes it, as before.
func TestSmokeFirstViewFills(t *testing.T) {
	env := New(t)
	env.FakeClaude()
	alpha, alphaDir := newProject(env, "Alpha")
	env.Trust(alphaDir)

	// Both start at 100×30 from the CLI, sized by no console.
	co := env.StartAgent("claude", alphaDir, "--role", "coordinator", "--project", alpha)
	th := env.StartAgent("claude", alphaDir, "--role", "thread", "--project", alpha, "--thread", "t-0001")
	env.WaitState(co, "idle", agentWait)
	env.WaitState(th, "idle", agentWait)

	// The first console to show the coordinator gives it its rectangle:
	// the window less the sidebar and the status bar.
	w1 := env.Window(136, 40)
	w1.WaitFor("▾○ alpha", wait)
	clickCoordinator(t, w1, alpha)
	w1.WaitUntil("attached to alpha", agentWait, func(sc string) bool { return lastLine(sc, alpha+" coordinator") })
	waitPaneSize(t, env, co, 112, 38)

	// A second console showing it later resizes nothing.
	w2 := env.Attach(100, 26, co.ID)
	w2.WaitFor("Fake Claude Code", agentWait)
	time.Sleep(time.Second) // longer than the server's resize quiet time
	assertPaneSize(t, env, co, 112, 38)

	// Typing claims as before: w2's rectangle, then w1's again.
	w2.Type("x")
	waitPaneSize(t, env, co, 76, 24) // less the status bar and the row above it
	w1.Type("y")
	waitPaneSize(t, env, co, 112, 38)

	// The watch-only thread fills its first console too, and the next
	// one to watch it leaves it alone.
	w3 := env.Attach(120, 30, th.ID)
	w3.WaitFor("Fake Claude Code", agentWait)
	waitPaneSize(t, env, th, 96, 28) // the watch-only status bar and the row above it
	w4 := env.Attach(90, 24, th.ID)
	w4.WaitFor("Fake Claude Code", agentWait)
	time.Sleep(time.Second)
	assertPaneSize(t, env, th, 96, 28)
	for _, w := range []*Window{w2, w3, w4} {
		w.Detach()
		w.WaitExit(wait)
	}
}
