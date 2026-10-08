package codex

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"

	"github.com/theclifmeister/terminatr/internal/agent"
	"github.com/theclifmeister/terminatr/internal/guard"
)

func load(t *testing.T) *Agent {
	t.Helper()
	r, err := agent.Load("")
	if err != nil {
		t.Fatal(err)
	}
	a, ok := r.Get("codex")
	if !ok {
		t.Fatal("no codex agent")
	}
	c, ok := a.(*Agent)
	if !ok {
		t.Fatalf("codex is %T, want the Go agent", a)
	}
	if agent.ManifestOf(a) == nil {
		t.Fatal("ManifestOf(codex) is nil")
	}
	return c
}

// TestTrustHash: the hash is the SHA-256 of exactly this text, the
// canonical JSON Codex 0.160 hashes (keys sorted, no spaces, the
// command's quotes escaped).
func TestTrustHash(t *testing.T) {
	const canon = `{"event_name":"pre_tool_use","hooks":[{"async":false,"command":"\"$TERMINATR_BIN\" hook --agent codex","timeout":5,"type":"command"}]}`
	sum := sha256.Sum256([]byte(canon))
	want := "sha256:" + hex.EncodeToString(sum[:])
	got, err := TrustHash("pre_tool_use", HookCommand, 5)
	if err != nil || got != want {
		t.Fatalf("TrustHash = %q %v, want %q", got, err, want)
	}
	// No HTML escaping: serde_json writes <, > and & as they are.
	sum = sha256.Sum256([]byte(`{"event_name":"stop","hooks":[{"async":false,"command":"a<b>&c","timeout":3,"type":"command"}]}`))
	if got, _ := TrustHash("stop", "a<b>&c", 3); got != "sha256:"+hex.EncodeToString(sum[:]) {
		t.Errorf("HTML characters: %q", got)
	}
}

