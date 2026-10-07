package e2e

// The mouse (docs/SPEC.md §4): every key has a mouse path. These drive
// the dashboard, its popups and menus, a session's status bar and its
// menu, and taking over a thread, with clicks, double-clicks,
// right-clicks, the wheel and drags only.

import (
	"bytes"
	"encoding/base64"
	"strings"
	"testing"
	"time"
)

// TestSmokeMouseDashboard: the footer's hints are buttons; ? help opens
// the help, a click outside it closes it; ≡ menu lists every action and a click
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
	w.WaitFor("─ Help ─", wait)
	w.WaitFor("On the dashboard", wait)
	// The wheel scrolls the help.
	top := w.Screen()
	w.Wheel(false, 80, 10)
	w.WaitUntil("the help scrolled", wait, func(sc string) bool { return sc != top })
	w.Click(130, 10) // beside the box, which is centred on the whole window
	w.WaitUntil("the help closed", wait, func(sc string) bool { return !strings.Contains(sc, "─ Help ─") })

	w.ClickText("≡ menu", 28)
	w.WaitFor("switch project", wait)
	w.WaitFor("details panel on / off", wait)
	w.Click(130, 2)
	w.WaitUntil("the menu closed", wait, func(sc string) bool { return !strings.Contains(sc, "switch project") })

	x, y := w.TextAt(s1.ID+" ", 1)
	w.RightClick(x, y)
	w.WaitFor("─ "+s1.ID+" ─", wait)
	w.ClickText("attach", 1)
	w.WaitUntil("attached", wait, func(sc string) bool { return lastLine(sc, "prefix+d dashboard") })

	w.ClickText("prefix+d dashboard", 29)
	w.WaitFor("OTHER SESSIONS", wait)

	x, y = w.TextAt(s1.ID+" ", 1)
	w.DoubleClick(x, y)
	w.WaitUntil("attached by a double-click", wait, func(sc string) bool { return lastLine(sc, "prefix+d dashboard") })

	w.ClickText("≡ menu · prefix+d", 29)
	w.WaitFor("keyboard to the sidebar", wait)
	w.ClickText("dashboard", 1) // the menu's first item, above the status bar
	w.WaitFor("OTHER SESSIONS", wait)
	w.ClickText("q quit", 28)
	w.WaitExit(wait)
}

// TestSmokeMouseStatusBar: the status bar has the ≡ menu and prefix+d
// dashboard, no window buttons; a double-click on a shell's pane (which
// doesn't take the mouse) does nothing (zoom is gone); a right-click
// there opens the session's menu, esc closes it; the menu's "keyboard to
// the sidebar" gives the sidebar the keyboard, and a click on the pane
// takes it back.
func TestSmokeMouseStatusBar(t *testing.T) {
	env := New(t)
	s1 := env.StartSize(120, 29, "shell")
	w := env.Window(120, 30)
	w.WaitFor(s1.ID+" ", wait)
	w.Key(Enter)
	w.WaitUntil("attached", wait, func(sc string) bool { return lastLine(sc, "≡ menu · prefix+d dashboard") })
	waitPaneSize(t, env, s1, paneCols(120), 28)

	w.DoubleClick(40, 5)
	w.Quiet(300 * time.Millisecond)
	if sc := w.Screen(); lastLine(sc, "zoom") || len(env.Sessions()) != 1 {
		t.Fatalf("a double-click did something:\n%s", sc)
	}
	assertPaneSize(t, env, s1, paneCols(120), 28)

	// A right-click on the pane: the session's menu; esc closes it.
	w.RightClick(40, 5)
	w.WaitFor("narrower sidebar", wait)
	if sc := w.Screen(); strings.Contains(sc, "split:") || strings.Contains(sc, "zoom") || strings.Contains(sc, "close the pane") {
		t.Fatalf("the menu has split pane items:\n%s", sc)
	}
	w.Key(keyEsc)
	w.WaitUntil("the menu closed", wait, func(sc string) bool { return !strings.Contains(sc, "narrower sidebar") })

	// ≡, then "keyboard to the sidebar"; a click on the pane takes it back.
	w.ClickText("≡ menu · prefix+d", 29)
	w.WaitFor("keyboard to the sidebar", wait)
	w.ClickText("keyboard to the sidebar", 1)
	w.WaitUntil("sidebar focused", wait, func(sc string) bool { return lastLine(sc, "sidebar: ↑ ↓ move") })
	w.Click(60, 5)
	w.WaitUntil("pane focused", wait, func(sc string) bool { return !lastLine(sc, "sidebar:") })

	w.ClickText("prefix+d dashboard", 29)
	w.WaitFor("SESSIONS 1", wait)
	w.Quit()
	w.WaitExit(wait)
}

