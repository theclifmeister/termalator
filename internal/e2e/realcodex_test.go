//go:build realcodex

package e2e

// The real-Codex suite (T105): the cases TestSmokeCodex* run against
// the fake, against the installed codex with gpt-6-luna, to catch Codex
// releases that change hooks, screens, the rollout or `codex queue`. It
// needs codex logged in with ChatGPT, so it runs on demand only (`make
// test-codex`; user, 2026-10-08: not in the weekly job). A run uses a
// little of the plan's limit (T105: about 310k input tokens, 87% of
// them cached, and 1k output; 5-hour window +1%).
//
// Codex keeps its login, threads and settings in CODEX_HOME (default
// ~/.codex). A fresh CODEX_HOME has no login, and auth.json isn't
// copied: log a dedicated one in once (CODEX_HOME=~/.codex-tm-test
// codex login) and run with it set, to keep the suite's threads out of
// ~/.codex. tm's own launch writes nothing there (trust and hooks are -c
// flags for the run).
//
// Drift detection: each step checks the hooks Codex sent against the
// ones the fake sends for the same case (internal/e2e/fakeagent,
// codex.go), and that the screen rule for each dialog matched.

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/theclifmeister/terminatr/internal/agent"
)

const codexRealWait = 120 * time.Second

// codexRealEnv is an isolated terminatr with the user's real HOME
// (Codex's login lives in ~/.codex) and codex on PATH.
func codexRealEnv(t *testing.T) *Env {
	t.Helper()
	bin, err := exec.LookPath("codex")
	if err != nil {
		t.Skip("codex is not on PATH")
	}
	out, _ := exec.Command(bin, "login", "status").CombinedOutput()
	if !strings.Contains(string(out), "Logged in") {
		t.Skipf("codex is not logged in (CODEX_HOME=%q): log in once with `codex login`", os.Getenv("CODEX_HOME"))
	}
	env := New(t)
	env.Setenv("HOME", os.Getenv("HOME"))
	if h := os.Getenv("CODEX_HOME"); h != "" {
		env.Setenv("CODEX_HOME", h)
	}
	env.Setenv("PATH", filepath.Dir(bin)+":"+filepath.Dir(env.Bin)+":/usr/bin:/bin:/usr/sbin:/sbin")
	return env
}

// codexOnRequest installs the built-in codex manifest with one argument
// more, -a on-request. Codex 0.160's default approval policy for a
// session started this way is granular with sandbox approvals off: an
// escalation is rejected without a dialog (seen live, T105), so there is
// no approval to test without it.
func codexOnRequest(t *testing.T, env *Env) {
	t.Helper()
	b, _ := agent.Builtin("codex")
	const after = `"{{if .Resume}}{{.AgentSID}}{{end}}",` // the resume subcommand comes first
	m := strings.Replace(string(b), after, after+` "-a", "on-request",`, 1)
	if m == string(b) {
		t.Fatal("codex.toml: no resume arguments to put -a after")
	}
	dir := filepath.Join(env.Home, "agents")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "codex.toml"), []byte(m), 0o600); err != nil {
		t.Fatal(err)
	}
}

// codexRealStart starts codex in a fresh folder and waits for its
// composer. tm trusts the folder for the run (-c projects) and answers
// "Hooks need review" itself; a dialog left on screen fails here.
func codexRealStart(t *testing.T, env *Env, args ...string) (*Session, string) {
	t.Helper()
	dir := env.Workdir()
	s := env.StartAgent("codex", dir, append([]string{"--model", "gpt-6-luna"}, args...)...)
	if !Poll(codexRealWait, func() bool {
		i, _ := env.Info(s)
		return i.State == "idle"
	}) {
		t.Fatalf("codex never came up idle:\n%s\nexplain:\n%s", env.Screen(s), env.CLI("agent", "explain", s.ID).Stdout)
	}
	return s, dir
}

// codexEvents lists the hook events the server received for a session
// after seq, and the last seq.
func codexEvents(env *Env, s *Session, after uint64) ([]string, uint64) {
	var out []string
	last := after
	for _, ev := range env.Explain(s).Events {
		if ev.Seq > after {
			out = append(out, ev.Event)
			last = ev.Seq
		}
	}
	return out, last
}

// codexEventsUntil waits until last is among the events after seq (the
// rollout can say idle before the Stop hook is in), then returns them.
func codexEventsUntil(env *Env, s *Session, after uint64, last string) ([]string, uint64) {
	Poll(30*time.Second, func() bool {
		g, _ := codexEvents(env, s, after)
		return strings.Contains(","+strings.Join(g, ",")+",", ","+last+",")
	})
	return codexEvents(env, s, after)
}

// codexDrift reports drift: want (what the fake sends) must appear in
// got (what Codex sent), in order.
func codexDrift(t *testing.T, what string, got, want []string) {
	t.Helper()
	i := 0
	for _, g := range got {
		if i < len(want) && g == want[i] {
			i++
		}
	}
	if i < len(want) {
		t.Errorf("drift in %s: the fake sends %v, Codex sent %v (missing %v); update internal/e2e/fakeagent/codex.go and codex.toml",
			what, want, got, want[i:])
	}
}

