//go:build realclaude

package e2e

// The real-Claude suite (docs/SPEC.md §16.4): the M3 scenarios against
// the installed claude, with Haiku, to catch Claude releases that change
// hooks, screens, the session file or the task tools. It needs a
// logged-in claude, so it runs on demand (`make test-claude`) and weekly
// on a machine with a login, never on GitHub-hosted CI. A run costs a few
// cents.
//
// Drift detection: each scenario checks the hook sequence Claude sent
// against the one the fake agent's script for the same case produces.

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/theclifmeister/terminatr/internal/agent"
)

// claudeEvents lists all the hook events the server received for a session.
func claudeEvents(env *Env, s *Session) []string {
	got, _ := agentEvents(env, s, 0)
	return got
}

// assertSubsequence reports drift: want (from the fake's script) must
// appear in got (from real Claude), in order.
func assertSubsequence(t *testing.T, what string, got, want []string) {
	t.Helper()
	i := 0
	for _, g := range got {
		if i < len(want) && g == want[i] {
			i++
		}
	}
	if i < len(want) {
		t.Errorf("drift in %s: the fake sends %v, Claude sent %v (missing %v); update internal/e2e/fakeagent and claude.toml",
			what, want, got, want[i:])
	}
}

func TestRealVersion(t *testing.T) {
	out, err := exec.Command("claude", "--version").Output()
	if err != nil {
		t.Skip("claude is not on PATH")
	}
	m, _ := agent.Builtin("claude")
	pm, err := agent.ParseManifest(m)
	if err != nil {
		t.Fatal(err)
	}
	v := strings.Fields(string(out))[0]
	if !pm.Tested(v) {
		t.Errorf("claude %s is outside tested_versions %v: the status file is not trusted", v, pm.TestedVersions)
	}
}

// TestRealSession is M3's "Try it" against Claude: prompt, permission
// approve, permission Esc (no hook), session file fields, the screen
// rules, todos and /clear with context re-injection.
func TestRealSession(t *testing.T) {
	env := realAgentEnv(t, "claude", nil)
	s, dir := realStart(t, env, "claude", "haiku")

	env.Prompt(s, "Reply with just the word READY.")
	env.WaitState(s, "working", realWait)
	info := env.WaitState(s, "idle", realWait)

	// The session file has what the manifest reads (status only after the
	// first change of state: a fresh file has none).
	sf := filepath.Join(os.Getenv("HOME"), ".claude", "sessions", strconv.Itoa(info.PID)+".json")
	var file map[string]any
	if b, err := os.ReadFile(sf); err != nil || json.Unmarshal(b, &file) != nil {
		t.Fatalf("session file %s: %v", sf, err)
	}
	for _, k := range []string{"status", "sessionId", "messagingSocketPath", "version"} {
		if _, ok := file[k]; !ok {
			t.Errorf("session file lacks %q: %v", k, file)
		}
	}
	assertSubsequence(t, "a turn", claudeEvents(env, s), []string{"SessionStart", "UserPromptSubmit", "Stop"})

	// Approve a permission dialog.
	env.Prompt(s, "Create a file named a.txt containing hi, using the Write tool. Nothing else.")
	env.WaitState(s, "blocked/permission", realWait)
	if !Poll(5*time.Second, func() bool {
		return strings.Contains(strings.Join(env.Explain(s).Matches, ","), "blocked-permission-dialog")
	}) {
		t.Errorf("the permission dialog rule didn't match: %v\n%s", env.Explain(s).Matches, env.Screen(s))
	}
	env.Keys(s, "1")
	env.WaitState(s, "idle", realWait)
	if _, err := os.Stat(filepath.Join(dir, "a.txt")); err != nil {
		t.Errorf("approved write: %v", err)
	}
	assertSubsequence(t, "permission approve", claudeEvents(env, s), []string{"UserPromptSubmit", "PreToolUse", "PermissionRequest", "PostToolUse", "Stop"})

	// Esc on another: no hook fires, the state still comes back.
	env.Prompt(s, "Create a file named b.txt containing hi, using the Write tool. Nothing else.")
	env.WaitState(s, "blocked/permission", realWait)
	n := len(claudeEvents(env, s))
	time.Sleep(500 * time.Millisecond)
	env.Keys(s, "\x1b")
	env.WaitState(s, "idle", 10*time.Second)
	if got := claudeEvents(env, s); len(got) > n {
		t.Logf("note: Claude now fires %v after an Esc on a dialog", got[n:])
	}
	env.Keys(s, "\x15") // the cancelled prompt is back in the box

	// Todos.
	env.Prompt(s, "Use the TaskCreate tool to create two tasks with subjects alpha and beta, then TaskUpdate alpha to in_progress. Nothing else.")
	env.WaitState(s, "working", realWait)
	env.WaitState(s, "idle", realWait)
	if !Poll(10*time.Second, func() bool { i, _ := env.Info(s); return i.TodosTotal == 2 && i.Current != "" }) {
		i, _ := env.Info(s)
		t.Errorf("todos %d/%d current %q; explain:\n%s", i.TodosDone, i.TodosTotal, i.Current, env.CLI("agent", "explain", s.ID).Stdout)
	}

	// /clear rotates the id and resets the list.
	before, _ := env.Info(s)
	env.Keys(s, "/clear")
	time.Sleep(300 * time.Millisecond)
	env.Keys(s, "\r")
	if !Poll(20*time.Second, func() bool { i, _ := env.Info(s); return i.AgentSID != before.AgentSID && i.TodosTotal == 0 }) {
		t.Errorf("/clear: agent session id or todos unchanged")
	}
	assertSubsequence(t, "/clear", claudeEvents(env, s), []string{"SessionEnd", "SessionStart"})
}