// TestSmokeMousePopupsAndAttach: a right-click on a project in the
// sidebar opens its menu, which shows its dashboard; the footer's a
// project opens the popup, a click on a tab switches to it and a click on
// a setting changes it; a click outside closes the popup. A right-click
// on the thread's row, then attach, attaches it; a click into it reaches
// the program and tells the coordinator, without asking. In the session,
// the sidebar's right-click menu opens the coordinator.
func TestSmokeMousePopupsAndAttach(t *testing.T) {
	env, projDir, _ := threadEnv(t)
	startThread(t, env, projDir)
	w := env.Window(120, 30)
	w.WaitFor(" ■ demo", wait)

	w.RightClick(4, sideRow(t, w.Screen(), "demo"))
	w.WaitFor("show its dashboard", wait)
	w.ClickText("show its dashboard", 1)
	w.WaitUntil("demo's dashboard", wait, func(sc string) bool {
		return strings.Contains(sc, " demo ──") && !strings.Contains(sc, "show its dashboard")
	})

	w.ClickText("a project", 28)
	w.WaitFor("1 Overview", wait)
	w.ClickText("4 Settings", 1)
	w.WaitFor("Coordinator approves", wait)
	_, y := w.TextAt("Coordinator approves", 1)
	before := strings.Split(w.Screen(), "\n")[y]
	w.ClickText("Coordinator approves", 1) // selects it
	w.ClickText("Coordinator approves", 1) // changes it
	saved(w, "the setting changed", func(sc string) bool { return strings.Split(sc, "\n")[y] != before })
	w.Click(40, 1) // above the popup, which takes the dashboard's width
	w.WaitUntil("the popup closed", wait, func(sc string) bool { return !strings.Contains(sc, "1 Overview") })

	x, y := w.TextAt("│  t-0001 Small fix", 1) // the list's row, not the sidebar's
	w.RightClick(x+3, y)
	w.WaitFor("attach", wait)
	if strings.Contains(w.Screen(), "take over") {
		t.Fatalf("the thread's menu has take over:\n%s", w.Screen())
	}
	w.ClickText("attach", y)
	w.WaitUntil("attached", wait, func(sc string) bool { return lastLine(sc, "demo t-0001") })
	w.Type("x")
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
	w.Quit()
	w.WaitExit(wait)
}

// TestSmokeDragCopies (T72): a drag over a shell's pane (which doesn't
// take the mouse) selects its text, and the release copies it to the
// window's clipboard with OSC 52, as it would over SSH; the program's
// own OSC 52 reaches the window too.
func TestSmokeDragCopies(t *testing.T) {
	env := New(t)
	s1 := env.StartSize(120, 29, "shell")
	w := env.Window(120, 30)
	w.WaitFor(s1.ID+" ", wait)
	w.Key(Enter)
	w.WaitUntil("attached", wait, func(sc string) bool { return lastLine(sc, "≡ menu · prefix+d dashboard") })
	waitPaneSize(t, env, s1, paneCols(120), 28)

	env.Keys(s1, "printf 'mark%s\\n' er\r")
	x, y := w.TextAt("marker", 0)
	w.DragTo(x, y, x+5, y)
	osc52 := func(text string) []byte {
		return []byte("\x1b]52;c;" + base64.StdEncoding.EncodeToString([]byte(text)) + "\a")
	}
	if !Poll(wait, func() bool { return bytes.Contains(w.Raw(), osc52("marker")) }) {
		t.Fatalf("no OSC 52 with \"marker\" in the window's output; screen:\n%s", w.Screen())
	}
	w.WaitUntil("the copied note", wait, func(sc string) bool { return lastLine(sc, "copied 1 line") })

	env.Keys(s1, "printf '\\033]52;c;%s\\a' aGk=\r")
	if !Poll(wait, func() bool { return bytes.Contains(w.Raw(), osc52("hi")) }) {
		t.Fatalf("the program's OSC 52 didn't reach the window; screen:\n%s", w.Screen())
	}
	w.Quit()
	w.WaitExit(wait)
}
