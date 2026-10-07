package e2e

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/theclifmeister/terminatr/internal/emu"
)

var (
	keyTab   = emu.Key{Special: emu.KeyTab}
	keyRight = emu.Key{Special: emu.KeyRight}
	keyLeft  = emu.Key{Special: emu.KeyLeft}
	keyEnter = emu.Key{Special: emu.KeyEnter}
)

// TestSmokeSidebarKeys: the sidebar from the keyboard (docs/SPEC.md §4).
// On the dashboard tab gives it the keyboard; ↓ moves, → goes into a
// project and ← back up to it, enter on a thread attaches it. In the session prefix+tab
// gives it the keyboard, esc gives it back, enter on a coordinator
// attaches it. Meanwhile no key reaches the pane.
func TestSmokeSidebarKeys(t *testing.T) {
	env, projDir, _ := threadEnv(t)
	demo := "demo"
	beta, betaDir := newProject(env, "beta")
	env.Trust(betaDir)
	startThread(t, env, projDir)

	w := env.Window(120, 30)
	// beta is current; every project is expanded. The cursor starts on
	// beta.
	w.WaitFor(" ■ "+beta, wait)
	w.WaitUntil("demo's thread", wait, func(sc string) bool { return treeRow(sc, demo, "t-0001 ") >= 0 })
	w.Key(keyTab)
	w.WaitFor("sidebar: ↑ ↓ move", wait)

	// ↓ ↓ past beta's coordinator to demo, → into it (its coordinator),
	// ← back up to demo, → again, ↓ to its thread; enter attaches it.
	for _, k := range []emu.Key{keyDown, keyDown, keyRight, keyLeft, keyRight, keyDown} {
		w.Key(k)
	}
	w.Key(keyEnter)
	w.WaitUntil("on t-0001", wait, func(sc string) bool { return lastLine(sc, demo+" t-0001") })

	// prefix+tab: the sidebar has the keyboard; esc gives it back.
	w.Prefix("\t")
	w.WaitUntil("sidebar focused", wait, func(sc string) bool { return lastLine(sc, "sidebar: ↑ ↓ move") })
	w.Key(keyEsc)
	w.WaitUntil("pane focused", wait, func(sc string) bool { return lastLine(sc, demo+" t-0001") && !lastLine(sc, "sidebar: ↑ ↓ move") })

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
	w.Quit()
	w.WaitExit(wait)
}

// sideThread is demo's thread row in the sidebar: its id, then its
// state glyph and a blank column before the border.
var sideThread = regexp.MustCompile(`t-0001 [^│]*\S │`)

// TestSmokeSidebarIcons: the sidebar's tree in the unicode, ascii and nerd
// icon sets (docs/SPEC.md §4), as the settings file picks them; every
// project expanded, its rows on tree connectors, columns aligned.
func TestSmokeSidebarIcons(t *testing.T) {
	env, projDir, _ := threadEnv(t)
	beta, betaDir := newProject(env, "beta")
	env.Trust(betaDir)
	th := startThread(t, env, projDir)
	for _, set := range []string{"unicode", "ascii", "nerd"} {
		t.Run(set, func(t *testing.T) {
			os.WriteFile(filepath.Join(env.Home, "config.toml"), []byte("[ui]\nicons = \""+set+"\"\n"), 0o600)
			w := env.Window(100, 12)
			w.WaitFor(beta, wait)
			w.WaitUntil("demo's thread", wait, func(sc string) bool {
				return sideThread.MatchString(sc) && strings.Contains(sc, "PROJECTS")
			})
			env.WaitState(th, "idle", agentWait) // its glyph: idle, not caught working
			side := SideCols(100)
			WaitGolden(t, DefaultTimeout, func() string {
				lines := strings.Split(w.Screen(), "\n")
				for i, l := range lines {
					r := []rune(l)
					lines[i] = string(r[:min(side, len(r))])
				}
				return strings.Join(lines, "\n")
			}, "sidebar-"+set+".txt")
			w.Quit()
			w.WaitExit(wait)
		})
	}
}

// TestSmokeSidebarClickFocus: a click gives its area the keyboard
// (docs/SPEC.md §4). A click on a project row in the dashboard's sidebar
// puts its cursor there, so ↓ moves it at once; a click on a sidebar row
// that attaches a session keeps the keyboard in the sidebar, where keys
// don't reach the pane; a click on the pane gives the program the keys.
func TestSmokeSidebarClickFocus(t *testing.T) {
	env, projDir, _ := threadEnv(t)
	demo := "demo"
	beta, betaDir := newProject(env, "beta")
	env.Trust(betaDir)
	startThread(t, env, projDir)

	w := env.Window(120, 30)
	w.WaitFor(" ■ "+beta, wait)
	w.WaitUntil("demo's thread", wait, func(sc string) bool { return treeRow(sc, demo, "t-0001 ") >= 0 })

	// The dashboard: a click on demo, then ↓ ↓ to its thread, enter.
	w.Click(5, projectRow(w.Screen(), demo))
	w.WaitFor("sidebar: ↑ ↓ move", wait)
	w.Key(keyDown)
	w.Key(keyDown)
	w.Key(keyEnter)
	w.WaitUntil("on t-0001", wait, func(sc string) bool { return lastLine(sc, demo+" t-0001") })

	// In the session: a click on demo's coordinator attaches it, and the
	// sidebar keeps the keyboard: typing doesn't reach the pane, ↓ moves
	// to the thread and enter attaches it.
	coordinator := func() {
		w.Click(5, treeRow(w.Screen(), demo, "coordinator"))
		w.WaitUntil("on demo's coordinator, the sidebar focused", agentWait, func(sc string) bool {
			return lastLine(sc, demo+" coordinator") && lastLine(sc, "sidebar: ↑ ↓ move")
		})
	}
	coordinator()
	coordinatorOf(t, env, demo)
	w.Type("zqzq")
	w.Key(keyDown)
	w.Key(keyEnter)
	w.WaitUntil("on t-0001 again", wait, func(sc string) bool { return lastLine(sc, demo+" t-0001") })

	// Back on the coordinator by a click; a click on its pane gives it
	// the keys.
	coordinator()
	w.Click(80, 10)
	w.WaitUntil("pane focused", wait, func(sc string) bool { return !lastLine(sc, "sidebar:") })
	w.Type("hello-click")
	w.WaitFor("hello-click", wait)
	if sc := w.PaneScreen(); strings.Contains(sc, "zqzq") {
		t.Fatalf("keys typed while the sidebar had the keyboard reached the pane:\n%s", sc)
	}
	w.Quit()
	w.WaitExit(wait)
}
