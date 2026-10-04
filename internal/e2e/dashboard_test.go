package e2e

// M4 scenarios: the dashboard (docs/SPEC.md §4, §15 M4). TestSmoke* run
// on every PR; the rest nightly.

import (
	"encoding/json"
	"regexp"
	"strings"
	"testing"
	"time"

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

// TestSmokeDashboard: the dashboard's screens, empty, and with a project
// and sessions; t shows the tasks, read-only: a task in review waits for
// the coordinator, not in NEEDS YOU, and d does nothing.
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
	w.WaitFor("1 needs you", wait)
	if strings.Contains(w.Screen(), "NEEDS YOU") {
		t.Fatalf("a task in review is in NEEDS YOU:\n%s", w.Screen())
	}

	// The task view: T1 first (needs you); it only shows.
	w.Type("t")
	w.WaitFor("demo tasks", wait)
	w.WaitFor("T1", wait)
	Golden(t, w.Screen(), "dashboard-tasks.txt", dashMasks...)
	w.Type("d")
	w.Quiet(500 * time.Millisecond)
	if out := env.MustCLI("task", "show", "T1", "--project", slug, "--json"); !strings.Contains(out, `"status": "review"`) {
		t.Fatalf("T1 after d:\n%s", out)
	}
	w.Key(keyEsc)
	w.Type("q")
	w.WaitExit(wait)
}

// TestSmokeFirstLocalRun is M4's "Try it" with the fake agent: an agent
// session of the user's own, outside any project, shows on the dashboard
// and enter attaches; a prompt typed there blocks on a permission dialog;
// Ctrl+\ d shows the session under NEEDS YOU; enter attaches again to
// answer it; closing the terminal loses nothing, and a new dashboard
// shows the session idle.
func TestSmokeFirstLocalRun(t *testing.T) {
	env := New(t)
	env.FakeClaude()
	dir := env.Workdir()
	env.Trust(dir)

	w := env.Window(100, 30)
	w.WaitFor("no sessions", wait)
	s := env.StartAgent("claude", dir)
	w.WaitFor(s.ID+" ", wait)
	w.Key(Enter)
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
	w.Detach()
	w.WaitFor("NEEDS YOU", wait)
	w.WaitFor("▲ blocked   permission", wait)
	env.AssertAlive(s)

	// Attach again (the session's row is still selected) and approve.
	w.Key(Enter)
	w.WaitUntil("attached", wait, func(sc string) bool {
		return lastLine(sc, `ctrl+\ d dashboard`) && strings.Contains(sc, "Do you want to create x.txt?")
	})
	w.Type("1")
	env.WaitState(s, "idle", agentWait)
	w.Detach()
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
