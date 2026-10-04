package e2e

// Server-owned views (docs/SPEC.md §3.3, Views): every console running
// tm shows view main, so what one console does the others show; tm --own
// keeps a view of its own; the view outlives a server restart.

import (
	"strings"
	"testing"
)

// TestSmokeViewsShared: two consoles on view main. Opening a project's
// coordinator in one shows it in both; splits, focus, zoom and the
// sidebar mirror; the console typed in sizes the panes, and the other
// shows the same frame padded; a console with --own stays apart.
func TestSmokeViewsShared(t *testing.T) {
	env := New(t)
	env.FakeClaude()
	alpha, alphaDir := newProject(env, "Alpha")
	beta, betaDir := newProject(env, "Beta")
	env.Trust(alphaDir, betaDir)

	w1 := env.Window(120, 30)
	w1.WaitFor("beta         coordinator", wait)
	w2 := env.Window(100, 26)
	w2.WaitFor("beta         coordinator", wait)

	// w1 opens alpha's coordinator: both consoles show it.
	w1.Click(3, sideRow(t, w1.Screen(), alpha))
	for _, w := range []*Window{w1, w2} {
		w.WaitUntil("attached to alpha", agentWait, func(sc string) bool { return lastLine(sc, alpha+" coordinator") })
		w.WaitFor("Fake Claude Code", agentWait)
	}
	a := coordinatorOf(t, env, alpha)
	// Started at w1's size, the window less the sidebar and status bar.
	waitPaneSize(t, env, a, 96, 29)
	// Watching from w2 resized nothing.
	assertPaneSize(t, env, a, 96, 29)

	// Typing in w2 claims the view's size: the pane takes w2's
	// rectangle, and w1 shows the same frame, padded on the right.
	w2.Type("x")
	waitPaneSize(t, env, a, 76, 25)
	w1.WaitUntil("w1 padded", wait, func(sc string) bool {
		lines := strings.Split(sc, "\n")
		for _, l := range lines[:25] {
			if r := []rune(l); len(r) > 100 && strings.TrimSpace(string(r[100:])) != "" {
				return false
			}
		}
		return lastLine(sc, alpha+" coordinator")
	})
	// And typing in w1 claims it back.
	w1.Type("y")
	waitPaneSize(t, env, a, 96, 29)

	// A split in w1 shows in w2; so do the focus and the zoom from w2.
	w1.Prefix("%")
	for _, w := range []*Window{w1, w2} {
		w.WaitUntil("two panes", wait, func(sc string) bool { return lastLine(sc, "pane 2/2") })
	}
	w2.Prefix("o")
	for _, w := range []*Window{w1, w2} {
		w.WaitUntil("the focus on pane 1", wait, func(sc string) bool { return lastLine(sc, "pane 1/2") })
	}
	w2.Prefix("z")
	for _, w := range []*Window{w1, w2} {
		w.WaitUntil("zoomed", wait, func(sc string) bool { return lastLine(sc, "pane 1/2 zoomed") })
	}
	w2.Prefix("z")
	for _, w := range []*Window{w1, w2} {
		w.WaitUntil("unzoomed", wait, func(sc string) bool { return lastLine(sc, "pane 1/2") && !lastLine(sc, "zoomed") })
	}

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
	w3.Click(3, sideRow(t, w3.Screen(), beta))
	w3.WaitUntil("w3 on beta", agentWait, func(sc string) bool { return lastLine(sc, beta+" coordinator") })
	for _, w := range []*Window{w1, w2} {
		if sc := w.Screen(); !lastLine(sc, alpha+" coordinator") {
			t.Fatalf("an own view's attach reached main:\n%s", sc)
		}
	}
	w3.Detach()
	w3.WaitFor("SESSIONS", wait)
	if sc := w1.Screen(); !lastLine(sc, "pane 1/2") {
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
	w.WaitFor("alpha        coordinator", wait)
	w.Click(3, sideRow(t, w.Screen(), alpha))
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
