package e2e

// M4 scenarios: the dashboard (docs/SPEC.md §4, §15 M4). TestSmoke* run
// on every PR; the rest nightly.

import (
	"encoding/json"
	"regexp"
	"strings"
	"testing"

	"github.com/theclifmeister/termalator/internal/emu"
)

// Dashboard keys.
var (
	keyDown = emu.Key{Special: emu.KeyDown}
	keyUp   = emu.Key{Special: emu.KeyUp}
	keyEsc  = emu.Key{Special: emu.KeyEscape}
)

// dashMasks hide the cwd column's temp paths.
var dashMasks = []Mask{{"tmp", regexp.MustCompile(`/\S*/T/\S*|/tmp/\S*`)}}

// newProject creates a project through the CLI and returns its slug and
// folder.
func newProject(env *Env, name string) (slug, dir string) {
	env.T.Helper()
	var p struct{ Slug, Dir string }
	if err := json.Unmarshal([]byte(env.MustCLI("project", "new", name, "--json")), &p); err != nil {
		env.T.Fatal(err)
	}
	return p.Slug, p.Dir
}

// TestSmokeDashboard: the dashboard's screens, empty, with a project and
// sessions, and with NEEDS YOU; t shows the tasks and d marks a task in
// review done.
func TestSmokeDashboard(t *testing.T) {
	env := New(t)
	w := env.Window(100, 24)
	w.WaitFor("no sessions; s starts a shell", wait)
	Golden(t, w.Screen(), "dashboard-empty.txt")

	slug, _ := newProject(env, "Demo")
	env.MustCLI("task", "add", "Write the README", "--project", slug, "--step", "Draft", "--step", "Review")
	env.MustCLI("task", "add", "Ship it", "--project", slug)
	env.MustCLI("task", "status", "T2", "started", "--project", slug)
	s := env.Start("shell")
	env.WaitFor(s, "$", wait)
	w.WaitFor("0 needs you · 1 in motion · 1 on deck", wait)
	w.WaitFor(s.ID+" ", wait)
	Golden(t, w.Screen(), "dashboard-sessions.txt", dashMasks...)

	env.MustCLI("task", "status", "T1", "review", "--project", slug)
	w.WaitFor("NEEDS YOU", wait)
	w.WaitFor("1 needs you", wait)
	Golden(t, w.Screen(), "dashboard-needs-you.txt", dashMasks...)

	// The task view: T1 first (needs you); d marks it done.
	w.Type("t")
	w.WaitFor("demo tasks", wait)
	w.WaitFor("T1", wait)
	w.Type("d")
	w.WaitFor("T1 done", wait)
	if out := env.MustCLI("task", "show", "T1", "--project", slug, "--json"); !strings.Contains(out, `"status": "done"`) {
		t.Fatalf("T1 after d:\n%s", out)
	}
	w.Key(keyEsc)
	w.WaitUntil("NEEDS YOU gone", wait, func(s string) bool { return !strings.Contains(s, "NEEDS YOU") })
	w.Type("q")
	w.WaitExit(wait)
}

// agentSession waits for the one agent session and returns it.
func agentSession(env *Env) *Session {
	env.T.Helper()
	var s *Session
	Poll(wait, func() bool {
		for _, info := range env.Sessions() {
			if info.Agent != "" {
				s = &Session{ID: info.ID, PID: info.PID}
				return true
			}
		}
		return false
	})
	if s == nil {
		env.T.Fatal("no agent session started")
	}
	env.track(s.PID, "session "+s.ID)
	return s
}

// TestSmokeFirstLocalRun is M4's "Try it" with the fake agent: from the
// dashboard, c starts an agent in a chosen directory and attaches; a
// prompt typed there blocks on a permission dialog; Ctrl+\ shows the
// session under NEEDS YOU; enter attaches again to answer it; closing the
// terminal loses nothing, and a new dashboard shows the session idle.
func TestSmokeFirstLocalRun(t *testing.T) {
	env := New(t)
	env.FakeClaude()
	dir := env.Workdir()
	env.Trust(dir)

	w := env.Window(100, 30)
	w.WaitFor("no sessions", wait)
	w.Type("c")
	w.WaitFor("claude session in directory:", wait)
	w.Key(emu.Key{Rune: 'u', Mods: emu.ModCtrl})
	w.Type(dir)
	w.Key(Enter)
	s := agentSession(env)
	if c, r := paneSize(env, s); c != 100 || r != 29 {
		t.Errorf("new session is %d×%d, want 100×29 (the window less the status bar)", c, r)
	}
	// Attached, with the status bar on the last row.
	w.WaitFor("Fake Claude Code", agentWait)
	w.WaitFor(s.ID+" · claude · ", wait)
	env.WaitState(s, "idle", agentWait)
	w.WaitUntil("idle in the status bar", wait, func(sc string) bool { return lastLine(sc, "idle") })

	w.Type("run permission")
	w.Key(Enter)
	env.WaitState(s, "blocked/permission", agentWait)
	w.WaitUntil("blocked in the status bar", wait, func(sc string) bool { return lastLine(sc, "blocked permission") })

	// Back to the dashboard: the session needs you.
	w.Key(CtrlBackslash)
	w.WaitFor("NEEDS YOU", wait)
	w.WaitFor("▲ blocked   permission", wait)
	env.AssertAlive(s)

	// Attach again (the session's row is still selected) and approve.
	w.Key(Enter)
	w.WaitUntil("attached", wait, func(sc string) bool {
		return lastLine(sc, `ctrl+\ dashboard`) && strings.Contains(sc, "Do you want to create x.txt?")
	})
	w.Type("1")
	env.WaitState(s, "idle", agentWait)
	w.Key(CtrlBackslash)
	w.WaitUntil("NEEDS YOU gone", wait, func(sc string) bool { return !strings.Contains(sc, "NEEDS YOU") && strings.Contains(sc, "idle") })

	// Close the terminal: nothing is lost.
	w.CloseWindow()
	env.AssertAlive(s)
	w2 := env.Window(100, 30)
	w2.WaitFor(s.ID, wait)
	w2.WaitFor("idle", wait)
	w2.Type("q")
	w2.WaitExit(wait)
}

// lastLine reports whether the screen's last non-empty line contains s.
func lastLine(screen, s string) bool {
	lines := strings.Split(strings.TrimRight(screen, "\n "), "\n")
	return len(lines) > 0 && strings.Contains(lines[len(lines)-1], s)
}