func TestSnake(t *testing.T) {
	for in, want := range map[string]string{
		"PreToolUse": "pre_tool_use", "Stop": "stop", "UserPromptSubmit": "user_prompt_submit",
		"SessionStart": "session_start", "SubagentStop": "subagent_stop", "PermissionRequest": "permission_request",
	} {
		if got := snake(in); got != want {
			t.Errorf("snake(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestHookArgs: both values parse as TOML into what Codex reads, and
// every hook has its trust entry under the session-flags source.
func TestHookArgs(t *testing.T) {
	hooks, state, err := HookArgs([]string{"Stop", "SessionEnd"})
	if err != nil {
		t.Fatal(err)
	}
	type handler struct {
		Type    string
		Command string
		Timeout int
	}
	var h struct {
		Hooks map[string][]struct{ Hooks []handler }
	}
	if _, err := toml.Decode(hooks, &h); err != nil {
		t.Fatalf("%s: %v", hooks, err)
	}
	if got := h.Hooks["Stop"]; len(got) != 1 || len(got[0].Hooks) != 1 || got[0].Hooks[0] != (handler{"command", HookCommand, 5}) {
		t.Errorf("Stop: %+v", got)
	}
	if got := h.Hooks["SessionEnd"]; len(got) != 1 || got[0].Hooks[0].Timeout != 3 {
		t.Errorf("SessionEnd: %+v", got)
	}
	var s struct {
		Hooks struct {
			State map[string]struct {
				TrustedHash string `toml:"trusted_hash"`
			}
		}
	}
	if _, err := toml.Decode(state, &s); err != nil {
		t.Fatalf("%s: %v", state, err)
	}
	want, _ := TrustHash("stop", HookCommand, 5)
	if got := s.Hooks.State["/<session-flags>/config.toml:stop:0:0"].TrustedHash; got != want {
		t.Errorf("stop trust %q, want %q (state %s)", got, want, state)
	}
	want, _ = TrustHash("session_end", HookCommand, 3)
	if got := s.Hooks.State["/<session-flags>/config.toml:session_end:0:0"].TrustedHash; got != want {
		t.Errorf("session_end trust %q, want %q", got, want)
	}
	if _, _, err := HookArgs([]string{"Stop={}"}); err == nil {
		t.Error("an event that isn't one word passed")
	}
}

// TestLaunch: the argv of a fresh thread, a resumed one and a
// coordinator, with the hooks before the kickoff.
func TestLaunch(t *testing.T) {
	a := load(t)
	dir := t.TempDir()
	brief := filepath.Join(dir, "brief.md")
	if err := os.WriteFile(brief, []byte("# Brief\nSay \"hi\".\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	spec := agent.LaunchSpec{
		Role: agent.RoleThread, SessionID: "s-1", Cwd: "/r/wt", RepoRoot: "/r/repo", GitDir: "/r/repo/.git/worktrees/wt",
		RuntimeDir: dir, BriefPath: brief, Kickoff: "Start.", TMBin: "/bin/tm", Socket: "/run/tm.sock", Model: "gpt-6-luna",
	}
	l, err := a.Launch(spec)
	if err != nil {
		t.Fatal(err)
	}
	hooks, state, _ := HookArgs(a.Events())
	want := []string{"codex",
		"-c", "check_for_update_on_startup=false",
		"-c", `projects={"/r/repo"={trust_level="trusted"}}`,
		"-c", `developer_instructions="# Brief\nSay \"hi\".\n"`,
		"-c", `mcp_servers.terminatr.command="/bin/tm"`,
		"-c", `mcp_servers.terminatr.args=["mcp"]`,
		"-c", `mcp_servers.terminatr.env_vars=["TERMINATR_HOME"]`,
		"-c", `mcp_servers.terminatr.default_tools_approval_mode="approve"`,
		"-m", "gpt-6-luna",
		"-c", hooks, "-c", state,
		"--", "Start.",
	}
	if !slices.Equal(l.Argv, want) {
		t.Fatalf("argv\n%q\nwant\n%q", l.Argv, want)
	}
	if !l.Kickoff {
		t.Error("the kickoff is in argv")
	}
	if !slices.Contains(l.Env, "TERMINATR_BIN=/bin/tm") || !slices.Contains(l.Unset, "CODEX_THREAD_ID") {
		t.Errorf("env %q unset %q", l.Env, l.Unset)
	}
	for _, ev := range []string{"SessionStart", "UserPromptSubmit", "PreToolUse", "PermissionRequest", "Stop", "Interrupt", "SubagentStart", "SessionEnd"} {
		if !strings.Contains(hooks, ev+"=[") {
			t.Errorf("no %s hook in %s", ev, hooks)
		}
	}

	// Resume: the subcommand first, no kickoff, hooks last.
	spec.Resume, spec.AgentSID = true, "01a112dc-ae8a-7cc2-89e6-36ea46b45a2d"
	l, err = a.Launch(spec)
	if err != nil {
		t.Fatal(err)
	}
	if l.Argv[1] != "resume" || l.Argv[2] != spec.AgentSID || l.Kickoff || l.Argv[len(l.Argv)-1] != state {
		t.Errorf("resume argv %q", l.Argv)
	}

	// A coordinator outside a repo: trust the folder, auto-review; yolo
	// replaces auto-review.
	spec = agent.LaunchSpec{Role: "coordinator", Cwd: "/p", RuntimeDir: dir, TMBin: "/bin/tm"}
	l, err = a.Launch(spec)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(l.Argv, `projects={"/p"={trust_level="trusted"}}`) || !slices.Contains(l.Argv, "--approve-for-me") || l.Kickoff {
		t.Errorf("coordinator argv %q", l.Argv)
	}
	spec.Yolo = true
	l, _ = a.Launch(spec)
	if slices.Contains(l.Argv, "--approve-for-me") || !slices.Contains(l.Argv, "--dangerously-bypass-approvals-and-sandbox") {
		t.Errorf("yolo coordinator argv %q", l.Argv)
	}
}

// TestHooks: hook events move the state as the manifest says.
func TestHooks(t *testing.T) {
	a := load(t)
	ctx := agent.HookEnv{Context: func() ([]byte, error) { return []byte("ctx \"line\""), nil }}
	for _, c := range []struct {
		event   string
		payload map[string]any
		state   agent.State
		reason  string
	}{
		{"UserPromptSubmit", nil, agent.StateWorking, ""},
		{"PreToolUse", map[string]any{"tool_name": "Bash"}, agent.StateWorking, ""},
		{"PermissionRequest", map[string]any{"tool_name": "Bash"}, agent.StateBlocked, "permission"},
		{"Interrupt", nil, agent.StateIdle, "interrupted"},
		{"Stop", nil, agent.StateIdle, ""},
		{"SessionEnd", map[string]any{"reason": "exit"}, agent.StateExited, ""},
		{"SessionEnd", map[string]any{"reason": "clear"}, "", ""},
	} {
		p := map[string]any{"session_id": "sid-1"}
		for k, v := range c.payload {
			p[k] = v
		}
		sigs, _, err := a.Hook(agent.HookEvent{Event: c.event, Payload: p}, ctx)
		if err != nil {
			t.Fatal(err)
		}
		var st agent.State
		var reason string
		sid := ""
		for _, s := range sigs {
			if s.State != "" {
				st, reason = s.State, s.Reason
			}
			if s.AgentSID != "" {
				sid = s.AgentSID
			}
		}
		if st != c.state || reason != c.reason || sid != "sid-1" {
			t.Errorf("%s %v: state %q %q sid %q, want %q %q", c.event, c.payload, st, reason, sid, c.state, c.reason)
		}
	}
	_, res, err := a.Hook(agent.HookEvent{Event: "SessionStart", Payload: map[string]any{"source": "clear"}}, ctx)
	if err != nil || string(res.Stdout) != `{"hookSpecificOutput":{"hookEventName":"SessionStart","additionalContext":"ctx \"line\""}}` {
		t.Errorf("SessionStart: %s %v", res.Stdout, err)
	}
	sigs, _, _ := a.Hook(agent.HookEvent{Event: "SubagentStart", Payload: map[string]any{"agent_id": "a1"}}, ctx)
	if len(sigs) != 1 || sigs[0].Counter != "+bg" || sigs[0].CounterKey != "a1" {
		t.Errorf("SubagentStart: %+v", sigs)
	}
}

// TestGuard: PreToolUse keeps the tool's input through the hook's trim,
// and the guard's refusal is answered as a deny that Codex honours.
func TestGuard(t *testing.T) {
	a := load(t)
	payload := a.Sources().Hook.Trim(map[string]any{
		"hook_event_name": "PreToolUse", "session_id": "x", "tool_name": "apply_patch",
		"tool_input": map[string]any{"command": "*** Begin Patch\n*** Add File: /etc/x\n"},
	})
	var judged string
	env := agent.HookEnv{Guard: func(tool string, input map[string]any) *guard.Denial {
		judged = tool + ": " + input["command"].(string)
		return &guard.Denial{Rule: "worktree-only", Message: "terminatr guard (worktree-only): no", Summary: "apply_patch"}
	}}
	_, res, err := a.Hook(agent.HookEvent{Event: "PreToolUse", Payload: payload}, env)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(judged, "apply_patch: *** Begin Patch") {
		t.Errorf("judged %q", judged)
	}
	if want := `{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"deny","permissionDecisionReason":"terminatr guard (worktree-only): no"}}`; string(res.Stdout) != want {
		t.Errorf("deny %s", res.Stdout)
	}
	if p := a.Sources().Hook.Trim(map[string]any{"hook_event_name": "PostToolUse", "tool_input": map[string]any{}}); p["tool_input"] != nil {
		t.Error("tool_input kept beyond PreToolUse")
	}
}
