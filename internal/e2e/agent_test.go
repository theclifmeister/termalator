package e2e

// M3 scenarios: agent sessions under the real claude.toml, with the
// scripted fake agent standing in for Claude Code 2.1.289 (docs/SPEC.md
// §15 M3, §16.3). TestSmoke* run on every PR, the rest nightly.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/theclifmeister/termalator/internal/agent"
)

// TestSmokeAgentPermission is M3's "Try it" 1: start Claude, prompt it,
// approve a permission dialog, and press Esc on another one. The state
// follows every step, and the Esc case (no hook at all) comes back to
// idle through the status file.
func TestSmokeAgentPermission(t *testing.T) {
	env := New(t)
	env.FakeClaude()
	dir := env.Workdir()
	env.Trust(dir)
	s := env.StartAgent("claude", dir)
	info := env.WaitState(s, "idle", agentWait)
	if info.Agent != "claude" || info.AgentSID == "" {
		t.Fatalf("session info %+v", info)
	}
	env.WaitState(s, "idle", agentWait)
	if got := env.Prompt(s, "run permission"); !strings.Contains(got, "queued") {
		t.Fatalf("prompt: %q", got)
	}
	env.WaitState(s, "blocked/permission", agentWait)
	env.Keys(s, "1")
	env.WaitFake("hook", agentWait, func(r FakeRecord) bool { return r.Str("event") == "Stop" })
	env.WaitState(s, "idle", agentWait)

	// Esc on the dialog: Claude fires no hook; the hooks' last word stays
	// "blocked" while the state comes back to idle.
	env.Prompt(s, "run permission")
	env.WaitState(s, "blocked/permission", agentWait)
	n := len(env.HookEvents())
	env.Keys(s, "\x1b")
	info = env.WaitState(s, "idle", agentWait)
	if info.StateSources != "status_file" {
		t.Errorf("sources %q, want status_file", info.StateSources)
	}
	if x := env.Explain(s); x.Hook == nil || x.Hook.State != "blocked" {
		t.Errorf("hooks should still say blocked: %+v", x.Hook)
	}
	time.Sleep(300 * time.Millisecond)
	if got := env.HookEvents(); len(got) != n {
		t.Errorf("Esc fired hooks: %v", got[n:])
	}
}

// noStatusFile drops the [status_file] section: hooks, the transcript
// tail and the screen must cope on their own (an untested Claude).
func noStatusFile(m string) string {
	i := strings.Index(m, "\n[status_file]")
	j := strings.Index(m, "\n# Cross-check when the status file")
	if i < 0 || j < i {
		panic("claude.toml layout changed: no [status_file] section to drop")
	}
	return m[:i] + m[j:]
}

// TestSmokeAgentStaleCases: the spike's three stale cases (s03, s04,
// s15) fire no closing hook. With the status file, and without it (the
// transcript tail and the screen take over), the state comes back.
func TestSmokeAgentStaleCases(t *testing.T) {
	for _, mode := range []string{"status file", "no status file"} {
		t.Run(mode, func(t *testing.T) {
			env := New(t)
			if mode == "no status file" {
				env.FakeClaude(noStatusFile)
			} else {
				env.FakeClaude()
			}
			dir := env.Workdir()
			env.Trust(dir)
			s := env.StartAgent("claude", dir)
			env.WaitState(s, "idle", agentWait)
			for _, c := range []struct{ script, during string }{
				{"permission", "blocked/permission"},
				{"stream-long", "working"},
				{"tool-long", "working"},
			} {
				env.Prompt(s, "run "+c.script)
				env.WaitState(s, c.during, agentWait)
				env.WaitFake("hook", agentWait, func(r FakeRecord) bool {
					return r.Str("event") == "UserPromptSubmit" && promptOf(r) == "run "+c.script
				})
				env.Keys(s, "\x1b")
				info := env.WaitState(s, "idle", agentWait)
				if mode == "no status file" && strings.Contains(info.StateSources, "status_file") {
					t.Fatalf("%s: sources %q", c.script, info.StateSources)
				}
				// After an Esc the cancelled prompt is back in the box;
				// clear it so the next paste goes into an empty box.
				env.Keys(s, "\x15")
			}
		})
	}
}

func promptOf(r FakeRecord) string {
	p, _ := r["payload"].(map[string]any)
	s, _ := p["prompt"].(string)
	return s
}