// codexRuleMatched waits for a screen rule to match.
func codexRuleMatched(t *testing.T, env *Env, s *Session, rule string) {
	t.Helper()
	if !Poll(10*time.Second, func() bool {
		return strings.Contains(","+strings.Join(env.Explain(s).Matches, ",")+",", ","+rule+",")
	}) {
		t.Errorf("rule %s didn't match: %v\n%s", rule, env.Explain(s).Matches, env.Screen(s))
	}
}

// codexType types a line into the composer and sends it, as a user
// would: for slash commands and plan mode, which `codex queue` skips.
func codexType(env *Env, s *Session, text string) {
	env.Keys(s, text)
	time.Sleep(300 * time.Millisecond)
	env.Keys(s, "\r")
}

func TestRealCodexVersion(t *testing.T) {
	out, err := exec.Command("codex", "--version").Output()
	if err != nil {
		t.Skip("codex is not on PATH")
	}
	m, _ := agent.Builtin("codex")
	pm, err := agent.ParseManifest(m)
	if err != nil {
		t.Fatal(err)
	}
	f := strings.Fields(strings.TrimSpace(string(out)))
	v := f[len(f)-1] // "codex-cli 0.160.0"
	if !pm.Tested(v) {
		t.Errorf("codex %s is outside tested_versions %v", v, pm.TestedVersions)
	}
}

