package e2e

import (
	"strings"
	"testing"

	"github.com/theclifmeister/termalator/internal/emu"
)

var (
	keyTab   = emu.Key{Special: emu.KeyTab}
	keyRight = emu.Key{Special: emu.KeyRight}
	keyLeft  = emu.Key{Special: emu.KeyLeft}
	keyEnter = emu.Key{Special: emu.KeyEnter}
)

// TestSmokeSidebarKeys: the sidebar from the keyboard (docs/SPEC.md §4).
// On the dashboard tab gives it the keyboard; ↓ moves, → ← open and close
// a project, enter on a thread watches it. In the session prefix+tab
// gives it the keyboard, esc gives it back, enter on a coordinator
// attaches it. Meanwhile no key reaches the pane.
func TestSmokeSidebarKeys(t *testing.T) {
	env, projDir, _ := threadEnv(t)
	demo := "demo"
	beta, betaDir := newProject(env, "Beta")
	env.Trust(betaDir)
	startThread(t, env, projDir)

	w := env.Window(120, 30)
	// beta is current and open; the cursor starts on it.
	w.WaitFor("▾· "+beta, wait)
	w.WaitFor("▸· "+demo, wait)
	w.Key(keyTab)
	w.WaitFor("sidebar: ↑ ↓ move", wait)

	// ↓ ↓ to demo, → opens it, ← closes it, → again.
	w.Key(keyDown)
	w.Key(keyDown)
	w.Key(keyRight)
	w.WaitUntil("demo open", wait, func(sc string) bool { return treeRow(sc, demo, "Small fix") >= 0 })
	w.Key(keyLeft)
	w.WaitUntil("demo closed", wait, func(sc string) bool { return treeRow(sc, demo, "coordinator") < 0 })
	w.Key(keyRight)
	w.WaitUntil("demo open", wait, func(sc string) bool { return treeRow(sc, demo, "Small fix") >= 0 })

	// ↓ ↓ to its thread; enter watches it.
	w.Key(keyDown)
	w.Key(keyDown)
	w.Key(keyEnter)
	w.WaitUntil("watching t-0001", wait, func(sc string) bool { return lastLine(sc, "watch-only") })

	// prefix+tab: the sidebar has the keyboard; esc gives it back.
	w.Prefix("\t")
	w.WaitUntil("sidebar focused", wait, func(sc string) bool { return lastLine(sc, "sidebar: ↑ ↓ move") })
	w.Key(keyEsc)
	w.WaitUntil("pane focused", wait, func(sc string) bool { return lastLine(sc, "watch-only") })

	// prefix+tab, ↑ to demo's coordinator, enter attaches it.
	w.Prefix("\t")
	w.WaitUntil("sidebar focused", wait, func(sc string) bool { return lastLine(sc, "sidebar: ↑ ↓ move") })
	w.Key(keyUp)
	w.Key(keyEnter)
	w.WaitUntil("on demo's coordinator", agentWait, func(sc string) bool { return lastLine(sc, demo+" coordinator") })
	coordinatorOf(t, env, demo)

	// A shell: what is typed while the sidebar has the keyboard never
	// reaches it.
	w.Detach()
	w.WaitFor("SESSIONS", wait)
	w.Type("s")
	w.WaitUntil("attached", wait, func(sc string) bool { return lastLine(sc, "prefix+d dashboard") && strings.Contains(sc, "$") })
	w.Prefix("\t")
	w.WaitUntil("sidebar focused", wait, func(sc string) bool { return lastLine(sc, "sidebar: ↑ ↓ move") })
	w.Type("echo LEAK")
	w.Paste("echo PASTED")
	w.Key(keyEsc)
	w.WaitUntil("pane focused", wait, func(sc string) bool { return !lastLine(sc, "sidebar:") })
	w.Type("echo done-$((6*7))\r")
	w.WaitFor("done-42", wait)
	if sc := w.PaneScreen(); strings.Contains(sc, "LEAK") || strings.Contains(sc, "PASTED") || strings.Contains(sc, "kecho") {
		t.Fatalf("keys leaked into the pane:\n%s", sc)
	}
	w.Detach()
	w.WaitFor("SESSIONS", wait)
	w.Type("q")
	w.WaitExit(wait)
}
