package codex

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
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

// TestLaunch: the argv of a fresh thread (asking for approvals), a
// resumed one and a coordinator, with the hooks before the kickoff.
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
		Access: agent.Access{
			Read: []string{"/h/projects/p"}, NoWrite: []string{"/h/projects/p"}, NoWriteFiles: []string{"/h/config.toml"},
			Write: []string{"/r/repo/.git", "/r/repo/.git/worktrees/wt"},
		},
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
		"-a", "on-request",
		"-c", `default_permissions="tm"`,
		"-c", `permissions={tm={extends=":workspace",filesystem={"/h/projects/p"="read","/r/repo/.git"="write","/r/repo/.git/worktrees/wt"="write","/h/config.toml"="read"},network={enabled=true,unix_sockets={"/run/tm.sock"="allow"}}}}`,
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

	// A coordinator outside a repo: trust the folder, auto-review, and
	// profile tm with its cwd written by name and the network limited to
	// the socket, kept when the probe says it holds.
	var probed []string
	probeFunc = func(codex, tmBin, cwd, sock, name string, flags []string) (string, error) {
		probed = append([]string{codex, tmBin, cwd, sock, name}, flags...)
		return "0.170.0", nil
	}
	defer func() { probeFunc = probeProfile }()
	spec = agent.LaunchSpec{
		Role: "coordinator", Cwd: "/p", RuntimeDir: dir, TMBin: "/bin/tm", Socket: "/run/tm.sock",
		Access: agent.Access{Read: []string{"/p", "/h/worktrees/p"}, NoWriteFiles: []string{"/h/config.toml"}, Commands: []string{"gh pr merge"}},
	}
	l, err = a.Launch(spec)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(l.Argv, `projects={"/p"={trust_level="trusted"}}`) || !slices.Contains(l.Argv, "--approve-for-me") || l.Kickoff {
		t.Errorf("coordinator argv %q", l.Argv)
	}
	if slices.Contains(l.Argv, "on-request") || len(l.Warnings) > 0 {
		t.Errorf("coordinator got -a or warnings: %q %q", l.Argv, l.Warnings)
	}
	coord := `permissions={tm={extends=":workspace",filesystem={"/p"="write","/h/worktrees/p"="read","/h/config.toml"="read"},network={enabled=true,mode="limited",unix_sockets={"/run/tm.sock"="allow"}}}}`
	for _, arg := range []string{`default_permissions="tm"`, "features.network_proxy=true", coord} {
		if !slices.Contains(l.Argv, arg) {
			t.Errorf("coordinator argv lacks %s: %q", arg, l.Argv)
		}
	}
	if _, err := toml.Decode(coord, &struct{}{}); err != nil {
		t.Errorf("coordinator profile %s: %v", coord, err)
	}
	// The probe runs the same flags, the profile named by -P instead.
	if want := []string{"codex", "/bin/tm", "/p", "/run/tm.sock", "tm", "-c", "features.network_proxy=true", "-c", coord}; !slices.Equal(probed, want) {
		t.Errorf("probed %q, want %q", probed, want)
	}

	// When it doesn't hold (a newer Codex renamed the proxy, say), the
	// coordinator runs as before the profile, and the launch says why.
	probeFunc = func(string, string, string, string, string, []string) (string, error) {
		return "0.170.0", errors.New("the network isn't limited to tm's socket: features.network_proxy or network.mode was ignored")
	}
	l, err = a.Launch(spec)
	if err != nil {
		t.Fatal(err)
	}
	fallback := []string{"codex",
		"-c", "check_for_update_on_startup=false",
		"-c", `projects={"/p"={trust_level="trusted"}}`,
		"-c", `mcp_servers.terminatr.command="/bin/tm"`,
		"-c", `mcp_servers.terminatr.args=["mcp"]`,
		"-c", `mcp_servers.terminatr.env_vars=["TERMINATR_HOME"]`,
		"-c", `mcp_servers.terminatr.default_tools_approval_mode="approve"`,
		"--approve-for-me",
		"-m", "gpt-6-luna",
		"-c", hooks, "-c", state,
	}
	if !slices.Equal(l.Argv, fallback) {
		t.Errorf("coordinator fallback argv\n%q\nwant\n%q", l.Argv, fallback)
	}
	if len(l.Warnings) != 1 || !strings.Contains(l.Warnings[0], "codex 0.170.0: the coordinator's sandbox profile doesn't hold (the network isn't limited") ||
		!strings.Contains(l.Warnings[0], "--approve-for-me") {
		t.Errorf("fallback warnings %q", l.Warnings)
	}
	// A thread is never probed.
	probed = nil
	probeFunc = func(codex, tmBin, cwd, sock, name string, flags []string) (string, error) {
		probed = []string{name}
		return "", errors.New("no")
	}
	if l, _ = a.Launch(agent.LaunchSpec{Role: agent.RoleThread, Cwd: "/r/wt", RuntimeDir: dir, TMBin: "/bin/tm", Socket: "/run/tm.sock"}); probed != nil || !slices.Contains(l.Argv, `default_permissions="tm"`) {
		t.Errorf("thread probed %q, argv %q", probed, l.Argv)
	}
	spec.Yolo = true
	l, _ = a.Launch(spec)
	if slices.Contains(l.Argv, "--approve-for-me") || !slices.Contains(l.Argv, "--dangerously-bypass-approvals-and-sandbox") ||
		slices.Contains(l.Argv, `default_permissions="tm"`) || slices.Contains(l.Argv, "features.network_proxy=true") {
		t.Errorf("yolo coordinator argv %q", l.Argv)
	}

	// The profile parses as TOML; a yolo thread has no sandbox to shape.
	var v struct {
		Permissions map[string]struct {
			Extends    string
			Filesystem map[string]string
		}
	}
	if _, err := toml.Decode(want[20], &v); err != nil || v.Permissions["tm"].Filesystem["/h/config.toml"] != "read" {
		t.Errorf("profile %s: %v %+v", want[20], err, v)
	}
	spec = agent.LaunchSpec{Role: agent.RoleThread, Cwd: "/r/wt", RuntimeDir: dir, TMBin: "/bin/tm", Socket: "/run/tm.sock", Yolo: true}
	l, _ = a.Launch(spec)
	if slices.Contains(l.Argv, `default_permissions="tm"`) || slices.Contains(l.Argv, "on-request") {
		t.Errorf("yolo thread got the profile or -a: %q", l.Argv)
	}
}