// TestRealThreadAccess: a thread can read the project but not write it,
// interactively and under yolo (docs/SPEC.md §5.2), with the settings
// terminatr generates.
func TestRealThreadAccess(t *testing.T) {
	env := realAgentEnv(t, "claude", nil)
	var p struct{ Slug, Dir string }
	if err := json.Unmarshal([]byte(env.MustCLI("project", "new", "real", "--json")), &p); err != nil {
		t.Fatal(err)
	}
	for _, yolo := range []bool{false, true} {
		args := []string{"--role", "thread", "--project", p.Slug, "--thread", "t-0001"}
		if yolo {
			args = append(args, "--yolo")
		}
		s, _ := realStart(t, env, "claude", "haiku", args...)
		target := filepath.Join(p.Dir, "x.txt")
		env.Prompt(s, "Use the Write tool to create the file "+target+" containing hi. If that is refused, try once with Bash echo. Then stop.")
		env.WaitState(s, "working", realWait)
		Poll(realWait, func() bool {
			i, _ := env.Info(s)
			if i.State == "blocked" && i.Reason == "permission" {
				env.Keys(s, "1") // even approved, the deny rule and sandbox hold
			}
			return i.State == "idle"
		})
		if _, err := os.Stat(target); err == nil {
			t.Fatalf("yolo=%v: the thread wrote into the project folder", yolo)
		}
		env.MustCLI("session", "stop", s.ID)
	}
}

// TestRealFirstLocalRun is M4's "Try it" against Claude: a Claude
// session of the user's own shows on the dashboard and enter attaches; a prompt
// typed there blocks on a permission dialog; Ctrl+B d shows it under NEEDS
// YOU; enter attaches again to approve; the session survives the window.
func TestRealFirstLocalRun(t *testing.T) {
	env := realAgentEnv(t, "claude", nil)
	env.Setenv("ANTHROPIC_MODEL", "haiku") // tm session start passes no --model
	dir := env.Workdir()

	w := env.Window(120, 40)
	w.WaitFor("no sessions", wait)
	s := env.StartAgent("claude", dir)
	w.WaitFor(s.ID+" ", wait)
	w.Key(Enter)
	w.WaitUntil("attached", realWait, func(sc string) bool { return lastLine(sc, `prefix+d dashboard`) })
	if !Poll(realWait, func() bool {
		i, _ := env.Info(s)
		return i.State == "idle" || i.Reason == "trust"
	}) {
		t.Fatalf("claude never came up:\n%s", w.Screen())
	}
	if i, _ := env.Info(s); i.Reason == "trust" {
		time.Sleep(time.Second) // keys within ~0.5 s of the dialog are dropped
		w.Key(keyDown)          // the default is "No, exit"
		time.Sleep(200 * time.Millisecond)
		w.Key(Enter)
	}
	env.WaitState(s, "idle", realWait)
	time.Sleep(time.Second)

	w.Type("Create a file named a.txt containing hi, using the Write tool. Nothing else.")
	time.Sleep(300 * time.Millisecond)
	w.Key(Enter)
	env.WaitState(s, "blocked/permission", realWait)
	w.WaitUntil("blocked in the status bar", wait, func(sc string) bool { return lastLine(sc, "blocked permission") })

	w.Detach()
	w.WaitFor("NEEDS YOU", wait)
	w.Key(Enter)
	w.WaitUntil("attached", wait, func(sc string) bool { return lastLine(sc, `prefix+d dashboard`) })
	time.Sleep(time.Second)
	w.Type("1")
	env.WaitState(s, "idle", realWait)
	if _, err := os.Stat(filepath.Join(dir, "a.txt")); err != nil {
		t.Errorf("approved write: %v", err)
	}
	w.Detach()
	w.WaitUntil("NEEDS YOU gone", wait, func(sc string) bool { return !strings.Contains(sc, "NEEDS YOU") })

	w.CloseWindow()
	env.AssertAlive(s)
	w2 := env.Window(120, 40)
	w2.WaitFor(s.ID, wait)
	w2.Quit()
	w2.WaitExit(wait)
}