// TestAgentQuestionAndTrust: AskUserQuestion is blocked/question; the
// trust dialog, which shows before any hook, is blocked/trust from the
// screen alone.
func TestAgentQuestionAndTrust(t *testing.T) {
	env := New(t)
	env.FakeClaude()
	dir := env.Workdir() // not trusted
	s := env.StartAgent("claude", dir)
	info := env.WaitState(s, "blocked/trust", agentWait)
	if info.StateSources != "screen" {
		t.Errorf("trust from %q, want the screen", info.StateSources)
	}
	if n := len(env.HookEvents()); n != 0 {
		t.Fatalf("%d hooks ran before trust", n)
	}
	// The dialog drops keys that arrive soon after it paints: repeat.
	if !Poll(agentWait, func() bool {
		env.Keys(s, "\x1b[B\r")
		return Poll(time.Second, func() bool { i, _ := env.Info(s); return i.State == "idle" })
	}) {
		t.Fatalf("trust dialog never accepted:\n%s", env.Screen(s))
	}

	env.Prompt(s, "run question")
	env.WaitState(s, "blocked/question", agentWait)
	env.Keys(s, "2")
	env.WaitState(s, "idle", agentWait)
}

// TestSmokeAgentBackground: a background subagent keeps the session
// working after the main Stop (counter keyed by agent_id), and the
// prompt-suggestion side agent changes nothing.
func TestSmokeAgentBackground(t *testing.T) {
	for _, mode := range []string{"status file", "no status file"} {
		t.Run(mode, func(t *testing.T) {
			env := New(t)
			if mode == "no status file" {
				env.FakeClaude(noStatusFile)
			} else {
				env.FakeClaude()
			}
			env.Setenv("FAKEAGENT_SUGGEST", "try something")
			dir := env.Workdir()
			env.Trust(dir)
			s := env.StartAgent("claude", dir)
			env.WaitState(s, "idle", agentWait)
			env.Prompt(s, "run subagent")
			env.WaitFake("hook", agentWait, func(r FakeRecord) bool { return r.Str("event") == "Stop" })
			if mode == "no status file" {
				env.WaitState(s, "working/background", agentWait)
			} else {
				env.WaitState(s, "working", agentWait)
			}
			env.WaitFake("hook", agentWait, func(r FakeRecord) bool {
				p, _ := r["payload"].(map[string]any)
				return r.Str("event") == "SubagentStop" && p["agent_type"] != nil
			})
			env.WaitState(s, "idle", agentWait)
		})
	}
}

// TestSmokeAgentTodosAndClear: TaskCreate/TaskUpdate diffs are mirrored
// (s16); /clear rotates the agent's session id, resets the list, is
// recorded for resume, and re-injects context (s07).
func TestSmokeAgentTodosAndClear(t *testing.T) {
	env := New(t)
	env.FakeClaude()
	dir := env.Workdir()
	env.Trust(dir)
	s := env.StartAgent("claude", dir)
	first := env.WaitState(s, "idle", agentWait).AgentSID
	env.Prompt(s, "run todos")
	env.WaitFake("hook", agentWait, func(r FakeRecord) bool { return r.Str("event") == "Stop" })
	info := env.WaitState(s, "idle", agentWait)
	if info.TodosTotal != 3 || info.TodosDone != 1 || info.Current != "Running beta" {
		t.Fatalf("todos %d/%d current %q", info.TodosDone, info.TodosTotal, info.Current)
	}
	if !strings.Contains(env.MustCLI("session", "list"), "1/3 todos ▸ Running beta") {
		t.Errorf("session list: %s", env.MustCLI("session", "list"))
	}

	env.Keys(s, "/clear\r")
	env.WaitFake("context", agentWait, func(r FakeRecord) bool { return r.Str("source") == "clear" })
	var after string
	if !Poll(agentWait, func() bool { i, _ := env.Info(s); after = i.AgentSID; return after != first && i.TodosTotal == 0 }) {
		t.Fatalf("agent session id %q (was %q) or todos not reset", after, first)
	}
	if !Poll(agentWait, func() bool {
		b, _ := os.ReadFile(filepath.Join(env.Home, "state", "sessions.json"))
		return strings.Contains(string(b), after)
	}) {
		t.Fatal("sessions.json lacks the new agent session id")
	}
}

