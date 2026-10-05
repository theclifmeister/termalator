package e2e

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/theclifmeister/termilator/internal/emu"
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
	beta, betaDir := newProject(env, "Beta")
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
	w.Type("q")
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
	beta, betaDir := newProject(env, "Beta")
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
			w.Type("q")
			w.WaitExit(wait)
		})
	}
}