// TestHooks: every hook event the manifest maps moves the state as it
// says.
func TestHooks(t *testing.T) {
	a := load(t)
	ctx := agent.HookEnv{Context: func() ([]byte, error) { return []byte("ctx \"line\""), nil }}
	type hookCase struct {
		event   string
		payload map[string]any
		state   agent.State
		reason  string
	}
	cases := []hookCase{
		{"SessionStart", map[string]any{"source": "startup"}, agent.StateIdle, ""},
		{"UserPromptSubmit", nil, agent.StateWorking, ""},
		{"PreToolUse", map[string]any{"tool_name": "Bash"}, agent.StateWorking, ""},
		{"PostToolUse", map[string]any{"tool_name": "Bash"}, agent.StateWorking, ""},
		{"PreCompact", map[string]any{"trigger": "manual"}, agent.StateWorking, ""},
		{"PermissionRequest", map[string]any{"tool_name": "Bash"}, agent.StateBlocked, "permission"},
		{"Interrupt", nil, agent.StateIdle, "interrupted"},
		{"Stop", nil, agent.StateIdle, ""},
		{"SubagentStart", map[string]any{"agent_id": "a1"}, "", ""},
		{"SubagentStop", map[string]any{"agent_id": "a1"}, "", ""},
		{"SessionEnd", map[string]any{"reason": "other"}, "", ""}, // a thread /clear left, or the end: the exit says which
	}
	for _, ev := range a.Events() {
		if !slices.ContainsFunc(cases, func(c hookCase) bool { return c.event == ev }) {
			t.Errorf("the manifest maps %s, which this test doesn't send", ev)
		}
	}
	for _, c := range cases {
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
	for ev, want := range map[string]string{"SubagentStart": "+bg", "SubagentStop": "-bg"} {
		sigs, _, _ := a.Hook(agent.HookEvent{Event: ev, Payload: map[string]any{"agent_id": "a1"}}, ctx)
		if !slices.ContainsFunc(sigs, func(s agent.Signal) bool { return s.Counter == want && s.CounterKey == "a1" }) {
			t.Errorf("%s: %+v", ev, sigs)
		}
	}
}

// TestRollout: the rollout rules of the real manifest, on lines in the
// shape of a 0.160 rollout: a turn starts, ends, or is aborted by Esc;
// anything else is no signal.
func TestRollout(t *testing.T) {
	tail := load(t).Sources().JSONLTail
	if tail == nil {
		t.Fatal("no [jsonl_tail]")
	}
	for _, c := range []struct {
		line   string
		state  agent.State
		reason string
	}{
		{`{"timestamp":"2026-10-06T20:18:01.002Z","type":"event_msg","payload":{"type":"task_started","model_context_window":258400}}`, agent.StateWorking, ""},
		{`{"timestamp":"2026-10-06T20:18:09.300Z","type":"event_msg","payload":{"type":"task_complete","last_agent_message":"Done."}}`, agent.StateIdle, ""},
		{`{"timestamp":"2026-10-06T20:18:09.300Z","type":"event_msg","payload":{"type":"turn_aborted","reason":"interrupted"}}`, agent.StateIdle, "interrupted"},
		{`{"timestamp":"2026-10-06T20:18:09.301Z","type":"response_item","payload":{"type":"message","role":"assistant","content":[]}}`, "", ""},
		{`{"type":"event_msg","payload":{"type":"agent_message","message":"task_complete"}}`, "", ""},
	} {
		sig, ok := tail.Line([]byte(c.line))
		if ok != (c.state != "") || sig.State != c.state || sig.Reason != c.reason {
			t.Errorf("%s: %+v %v, want %q %q", c.line, sig, ok, c.state, c.reason)
		}
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

// TestLeftThread: once the session is on a thread, events naming another
// one are from a thread it left, except SessionStart, which switches.
func TestLeftThread(t *testing.T) {
	src := load(t).Sources()
	end := map[string]any{"session_id": "old", "reason": "other"}
	if sid, ok := src.Foreign("SessionEnd", end, "new"); !ok || sid != "old" {
		t.Errorf("SessionEnd of the thread left: %q %v", sid, ok)
	}
	for _, c := range []struct {
		event, sid, current string
	}{
		{"SessionStart", "new", "old"}, // the switch itself
		{"Stop", "new", "new"},
		{"Stop", "new", ""}, // no thread yet
		{"Stop", "", "new"},
	} {
		if _, ok := src.Foreign(c.event, map[string]any{"session_id": c.sid}, c.current); ok {
			t.Errorf("%+v: ignored", c)
		}
	}
}

// TestSocketAccess: only on Windows does the socket's folder become
// writable (Codex's sandbox there connects to a socket by file access).
func TestSocketAccess(t *testing.T) {
	spec := agent.LaunchSpec{Socket: filepath.Join("run", "tm.sock"), Access: agent.Access{Write: []string{"w"}}}
	if got := socketAccess(spec, "linux"); len(got.Access.Write) != 1 {
		t.Errorf("linux: %v", got.Access.Write)
	}
	got := socketAccess(spec, "windows")
	if len(got.Access.Write) != 2 || got.Access.Write[1] != "run" || len(spec.Access.Write) != 1 {
		t.Errorf("windows: %v (spec %v)", got.Access.Write, spec.Access.Write)
	}
}