// TestSmokeAgentPromptQueue is M3's "Try it" 2: a prompt sent while the
// agent works is queued and pasted once it is idle again.
func TestSmokeAgentPromptQueue(t *testing.T) {
	env := New(t)
	env.FakeClaude()
	dir := env.Workdir()
	env.Trust(dir)
	s := env.StartAgent("claude", dir)
	env.WaitState(s, "idle", agentWait)
	env.Prompt(s, "run stream-long")
	env.WaitState(s, "working", agentWait)
	env.Prompt(s, "second prompt\nwith two lines")
	if info := env.WaitState(s, "working", agentWait); info.Queued != 1 {
		t.Fatalf("queued %d", info.Queued)
	}
	r := env.WaitFake("prompt", 4*agentWait, func(r FakeRecord) bool { return strings.HasPrefix(r.Str("text"), "second prompt") })
	if r.Str("via") != "paste" || r.Str("text") != "second prompt\nwith two lines" {
		t.Fatalf("delivered %+v", r)
	}
}

// TestSmokeAgentResume is M3's "Try it" 3: tm server restart resumes the
// agent with its latest session id, under the same session id.
func TestSmokeAgentResume(t *testing.T) {
	env := New(t)
	env.FakeClaude()
	dir := env.Workdir()
	env.Trust(dir)
	s := env.StartAgent("claude", dir)
	env.WaitState(s, "idle", agentWait)
	env.Prompt(s, "hello")
	env.WaitFake("hook", agentWait, func(r FakeRecord) bool { return r.Str("event") == "Stop" })
	sid := env.WaitState(s, "idle", agentWait).AgentSID

	// An unprompted session has no conversation to resume: it comes back
	// fresh.
	s2 := env.StartAgent("claude", dir)
	sid2 := env.WaitState(s2, "idle", agentWait).AgentSID

	env.MustCLI("server", "restart", "--yes")
	info := env.WaitState(s, "idle", agentWait)
	if info.AgentSID != sid {
		t.Fatalf("resumed with %q, want %q", info.AgentSID, sid)
	}
	starts := env.FakeRecords("start")
	if r := starts[len(starts)-1]; r.Str("resume") == "" && starts[len(starts)-2].Str("resume") == "" {
		t.Fatalf("no --resume start: %+v", starts)
	}
	if got := env.WaitState(s2, "idle", agentWait).AgentSID; got == sid2 || got == "" {
		t.Fatalf("unprompted session came back with %q (was %q)", got, sid2)
	}
	st := env.MustCLI("server", "status", "--json")
	if !strings.Contains(st, s.ID) {
		t.Errorf("server status doesn't list %s as resumed: %s", s.ID, st)
	}
	for _, r := range env.FakeRecords("start") {
		for _, a := range r["argv"].([]any) {
			if a == "" {
				t.Errorf("empty argument passed: %v", r["argv"])
			}
		}
	}
	for _, r := range env.FakeRecords("picker") {
		t.Fatalf("resume with an empty id: %+v", r)
	}
}

// TestSmokeAgentCoordinatorContext: a coordinator's SessionStart (startup,
// clear, compact) gets the role rules plus `tm context`, fetched fresh
// from the server each time (docs/SPEC.md §7.8).
func TestSmokeAgentCoordinatorContext(t *testing.T) {
	env := New(t)
	env.FakeClaude()
	var p struct{ Slug, Dir string }
	if err := json.Unmarshal([]byte(env.MustCLI("project", "new", "Demo", "--goal", "first goal", "--json")), &p); err != nil {
		t.Fatal(err)
	}
	env.Trust(p.Dir)
	s := env.StartAgent("claude", p.Dir, "--role", "coordinator", "--project", p.Slug, "--kickoff", "hello")
	r := env.WaitFake("context", agentWait, func(r FakeRecord) bool { return r.Str("source") == "startup" })
	for _, want := range []string{"tm skill coordinator", "first goal"} {
		if !strings.Contains(r.Str("text"), want) {
			t.Fatalf("startup context lacks %q:\n%s", want, r.Str("text"))
		}
	}
	env.WaitFake("prompt", agentWait, func(r FakeRecord) bool { return r.Str("via") == "kickoff" && r.Str("text") == "hello" })
	env.WaitState(s, "idle", agentWait)
	env.MustCLI("task", "add", "Second task appears after a clear", "--project", p.Slug)
	env.Prompt(s, "run clear-compact")
	for _, src := range []string{"clear", "compact"} {
		r := env.WaitFake("context", agentWait, func(r FakeRecord) bool { return r.Str("source") == src })
		if !strings.Contains(r.Str("text"), "Second task appears after a clear") {
			t.Fatalf("%s context is not fresh:\n%s", src, r.Str("text"))
		}
	}
	// The role reaches hooks and tm calls inside the session.
	if r := env.FakeRecords("hook")[0]; r.Str("event") != "SessionStart" {
		t.Fatalf("first hook %q", r.Str("event"))
	}
}

