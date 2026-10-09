//go:build unix

package e2e

// Codex scenarios (T105): agent sessions under the real codex.toml and
// its Go agent, with the fake agent as Codex 0.160 (FakeCodex). The
// realcodex suite runs the same cases against the installed codex.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// waitHook waits for the fake's n-th hook of event and returns it.
func (e *Env) waitHook(event string, n int) FakeRecord {
	e.T.Helper()
	var found FakeRecord
	if !Poll(agentWait, func() bool {
		seen := 0
		for _, r := range e.FakeRecords("hook") {
			if r.Str("event") == event {
				if seen++; seen == n {
					found = r
					return true
				}
			}
		}
		return false
	}) {
		e.T.Fatalf("no hook %s #%d; hooks %v", event, n, e.HookEvents())
	}
	return found
}

// hookField reads a field of a hook record's payload.
func hookField(r FakeRecord, k string) string {
	p, _ := r["payload"].(map[string]any)
	s, _ := p[k].(string)
	return s
}

// lastPrompt is the fake's latest prompt record.
func (e *Env) lastPrompt() FakeRecord {
	p := e.FakeRecords("prompt")
	if len(p) == 0 {
		return nil
	}
	return p[len(p)-1]
}

// TestSmokeCodexSession is the realcodex suite's session against the
// fake: the first prompt is pasted (no thread id before the first
// hook), later ones go through `codex queue`; an approval approved and
// one cancelled with Esc (the Interrupt hook); a plan-mode question,
// which no hook reports; /clear, after which prompts are pasted until
// the new thread reports itself, and the thread left ends later with a
// SessionEnd that changes nothing; /compact.
func TestSmokeCodexSession(t *testing.T) {
	env := New(t)
	env.FakeCodex()
	env.Setenv("FAKEAGENT_CODEX_UNLOAD_MS", "3000") // after the new thread's first prompt
	dir := env.Workdir()
	s := env.StartAgent("codex", dir)
	info := env.WaitState(s, "idle", agentWait)
	if info.Agent != "codex" || info.AgentSID != "" {
		t.Fatalf("before the first prompt: %+v", info)
	}
	start := env.WaitFake("start", agentWait, nil)
	if start["hooks"] != float64(11) || start["untrusted_hooks"] != float64(0) {
		t.Fatalf("the fake didn't trust tm's hooks: %v", start)
	}

	env.Prompt(s, "hello")
	sst := env.waitHook("SessionStart", 1)
	if hookField(sst, "source") != "startup" {
		t.Errorf("SessionStart %v", sst)
	}
	env.waitHook("Stop", 1)
	info = env.WaitState(s, "idle", agentWait)
	thread := hookField(sst, "session_id")
	if info.AgentSID != thread || env.lastPrompt().Str("via") != "paste" {
		t.Fatalf("thread %q (hooks %q), first prompt %v", info.AgentSID, thread, env.lastPrompt())
	}

	// Through the channel: codex queue.
	env.Prompt(s, "run codex-approval")
	env.WaitState(s, "blocked/permission", agentWait)
	if p := env.lastPrompt(); p.Str("via") != "queue" {
		t.Errorf("second prompt %v", p)
	}
	if !Poll(5*time.Second, func() bool {
		return strings.Contains(strings.Join(env.Explain(s).Matches, ","), "blocked-permission-dialog")
	}) {
		t.Errorf("the approval rule didn't match: %v\n%s", env.Explain(s).Matches, env.Screen(s))
	}
	env.Keys(s, "y")
	env.waitHook("Stop", 2)
	env.WaitState(s, "idle", agentWait)

	// Esc on an approval: Interrupt.
	env.Prompt(s, "run codex-approval")
	env.WaitState(s, "blocked/permission", agentWait)
	time.Sleep(300 * time.Millisecond)
	env.Keys(s, "\x1b")
	env.waitHook("Interrupt", 1)
	env.WaitState(s, "idle", agentWait)

	// A plan-mode question: the screen alone.
	n := len(env.HookEvents())
	env.Prompt(s, "run question")
	env.WaitState(s, "blocked/question", agentWait)
	env.Keys(s, "1")
	env.waitHook("Stop", 3)
	env.WaitState(s, "idle", agentWait)
	if got := env.HookEvents()[n:]; strings.Join(got, ",") != "UserPromptSubmit,Stop" {
		t.Errorf("hooks around the question: %v", got)
	}

	// /clear is pasted; the next prompt too, until SessionStart (clear)
	// gives the new thread's id; then the channel again.
	env.Prompt(s, "/clear")
	env.WaitFake("slash", agentWait, func(r FakeRecord) bool { return r.Str("text") == "/clear" })
	env.WaitState(s, "idle", agentWait)
	env.Prompt(s, "after clear")
	sst = env.waitHook("SessionStart", 2)
	if hookField(sst, "source") != "clear" || hookField(sst, "session_id") == thread {
		t.Errorf("SessionStart after /clear %v", sst)
	}
	env.waitHook("Stop", 4)
	if p := env.lastPrompt(); p.Str("via") != "paste" || p.Str("text") != "after clear" {
		t.Errorf("prompt after /clear %v", p)
	}
	info = env.WaitState(s, "idle", agentWait)
	if info.AgentSID != hookField(sst, "session_id") {
		t.Errorf("thread after /clear %q, want %q", info.AgentSID, hookField(sst, "session_id"))
	}

	// The thread left ends later with its own SessionEnd: the session
	// neither exits nor goes back to that thread.
	end := env.waitHook("SessionEnd", 1)
	if hookField(end, "session_id") != thread || hookField(end, "reason") != "other" {
		t.Errorf("SessionEnd %v", end)
	}
	if !Poll(5*time.Second, func() bool {
		for _, ev := range env.Explain(s).Events {
			if ev.Event == "SessionEnd" && strings.Contains(strings.Join(ev.Signals, ","), "ignored: session "+thread+" was left") {
				return true
			}
		}
		return false
	}) {
		t.Errorf("the left thread's SessionEnd wasn't ignored:\n%s", env.CLI("agent", "explain", s.ID).Stdout)
	}
	if i, _ := env.Info(s); i.State != "idle" || i.AgentSID != info.AgentSID {
		t.Errorf("after the left thread's SessionEnd: %s, thread %q (want %q)\n%s", i.State, i.AgentSID, info.AgentSID,
			env.CLI("agent", "explain", s.ID).Stdout)
	}
	env.Prompt(s, "on the new thread")
	env.waitHook("Stop", 5)
	if p := env.lastPrompt(); p.Str("via") != "queue" {
		t.Errorf("prompt on the new thread %v", p)
	}
	if h := env.FakeRecords("queue-hidden"); len(h) != 0 {
		t.Errorf("a prompt ran on the thread /clear left: %v", h)
	}

	// /compact: PreCompact, same thread; SessionStart (compact) at the
	// next prompt.
	env.Prompt(s, "/compact")
	env.waitHook("PreCompact", 1)
	env.WaitState(s, "idle", agentWait)
	env.Prompt(s, "after compact")
	sst = env.waitHook("SessionStart", 3)
	if hookField(sst, "source") != "compact" || hookField(sst, "session_id") != info.AgentSID {
		t.Errorf("SessionStart after /compact %v", sst)
	}
	env.waitHook("Stop", 6)
	if i := env.WaitState(s, "idle", agentWait); i.AgentSID != info.AgentSID {
		t.Errorf("/compact changed the thread: %q", i.AgentSID)
	}
}

