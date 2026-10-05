package e2e

// The mouse (docs/SPEC.md §4): every key has a mouse path. These drive
// the dashboard, its popups and menus, a session's status bar, its menu,
// split panes and their dividers, and taking over a thread, with clicks,
// double-clicks, right-clicks, the wheel and drags only.

import (
	"strings"
	"testing"
)

// TestSmokeMouseDashboard: the footer's hints are buttons; ? help opens
// the help, its × closes it; ≡ menu lists every action and a click
// outside closes it; a right-click on a session's row opens its menu,
// whose attach attaches; the status bar's prefix+d dashboard goes back;
// a double-click on the row attaches again, and the session's ≡ menu
// goes back to the dashboard too.
func TestSmokeMouseDashboard(t *testing.T) {
	env := New(t)
	s1 := env.StartSize(116, 29, "shell")
	w := env.Window(140, 30)
	w.WaitFor(s1.ID+" ", wait)
	w.WaitFor("≡ menu", wait)

	w.ClickText("? help", 28)
	w.WaitFor("keys · prefix =", wait)
	w.WaitFor("On the dashboard", wait)
	// The wheel scrolls the help.
	top := w.Screen()
	w.Wheel(false, 80, 10)
	w.WaitUntil("the help scrolled", wait, func(sc string) bool { return sc != top })
	w.ClickText("×", 1)
	w.WaitUntil("the help closed", wait, func(sc string) bool { return !strings.Contains(sc, "keys · prefix =") })

	w.ClickText("≡ menu", 28)
	w.WaitFor("switch project", wait)
	w.WaitFor("details panel on / off", wait)
	w.Click(130, 2)
	w.WaitUntil("the menu closed", wait, func(sc string) bool { return !strings.Contains(sc, "switch project") })

	x, y := w.TextAt(s1.ID+" ", 1)
	w.RightClick(x, y)
	w.WaitFor("attach  enter", wait)
	w.ClickText("attach  enter", 1)
	w.WaitUntil("attached", wait, func(sc string) bool { return lastLine(sc, "prefix+d dashboard") })

	w.ClickText("prefix+d dashboard", 29)
	w.WaitFor("OTHER SESSIONS", wait)

	x, y = w.TextAt(s1.ID+" ", 1)
	w.DoubleClick(x, y)
	w.WaitUntil("attached by a double-click", wait, func(sc string) bool { return lastLine(sc, "prefix+d dashboard") })

	w.ClickText("≡  prefix+d", 29)
	w.WaitFor("split: a shell beside", wait)
	w.ClickText("dashboard", 1) // the menu's first item, above the status bar
	w.WaitFor("OTHER SESSIONS", wait)
	w.ClickText("q quit", 28)
	w.WaitExit(wait)
}

// TestSmokeMousePanes: the status bar's │ splits beside; dragging the
// divider resizes both panes; a double-click on a shell's pane (which
// doesn't take the mouse) zooms it, and back; a right-click there opens
// the session's menu, esc closes it; × closes the focused pane, leaving
// its session running; ─ splits below, and that divider drags too; ⤢
// zooms.
func TestSmokeMousePanes(t *testing.T) {
	env := New(t)
	s1 := env.StartSize(120, 29, "shell")
	w := env.Window(120, 30)
	w.WaitFor(s1.ID+" ", wait)
	w.Key(Enter)
	w.WaitUntil("attached", wait, func(sc string) bool { return lastLine(sc, `prefix+d dashboard`) })

	w.ClickText("│ ─ ⤢ × ≡", 29)
	w.WaitUntil("two panes", wait, func(sc string) bool { return lastLine(sc, "pane 2/2") })
	s2 := otherSession(t, env, s1)
	waitPaneSize(t, env, s1, 48, 29)
	waitPaneSize(t, env, s2, 47, 29)

	// The divider at column 72, dragged to 60: the left pane gets the 36
	// columns right of the sidebar.
	w.DragTo(72, 10, 60, 10)
	waitPaneSize(t, env, s1, 36, 29)
	waitPaneSize(t, env, s2, 59, 29)

	// A double-click on the left pane focuses and zooms it; again, back.
	w.DoubleClick(30, 5)
	w.WaitUntil("zoomed", wait, func(sc string) bool { return lastLine(sc, "pane 1/2 zoomed") })
	waitPaneSize(t, env, s1, 96, 29)
	w.DoubleClick(30, 5)
	w.WaitUntil("unzoomed", wait, func(sc string) bool { return lastLine(sc, "pane 1/2") && !lastLine(sc, "zoomed") })
	waitPaneSize(t, env, s1, 36, 29)

	// A right-click on it: the session's menu; esc closes it.
	w.RightClick(30, 5)
	w.WaitFor("focus the next pane", wait)
	w.Key(keyEsc)
	w.WaitUntil("the menu closed", wait, func(sc string) bool { return !strings.Contains(sc, "focus the next pane") })

	// × closes the focused (left) pane; its session keeps running.
	w.ClickText("× ≡", 29)
	w.WaitUntil("one pane", wait, func(sc string) bool { return lastLine(sc, s2.ID+" ") && !lastLine(sc, "pane ") })
	waitPaneSize(t, env, s2, 96, 29)
	env.AssertAlive(s1)

	// ─ splits below; the divider under the top pane drags up.
	w.ClickText("─ ⤢ × ≡", 29)
	w.WaitUntil("two panes", wait, func(sc string) bool { return lastLine(sc, "pane 2/2") })
	waitPaneSize(t, env, s2, 96, 14)
	w.DragTo(50, 14, 50, 9)
	waitPaneSize(t, env, s2, 96, 9)

	// ⤢ zooms the focused pane, and back.
	w.ClickText("⤢ × ≡", 29)
	w.WaitUntil("zoomed", wait, func(sc string) bool { return lastLine(sc, "zoomed") })
	w.ClickText("⤢ × ≡", 29)
	w.WaitUntil("unzoomed", wait, func(sc string) bool { return lastLine(sc, "pane 2/2") && !lastLine(sc, "zoomed") })

	w.ClickText("prefix+d dashboard", 29)
	w.WaitFor("SESSIONS 3", wait)
	w.Type("q")
	w.WaitExit(wait)
}