// TestSmokeAgentThreadAccess: a thread's generated settings deny writes
// to the project folder (Edit(//…) rule), in normal and yolo mode; the
// fake enforces the settings it is given, so this checks the policy we
// render (the real enforcement is in the realclaude suite).
func TestSmokeAgentThreadAccess(t *testing.T) {
	env := New(t)
	env.FakeClaude()
	var p struct{ Slug, Dir string }
	json.Unmarshal([]byte(env.MustCLI("project", "new", "Demo", "--json")), &p)
	real, _ := filepath.EvalSymlinks(p.Dir)
	env.Setenv("FAKEAGENT_DENY_PATH", real)
	dir := env.Workdir()
	env.Trust(dir)
	for _, yolo := range []bool{false, true} {
		args := []string{"--role", "thread", "--project", p.Slug, "--thread", "t-0001"}
		if yolo {
			args = append(args, "--yolo")
			// Accept the bypass warning once, as a human would.
			b, _ := os.ReadFile(filepath.Join(env.HomeDir(), ".claude.json"))
			var cfg map[string]any
			json.Unmarshal(b, &cfg)
			cfg["bypassPermissionsModeAccepted"] = true
			b, _ = json.Marshal(cfg)
			os.WriteFile(filepath.Join(env.HomeDir(), ".claude.json"), b, 0o600)
		}
		s := env.StartAgent("claude", dir, args...)
		env.WaitState(s, "idle", agentWait)
		env.Prompt(s, "run deny-write")
		r := env.WaitFake("write", agentWait, func(r FakeRecord) bool { return true })
		if r["allowed"] != false {
			t.Fatalf("yolo=%v: write into the project allowed: %+v", yolo, r)
		}
		if _, err := os.Stat(filepath.Join(real, "denied.txt")); err == nil {
			t.Fatal("file written into the project")
		}
		env.MustCLI("session", "stop", s.ID)
		os.Remove(env.FakeLog())
	}
}

// TestAgentIdentifyByProcess: claude started by hand in a shell session
// gets agent state while it runs in the foreground (from its status file
// and the screen; it has no termalator hooks), and loses it after.
func TestAgentIdentifyByProcess(t *testing.T) {
	env := New(t)
	env.FakeClaude()
	shim := t.TempDir()
	if err := os.Symlink(filepath.Join(filepath.Dir(env.Bin), "fakeagent"), filepath.Join(shim, "claude")); err != nil {
		t.Fatal(err)
	}
	env.Trust("/")
	s := env.Start("shell")
	env.Keys(s, shim+"/claude\r")
	info := env.WaitState(s, "idle", agentWait)
	if info.Agent != "claude" || !info.Identified {
		t.Fatalf("info %+v", info)
	}
	env.Keys(s, "run stream-long\r")
	env.WaitState(s, "working", agentWait)
	env.Keys(s, "\x1b")
	env.WaitState(s, "idle", agentWait)
	env.Keys(s, "\x15/exit\r") // Ctrl+U: Esc put the prompt back in the box
	if !Poll(agentWait, func() bool { i, _ := env.Info(s); return i.Agent == "" }) {
		t.Fatal("agent state outlived the agent")
	}
	if n := len(env.HookEvents()); n == 0 {
		t.Log("note: the user's own hooks (none here) are the only ones a hand-started agent runs")
	}
}