// TestSmokeCodexTypedClear: a /clear typed in the pane by hand (no
// Prompt, so no hook and no slash refusal) still makes the next prompt
// paste into the visible thread, not queue for the one left (T187).
func TestSmokeCodexTypedClear(t *testing.T) {
	env := New(t)
	env.FakeCodex()
	s := env.StartAgent("codex", env.Workdir())
	env.WaitState(s, "idle", agentWait)
	env.Prompt(s, "hello")
	sst := env.waitHook("SessionStart", 1)
	env.waitHook("Stop", 1)
	thread := hookField(sst, "session_id")
	env.WaitState(s, "idle", agentWait)

	env.Keys(s, "/clea")
	env.Keys(s, "x\x7fr")
	env.Keys(s, "\r")
	env.WaitFake("slash", agentWait, func(r FakeRecord) bool { return r.Str("text") == "/clear" })
	env.WaitState(s, "idle", agentWait)
	env.Prompt(s, "after typed clear")
	sst = env.waitHook("SessionStart", 2)
	if hookField(sst, "source") != "clear" || hookField(sst, "session_id") == thread {
		t.Errorf("SessionStart after the typed /clear %v", sst)
	}
	env.waitHook("Stop", 2)
	if p := env.lastPrompt(); p.Str("via") != "paste" || p.Str("text") != "after typed clear" {
		t.Errorf("prompt after the typed /clear %v", p)
	}
	if h := env.FakeRecords("queue-hidden"); len(h) != 0 {
		t.Errorf("a prompt went to the thread /clear left: %v", h)
	}
	env.WaitState(s, "idle", agentWait)
	env.Prompt(s, "on the new thread")
	env.waitHook("Stop", 3)
	if p := env.lastPrompt(); p.Str("via") != "queue" {
		t.Errorf("prompt on the new thread %v", p)
	}
}