// otherSession is the one session besides s.
func otherSession(t *testing.T, env *Env, s *Session) *Session {
	t.Helper()
	var o *Session
	Poll(wait, func() bool {
		for _, info := range env.Sessions() {
			if info.ID != s.ID {
				o = &Session{ID: info.ID, PID: info.PID}
			}
		}
		return o != nil
	})
	if o == nil {
		t.Fatal("no other session")
	}
	env.track(o.PID, "session "+o.ID)
	return o
}

// TestSmokeMousePopupsAndTakeOver: a right-click on a project in the
// sidebar opens its menu, which shows its dashboard; the footer's a
// project opens the popup, a click on a tab switches to it and a click on
// a setting changes it; a click outside closes the popup. A right-click
// on the thread's row, then take over…, watches it and asks in the status
// bar; a click on y yes takes it over, and the coordinator is told. In
// the session, the sidebar's right-click menu opens the coordinator.
func TestSmokeMousePopupsAndTakeOver(t *testing.T) {
	env, projDir, _ := threadEnv(t)
	startThread(t, env, projDir)
	w := env.Window(120, 30)
	w.WaitFor("▾· demo", wait)

	w.RightClick(4, sideRow(t, w.Screen(), "demo"))
	w.WaitFor("show its dashboard", wait)
	w.ClickText("show its dashboard", 1)
	w.WaitUntil("demo's dashboard", wait, func(sc string) bool { return strings.Contains(sc, "t-0001 Small fix") })

	w.ClickText("a project", 28)
	w.WaitFor("1 Overview", wait)
	w.ClickText("4 Settings", 1)
	w.WaitFor("Coordinator approves", wait)
	_, y := w.TextAt("Coordinator approves", 1)
	before := strings.Split(w.Screen(), "\n")[y]
	w.ClickText("Coordinator approves", 1)
	w.WaitUntil("the setting changed", wait, func(sc string) bool { return strings.Split(sc, "\n")[y] != before })
	w.Click(40, 1) // above the popup, which takes the dashboard's width
	w.WaitUntil("the popup closed", wait, func(sc string) bool { return !strings.Contains(sc, "1 Overview") })

	x, y := w.TextAt("t-0001 Small fix", 1)
	w.RightClick(x, y)
	w.WaitFor("take over…", wait)
	w.ClickText("take over…", 1)
	w.WaitUntil("asked to take over", wait, func(sc string) bool { return lastLine(sc, "take over t-0001 and type into it?") })
	w.ClickText("y yes", 29)
	w.WaitUntil("taken over", wait, func(sc string) bool { return lastLine(sc, "you took over t-0001") })
	waitInbox(t, env, "takeover")

	// In the session, a right-click on the sidebar's coordinator row opens
	// its menu, which starts and attaches the coordinator.
	w.RightClick(4, treeRow(w.Screen(), "demo", "coordinator"))
	w.WaitFor("open the coordinator", wait)
	w.ClickText("open the coordinator", 1)
	w.WaitUntil("on demo's coordinator", agentWait, func(sc string) bool { return lastLine(sc, "demo coordinator") })
	coordinatorOf(t, env, "demo")

	w.ClickText("prefix+d dashboard", 29)
	w.WaitFor("SESSIONS", wait)
	w.Type("q")
	w.WaitExit(wait)
}