// TestAgentCommands: tm agent list/check/reload/explain, with a broken
// user manifest skipped and reported, and an untested version making
// the status file untrusted.
func TestAgentCommands(t *testing.T) {
	env := New(t)
	env.FakeClaude()
	env.Setenv("FAKEAGENT_VERSION", "9.9.9")
	agents := filepath.Join(env.Home, "agents")
	os.WriteFile(filepath.Join(agents, "broken.toml"), []byte("manifest_version = 1\nname = \"broken\"\nbogus = 1\n"), 0o600)
	out := env.MustCLI("agent", "list") // no server yet: read locally
	if !strings.Contains(out, "claude") || !strings.Contains(out, "broken (skipped)") {
		t.Fatalf("agent list:\n%s", out)
	}
	if r := env.CLI("agent", "check", filepath.Join(agents, "broken.toml")); r.Code != 1 || !strings.Contains(r.Stderr, "bogus") {
		t.Fatalf("check broken: %+v", r)
	}
	if out := env.MustCLI("agent", "check", filepath.Join(agents, "claude.toml")); !strings.Contains(out, "claude: ok") {
		t.Fatalf("check: %s", out)
	}
	dir := env.Workdir()
	env.Trust(dir)
	s := env.StartAgent("claude", dir)
	info := env.WaitState(s, "idle", agentWait)
	if strings.Contains(info.StateSources, "status_file") {
		t.Fatalf("an untested version's status file was used: %s", info.StateSources)
	}
	var x agent.Explanation
	if !Poll(agentWait, func() bool { x = env.Explain(s); return strings.Contains(x.StatusErr, "tested_versions") }) {
		t.Fatalf("explain: status error %q", x.StatusErr)
	}
	if info, _ := env.Info(s); strings.Contains(info.StateSources, "status_file") {
		t.Fatalf("an untested version's status file was used: %s", info.StateSources)
	}
	out = env.MustCLI("agent", "explain", s.ID)
	for _, want := range []string{"sources, highest rank first", "SessionStart", "status file"} {
		if !strings.Contains(out, want) {
			t.Fatalf("explain lacks %q:\n%s", want, out)
		}
	}
	os.Remove(filepath.Join(agents, "broken.toml"))
	if r := env.CLI("agent", "reload"); r.Code != 0 || strings.Contains(r.Stdout, "broken") {
		t.Fatalf("reload: %+v", r)
	}
	// Exit: the session's state is exited until it is gone.
	env.Keys(s, "/exit\r")
	if !Poll(agentWait, func() bool { _, ok := env.Info(s); return !ok }) {
		t.Fatal("exited session still listed")
	}
}

// TestSmokeMakeRun: `make run` (scripts/run.sh) opens the dashboard,
// where c starts a Claude session in the current directory (here the
// fake behind a claude shim on PATH) and attaches to it.
func TestSmokeMakeRun(t *testing.T) {
	env := New(t)
	env.FakeClaude()
	shim := t.TempDir()
	os.Symlink(filepath.Join(filepath.Dir(env.Bin), "fakeagent"), filepath.Join(shim, "claude"))
	dir := env.Workdir()
	env.Trust(dir)
	root, err := moduleRoot()
	if err != nil {
		t.Fatal(err)
	}
	env.Vars = append(env.Vars, "PATH="+shim+":"+filepath.Dir(env.Bin)+":/usr/bin:/bin")
	w := env.WindowCmd(100, 30, "/bin/sh", "-c", "cd "+dir+" && exec "+filepath.Join(root, "scripts", "run.sh"))
	w.WaitFor("no sessions; s starts a shell, c an agent", wait)
	w.Type("c")
	w.WaitFor("claude session in directory: /", wait)
	w.Key(Enter)
	w.WaitFor("Fake Claude Code", agentWait)
	w.WaitUntil("idle in the status bar", agentWait, func(sc string) bool { return lastLine(sc, "claude · idle") })
	for _, s := range env.Sessions() {
		env.track(s.PID, "session "+s.ID)
		if real, _ := filepath.EvalSymlinks(dir); s.Cwd != dir && s.Cwd != real {
			t.Errorf("session cwd %s, want %s", s.Cwd, dir)
		}
	}
	w.Detach()
	w.WaitFor("SESSIONS", wait)
	w.Type("q")
	w.WaitExit(wait)
}

// agentWait bounds waits in agent scenarios. Every hook the fake fires
// starts a tm process; a race-built tm can take a second to start on
// some macOS versions, so a race-built run (E2E_RACE=1) waits longer.
var agentWait = func() time.Duration {
	if os.Getenv("E2E_RACE") == "1" {
		return 90 * time.Second
	}
	return wait
}()