// TestSmokeCodexResume: a server restart resumes Codex with `codex
// resume <thread>`, and the thread reports itself again at the next
// prompt (SessionStart resume).
func TestSmokeCodexResume(t *testing.T) {
	env := New(t)
	env.FakeCodex()
	s := env.StartAgent("codex", env.Workdir())
	env.WaitState(s, "idle", agentWait)
	env.Prompt(s, "hello")
	env.waitHook("Stop", 1)
	sid := env.WaitState(s, "idle", agentWait).AgentSID

	env.MustCLI("server", "restart", "--yes")
	if !Poll(agentWait, func() bool {
		st := env.FakeRecords("start")
		return len(st) == 2 && st[1].Str("resume") == sid
	}) {
		t.Fatalf("no codex resume %s: %v", sid, env.FakeRecords("start"))
	}
	env.WaitState(s, "idle", agentWait)
	env.Prompt(s, "again")
	sst := env.waitHook("SessionStart", 2)
	if hookField(sst, "source") != "resume" || hookField(sst, "session_id") != sid {
		t.Fatalf("SessionStart after resume %v", sst)
	}
	env.waitHook("Stop", 2)
	if info := env.WaitState(s, "idle", agentWait); info.AgentSID != sid {
		t.Fatalf("resumed as %q, want %q", info.AgentSID, sid)
	}
	for _, r := range env.FakeRecords("start") {
		for _, a := range r["argv"].([]any) {
			if a == "" {
				t.Errorf("empty argument passed: %v", r["argv"])
			}
		}
	}
}

// TestCodexHooksReview: hooks of the user's own that aren't trusted
// bring up "Hooks need review"; tm answers "Continue without trusting"
// itself (user, 2026-10-07), and its own hooks, trusted for the
// session, still run.
func TestCodexHooksReview(t *testing.T) {
	env := New(t)
	env.FakeCodex()
	env.Setenv("FAKEAGENT_UNTRUSTED_HOOKS", "2")
	s := env.StartAgent("codex", env.Workdir())
	r := env.WaitFake("hooks-review", agentWait, nil)
	if r["choice"] != float64(3) {
		t.Fatalf("tm chose %v on the hooks review", r["choice"])
	}
	env.WaitState(s, "idle", agentWait)
	env.Prompt(s, "hello")
	env.waitHook("Stop", 1)
}