// TestRealCodexSession: a turn (pasted: no thread id before the first
// hook), then through `codex queue`: an approval approved, one
// cancelled with Esc, /compact, /clear (the next prompt pasted, a new
// thread), a question in plan mode, and last the SessionEnd of the
// thread /clear left, which must change nothing.
func TestRealCodexSession(t *testing.T) {
	env := codexRealEnv(t)
	codexOnRequest(t, env)
	s, dir := codexRealStart(t, env)
	if i, _ := env.Info(s); i.AgentSID != "" {
		t.Errorf("a thread id before the first hook: %q", i.AgentSID)
	}

	env.Prompt(s, "Reply with just the word READY.")
	env.WaitState(s, "working", codexRealWait)
	info := env.WaitState(s, "idle", codexRealWait)
	if info.AgentSID == "" {
		t.Fatal("no thread id after the first turn")
	}
	got, seq := codexEventsUntil(env, s, 0, "Stop")
	codexDrift(t, "a turn", got, []string{"SessionStart", "UserPromptSubmit", "Stop"})

	// Approve: through the channel now.
	outside := filepath.Join(filepath.Dir(dir), "outside-a.txt")
	env.Prompt(s, "Create the file "+outside+" containing hi with one shell command, requesting escalated permissions for it. Nothing else.")
	env.WaitState(s, "blocked/permission", codexRealWait)
	codexRuleMatched(t, env, s, "blocked-permission-dialog")
	time.Sleep(500 * time.Millisecond)
	env.Keys(s, "y")
	env.WaitState(s, "idle", codexRealWait)
	if _, err := os.Stat(outside); err != nil {
		t.Errorf("approved command: %v", err)
	}
	got, seq = codexEventsUntil(env, s, seq, "Stop")
	codexDrift(t, "approval", got, []string{"UserPromptSubmit", "PreToolUse", "PermissionRequest", "PostToolUse", "Stop"})

	// Esc on an approval: Interrupt.
	outside = filepath.Join(filepath.Dir(dir), "outside-b.txt")
	env.Prompt(s, "Create the file "+outside+" containing hi with one shell command, requesting escalated permissions for it. Nothing else.")
	env.WaitState(s, "blocked/permission", codexRealWait)
	time.Sleep(500 * time.Millisecond)
	env.Keys(s, "\x1b")
	env.WaitState(s, "idle", 20*time.Second)
	if _, err := os.Stat(outside); err == nil {
		t.Error("the cancelled command ran")
	}
	got, seq = codexEventsUntil(env, s, seq, "Interrupt")
	codexDrift(t, "Esc on an approval", got, []string{"UserPromptSubmit", "PreToolUse", "PermissionRequest", "Interrupt"})

	// /compact: a turn in the rollout, PreCompact, same thread;
	// SessionStart (compact) at the next prompt.
	thread := info.AgentSID
	env.Prompt(s, "/compact")
	if !Poll(codexRealWait, func() bool {
		g, _ := codexEvents(env, s, seq)
		return strings.Contains(strings.Join(g, ","), "PreCompact")
	}) {
		t.Errorf("/compact: no PreCompact\n%s", env.Screen(s))
	}
	time.Sleep(2 * time.Second)
	env.WaitState(s, "idle", codexRealWait)
	env.Prompt(s, "Reply with just the word COMPACTED.")
	env.WaitState(s, "working", codexRealWait)
	env.WaitState(s, "idle", codexRealWait)
	got, seq = codexEventsUntil(env, s, seq, "Stop")
	codexDrift(t, "/compact", got, []string{"PreCompact", "SessionStart", "UserPromptSubmit", "Stop"})
	if i, _ := env.Info(s); i.AgentSID != thread {
		t.Errorf("/compact changed the thread: %q -> %q", thread, i.AgentSID)
	}

	// /clear: the new thread reports itself at its first prompt.
	env.Prompt(s, "/clear")
	env.WaitState(s, "idle", codexRealWait)
	time.Sleep(time.Second)
	env.Prompt(s, "Reply with just the word AGAIN.")
	if !Poll(codexRealWait, func() bool { i, _ := env.Info(s); return i.AgentSID != thread && i.AgentSID != "" }) {
		t.Errorf("/clear: the thread id stayed %q", thread)
	}
	env.WaitState(s, "idle", codexRealWait)
	got, seq = codexEventsUntil(env, s, seq, "Stop")
	codexDrift(t, "/clear", got, []string{"SessionStart", "UserPromptSubmit", "Stop"})
	cleared := time.Now()

	// A question in plan mode: the screen alone. Codex may ask more than
	// one; each gets its first option until the turn ends.
	codexType(env, s, "/plan")
	time.Sleep(time.Second)
	// The model may pick request_user_input_async, which shows no menu
	// (seen live, T105): one more try, naming the tool.
	asked := false
	for _, p := range []string{
		"Plan a tiny change: add a colour setting to a new file. Before planning, ask me with your question tool to pick red, green or blue.",
		"Ask me now with the request_user_input tool (the blocking one, not request_user_input_async) to pick red, green or blue for the setting.",
	} {
		codexType(env, s, p)
		asked = Poll(codexRealWait, func() bool {
			i, _ := env.Info(s)
			g, _ := codexEvents(env, s, seq)
			return i.Reason == "question" || slices.Contains(g, "Stop")
		}) && func() bool { i, _ := env.Info(s); return i.Reason == "question" }()
		if asked {
			break
		}
		_, seq = codexEventsUntil(env, s, seq, "Stop")
		env.WaitState(s, "idle", codexRealWait)
	}
	if !asked {
		t.Fatalf("no question menu in plan mode:\n%s", env.Screen(s))
	}
	codexRuleMatched(t, env, s, "blocked-question")
	asking := func() bool { return slices.Contains(env.Explain(s).Matches, "blocked-question") }
	deadline := time.Now().Add(4 * time.Minute)
	for {
		if got, _ := codexEvents(env, s, seq); slices.Contains(got, "Stop") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the plan never ended:\n%s", env.Screen(s))
		}
		if asking() {
			time.Sleep(500 * time.Millisecond)
			env.Keys(s, "1")
			Poll(10*time.Second, func() bool { return !asking() })
		}
		time.Sleep(time.Second)
	}
	got, seq = codexEventsUntil(env, s, seq, "Stop")
	codexDrift(t, "plan mode", got, []string{"UserPromptSubmit", "Stop"})
	// A plan ends with "Implement this plan?": blocked until answered;
	// "No, stay in Plan mode" closes it.
	if strings.Contains(env.Screen(s), "Implement this plan?") {
		env.WaitState(s, "blocked/question", 20*time.Second)
		codexRuleMatched(t, env, s, "blocked-plan-implement")
		env.Keys(s, "3")
		env.WaitState(s, "idle", 20*time.Second)
	} else {
		t.Logf("note: no \"Implement this plan?\" after the plan:\n%s", env.Screen(s))
	}

	// The thread /clear left ends about a minute later with its own
	// SessionEnd: ignored, and the session doesn't exit.
	if !Poll(time.Until(cleared.Add(3*time.Minute)), func() bool {
		for _, ev := range env.Explain(s).Events {
			if ev.Event == "SessionEnd" && strings.Contains(strings.Join(ev.Signals, ","), "ignored: session "+thread) {
				return true
			}
		}
		return false
	}) {
		t.Logf("note: no SessionEnd for the thread /clear left within 3 minutes; the fake sends one")
	}
	if i, _ := env.Info(s); i.State == "exited" {
		t.Errorf("the session counts as exited while codex runs:\n%s", env.CLI("agent", "explain", s.ID).Stdout)
	}
}

// TestRealCodexResume: a server restart resumes the thread (codex
// resume <id>); it reports itself again at the next prompt.
func TestRealCodexResume(t *testing.T) {
	env := codexRealEnv(t)
	s, _ := codexRealStart(t, env)
	env.Prompt(s, "Reply with just the word READY.")
	env.WaitState(s, "working", codexRealWait)
	thread := env.WaitState(s, "idle", codexRealWait).AgentSID

	env.MustCLI("server", "restart", "--yes")
	env.WaitState(s, "idle", codexRealWait)
	env.Prompt(s, "Reply with just the word RESUMED.")
	env.WaitState(s, "working", codexRealWait)
	info := env.WaitState(s, "idle", codexRealWait)
	if info.AgentSID != thread {
		t.Errorf("resumed as %q, want %q", info.AgentSID, thread)
	}
	if !strings.Contains(strings.Join(info.Argv, " "), "resume "+thread) {
		t.Errorf("not started with resume %s: %q", thread, info.Argv)
	}
	got, _ := codexEventsUntil(env, s, 0, "Stop")
	codexDrift(t, "resume", got, []string{"SessionStart", "UserPromptSubmit", "Stop"})
}