// TestSmokeCodexRoles: a coordinator gets its kickoff in argv, the role
// context at SessionStart, Codex's auto-review (--approve-for-me), so an
// approval never blocks it, and the "tm" profile once the probe says it
// holds; a thread runs in the "tm" permission profile.
func TestSmokeCodexRoles(t *testing.T) {
	env := New(t)
	env.FakeCodex()
	var p struct{ Slug, Dir string }
	if err := json.Unmarshal([]byte(env.MustCLI("project", "new", "demo", "--goal", "first goal", "--json")), &p); err != nil {
		t.Fatal(err)
	}
	s := env.StartAgent("codex", p.Dir, "--role", "coordinator", "--project", p.Slug, "--kickoff", "run codex-approval")
	if r := env.WaitFake("start", agentWait, nil); r["approve_for_me"] != true || r.Str("permissions") != "tm" {
		t.Fatalf("coordinator start %v", r)
	}
	if r := env.WaitFake("sandbox", agentWait, nil); r.Str("verdict") != "unix=ok tcp=refused" {
		t.Fatalf("coordinator probe %v", r)
	}
	r := env.WaitFake("context", agentWait, func(r FakeRecord) bool { return r.Str("source") == "startup" })
	if !strings.Contains(r.Str("text"), "first goal") {
		t.Fatalf("startup context:\n%s", r.Str("text"))
	}
	env.WaitFake("prompt", agentWait, func(r FakeRecord) bool { return r.Str("via") == "kickoff" })
	env.WaitFake("auto-review", agentWait, nil)
	env.waitHook("Stop", 1)
	env.WaitState(s, "idle", agentWait)
	if strings.Contains(strings.Join(env.HookEvents(), ","), "PermissionRequest") {
		t.Errorf("the coordinator was asked: %v", env.HookEvents())
	}
	env.MustCLI("session", "stop", s.ID)
	os.Remove(env.FakeLog())

	th := env.StartAgent("codex", env.Workdir(), "--role", "thread", "--project", p.Slug, "--thread", "t-0001")
	if r := env.WaitFake("start", agentWait, nil); r.Str("permissions") != "tm" || r["approve_for_me"] == true {
		t.Fatalf("thread start %v", r)
	}
	env.WaitState(th, "idle", agentWait)
}

// TestSmokeCodexSandboxFallback: on a Codex where the coordinator's
// profile doesn't hold (the proxy feature renamed, so the network would
// be open; or a -c key refused), the coordinator starts without it, as
// before the profile, and the server's log says why, naming the version.
func TestSmokeCodexSandboxFallback(t *testing.T) {
	for _, mode := range []string{"ignore-proxy", "reject"} {
		t.Run(mode, func(t *testing.T) {
			env := New(t)
			env.FakeCodex()
			env.Setenv("FAKEAGENT_CODEX_SANDBOX", mode)
			var p struct{ Slug, Dir string }
			if err := json.Unmarshal([]byte(env.MustCLI("project", "new", "demo", "--json")), &p); err != nil {
				t.Fatal(err)
			}
			s := env.StartAgent("codex", p.Dir, "--role", "coordinator", "--project", p.Slug)
			r := env.WaitFake("start", agentWait, nil)
			if r["approve_for_me"] != true || r.Str("permissions") != "" {
				t.Fatalf("coordinator start %v", r)
			}
			for _, arg := range r["argv"].([]any) {
				if a := arg.(string); strings.HasPrefix(a, "default_permissions=") || strings.HasPrefix(a, "permissions=") || strings.HasPrefix(a, "features.network_proxy=") {
					t.Errorf("fallback argv has %s", a)
				}
			}
			env.WaitState(s, "idle", agentWait)
			b, _ := os.ReadFile(filepath.Join(env.Home, "logs", "server.log"))
			if !strings.Contains(string(b), "codex 0.160.0: the coordinator's sandbox profile doesn't hold") {
				t.Errorf("server log:\n%s", b)
			}
		})
	}
}
