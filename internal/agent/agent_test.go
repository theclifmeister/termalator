package agent

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/theclifmeister/terminatr/internal/guard"
)

func claude(t *testing.T) Agent {
	t.Helper()
	r, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	a, ok := r.Get("claude")
	if !ok {
		t.Fatal("claude manifest not embedded")
	}
	return a
}

func threadSpec() LaunchSpec {
	return LaunchSpec{
		Role: RoleThread, SessionID: "s1", AgentSID: "uuid-1",
		Cwd: "/w", RuntimeDir: "/run/s1", BriefPath: "/h/.terminatr/projects/p/threads/t-0001/brief.md",
		Kickoff: "Run tm skill thread.", TMBin: "/bin/tm", Socket: "/run/tm.sock",
		Access: Access{Read: []string{"/h/.terminatr/projects/p"}, NoWrite: []string{"/h/.terminatr/projects/p"}},
	}
}

func TestClaudeLaunch(t *testing.T) {
	a := claude(t)
	spec := threadSpec()
	l, err := a.Launch(spec)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"claude",
		"--plugin-dir", "/run/s1/claude-plugin",
		"--settings", "/run/s1/claude-settings.json",
		"--session-id", "uuid-1",
		"--append-system-prompt-file", "/h/.terminatr/projects/p/threads/t-0001/brief.md",
		"--", "Run tm skill thread."}
	if !reflect.DeepEqual(l.Argv, want) {
		t.Fatalf("argv\n got %q\nwant %q", l.Argv, want)
	}
	if !l.Kickoff {
		t.Fatal("Kickoff unset with the kickoff in argv")
	}
	spec.Resume = true
	if l, err := a.Launch(spec); err != nil || l.Kickoff {
		t.Fatalf("resume: Kickoff %v, err %v", l.Kickoff, err)
	}
	spec.Resume = false
	if !contains(l.Unset, "CLAUDECODE") || contains(l.Unset, "CLAUDE_CODE_*") {
		t.Fatalf("unset = %q: want the inherited session vars, not the whole CLAUDE_CODE_ prefix", l.Unset)
	}

	var hooks struct {
		Hooks map[string][]struct {
			Hooks []struct {
				Type, Command string
				Async         bool
				Timeout       int
			}
		}
	}
	if err := json.Unmarshal(l.Files["claude-plugin/hooks/hooks.json"], &hooks); err != nil {
		t.Fatalf("hooks.json is not JSON: %v\n%s", err, l.Files["claude-plugin/hooks/hooks.json"])
	}
	for _, ev := range []string{"SessionStart", "PermissionRequest", "SubagentStart", "TaskCreated", "SessionEnd"} {
		h := hooks.Hooks[ev]
		if len(h) != 1 || h[0].Hooks[0].Command != `"/bin/tm" hook --agent claude` || h[0].Hooks[0].Async || h[0].Hooks[0].Timeout != 5 {
			t.Fatalf("hooks.json %s = %+v; want one sync command hook with timeout 5", ev, h)
		}
	}

	settings := parseSettings(t, l.Files["claude-settings.json"])
	if want := []string{"Read(//h/.terminatr/projects/p/**)"}; !reflect.DeepEqual(settings.Permissions.Allow, want) {
		t.Fatalf("allow = %q, want %q", settings.Permissions.Allow, want)
	}
	if want := []string{"Edit(//h/.terminatr/projects/p/**)"}; !reflect.DeepEqual(settings.Permissions.Deny, want) {
		t.Fatalf("deny = %q, want %q (Edit rules only)", settings.Permissions.Deny, want)
	}
	if !settings.Sandbox.Enabled || !reflect.DeepEqual(settings.Sandbox.Network.AllowUnixSockets, []string{"/run/tm.sock"}) {
		t.Fatalf("thread sandbox = %+v", settings.Sandbox)
	}
	if settings.StatusLine != nil {
		t.Fatal("settings must never set statusLine; it would replace the user's")
	}

	coord := spec
	coord.Role, coord.Access = Role("coordinator"), Access{Read: []string{"/h/.terminatr/worktrees/p"}}
	l, err = a.Launch(coord)
	if err != nil {
		t.Fatal(err)
	}
	settings = parseSettings(t, l.Files["claude-settings.json"])
	if settings.Sandbox.Enabled || len(settings.Permissions.Deny) != 0 {
		t.Fatalf("coordinator must not get the thread sandbox or deny rules: %+v", settings)
	}

	coord.Access.Commands = []string{"gh pr merge", "az repos pr update"}
	if l, err = a.Launch(coord); err != nil {
		t.Fatal(err)
	}
	settings = parseSettings(t, l.Files["claude-settings.json"])
	if want := []string{"Read(//h/.terminatr/worktrees/p/**)", "Bash(gh pr merge:*)", "Bash(az repos pr update:*)"}; !reflect.DeepEqual(settings.Permissions.Allow, want) {
		t.Fatalf("allow = %q, want %q", settings.Permissions.Allow, want)
	}

	spec.Resume, spec.Yolo = true, true
	l, err = a.Launch(spec)
	if err != nil {
		t.Fatal(err)
	}
	want = []string{"claude",
		"--plugin-dir", "/run/s1/claude-plugin",
		"--settings", "/run/s1/claude-settings.json",
		"--append-system-prompt-file", "/h/.terminatr/projects/p/threads/t-0001/brief.md",
		"--resume", "uuid-1", "--dangerously-skip-permissions"}
	if !reflect.DeepEqual(l.Argv, want) {
		t.Fatalf("resume argv\n got %q\nwant %q", l.Argv, want)
	}

	spec.AgentSID = ""
	if _, err := a.Launch(spec); err == nil {
		t.Fatal("resume without a session id must fail; claude --resume \"\" opens a picker")
	}
}

type claudeSettings struct {
	Permissions struct{ Allow, Deny []string }
	Sandbox     struct {
		Enabled bool
		Network struct{ AllowUnixSockets []string }
	}
	StatusLine any `json:"statusLine"`
}

func parseSettings(t *testing.T, b []byte) claudeSettings {
	t.Helper()
	var s claudeSettings
	if err := json.Unmarshal(b, &s); err != nil {
		t.Fatalf("claude-settings.json is not JSON: %v\n%s", err, b)
	}
	return s
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// states returns the state signals of an event as "state/reason" strings.
func states(t *testing.T, a Agent, event string, payload map[string]any) []string {
	t.Helper()
	sigs, _, err := a.Hook(HookEvent{Event: event, Payload: payload}, HookEnv{})
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, s := range sigs {
		if s.State != "" {
			out = append(out, string(s.State)+"/"+s.Reason)
		}
	}
	return out
}

func TestClaudeHookStates(t *testing.T) {
	a := claude(t)
	cases := []struct {
		event   string
		payload map[string]any
		want    []string
	}{
		{"UserPromptSubmit", nil, []string{"working/"}},
		{"PermissionRequest", map[string]any{"tool_name": "Bash"}, []string{"blocked/permission"}},
		{"PermissionRequest", map[string]any{"tool_name": "AskUserQuestion"}, []string{"blocked/question"}},
		{"Notification", map[string]any{"notification_type": "idle_prompt"}, []string{"idle/"}},
		{"Stop", nil, []string{"idle/"}},
		{"Stop", map[string]any{"agent_id": "sub-1"}, nil},
		{"SessionEnd", map[string]any{"reason": "clear"}, nil},
		{"SessionEnd", map[string]any{"reason": "prompt_input_exit"}, []string{"exited/"}},
		{"SessionEnd", map[string]any{"reason": "other"}, []string{"exited/"}},
	}
	for _, c := range cases {
		if got := states(t, a, c.event, c.payload); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s %v -> %q, want %q", c.event, c.payload, got, c.want)
		}
	}
}

func TestClaudeSessionIDAndCounters(t *testing.T) {
	a := claude(t)
	sigs, _, _ := a.Hook(HookEvent{Event: "UserPromptSubmit", Payload: map[string]any{"session_id": "new-after-clear"}}, HookEnv{})
	if len(sigs) == 0 || sigs[0].AgentSID != "new-after-clear" {
		t.Fatalf("session id must be read from every event, got %+v", sigs)
	}

	counter := func(event string, payload map[string]any) string {
		sigs, _, _ := a.Hook(HookEvent{Event: event, Payload: payload}, HookEnv{})
		for _, s := range sigs {
			if s.Counter != "" {
				return s.Counter + ":" + s.CounterKey
			}
		}
		return ""
	}
	if got := counter("SubagentStart", map[string]any{"agent_id": "a1", "agent_type": "general-purpose"}); got != "+bg:a1" {
		t.Fatalf("SubagentStart -> %q", got)
	}
	if got := counter("SubagentStop", map[string]any{"agent_id": "a1", "agent_type": "general-purpose"}); got != "-bg:a1" {
		t.Fatalf("SubagentStop -> %q", got)
	}
	if got := counter("SubagentStop", map[string]any{"agent_id": "suggest"}); got != "" {
		t.Fatalf("the prompt-suggestion side agent (no agent_type) must be ignored, got %q", got)
	}
}

func TestClaudeContextResponse(t *testing.T) {
	a := claude(t)
	_, res, err := a.Hook(HookEvent{Event: "SessionStart", Payload: map[string]any{"session_id": "x", "source": "clear"}},
		HookEnv{Context: func() ([]byte, error) { return []byte("# Context\n\"quoted\""), nil }})
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		HookSpecificOutput struct{ AdditionalContext string } `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal(res.Stdout, &out); err != nil {
		t.Fatalf("SessionStart response is not JSON: %v\n%s", err, res.Stdout)
	}
	if out.HookSpecificOutput.AdditionalContext != "# Context\n\"quoted\"" {
		t.Fatalf("additionalContext = %q", out.HookSpecificOutput.AdditionalContext)
	}
}

// TestClaudeGuardResponse: without the mod, PreToolUse answers the
// guard's refusal as a deny, judged once; an allowed call gets no output.
func TestClaudeGuardResponse(t *testing.T) {
	a := claude(t)
	var calls []string
	env := HookEnv{Guard: func(tool string, input map[string]any) *guard.Denial {
		calls = append(calls, tool+" "+input["command"].(string))
		if input["command"] == "gh pr merge 1" {
			return &guard.Denial{Rule: "merge", Message: `terminatr guard (merge): "no"`, Summary: "gh pr merge"}
		}
		return nil
	}}
	call := func(cmd string) []byte {
		_, res, err := a.Hook(HookEvent{Event: "PreToolUse", Payload: map[string]any{
			"session_id": "x", "tool_name": "Bash", "tool_input": map[string]any{"command": cmd}}}, env)
		if err != nil {
			t.Fatal(err)
		}
		return res.Stdout
	}
	var out struct {
		HookSpecificOutput struct {
			HookEventName, PermissionDecision, PermissionDecisionReason string
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal(call("gh pr merge 1"), &out); err != nil {
		t.Fatal(err)
	}
	if h := out.HookSpecificOutput; h.HookEventName != "PreToolUse" || h.PermissionDecision != "deny" || h.PermissionDecisionReason != `terminatr guard (merge): "no"` {
		t.Errorf("deny = %+v", h)
	}
	if got := call("gh pr view 1"); len(got) != 0 {
		t.Errorf("allowed call answered %q", got)
	}
	if want := []string{"Bash gh pr merge 1", "Bash gh pr view 1"}; !slices.Equal(calls, want) {
		t.Errorf("judged %q, want %q", calls, want)
	}
	// Without a guard (the mod judges), nothing is answered.
	_, res, _ := a.Hook(HookEvent{Event: "PreToolUse", Payload: map[string]any{"tool_name": "Bash", "tool_input": map[string]any{"command": "gh pr merge 1"}}}, HookEnv{})
	if len(res.Stdout) != 0 {
		t.Errorf("no guard answered %q", res.Stdout)
	}
}

// todo applies one hook event to list and returns the result.
func todo(t *testing.T, a Agent, list []Todo, event string, payload map[string]any) []Todo {
	t.Helper()
	sigs, _, err := a.Hook(HookEvent{Event: event, Payload: payload}, HookEnv{})
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range sigs {
		if s.Todo != nil {
			return ApplyTodo(list, *s.Todo)
		}
	}
	return list
}

func TestClaudeTodos(t *testing.T) {
	a := claude(t)
	create := func(id, subject string) map[string]any {
		return map[string]any{
			"tool_name":     "TaskCreate",
			"tool_input":    map[string]any{"subject": subject, "description": "…", "activeForm": subject + "ing"},
			"tool_response": map[string]any{"task": map[string]any{"id": id, "subject": subject}},
		}
	}
	update := func(id, status string) map[string]any {
		return map[string]any{
			"tool_name":  "TaskUpdate",
			"tool_input": map[string]any{"taskId": id, "status": status},
		}
	}

	var list []Todo
	list = todo(t, a, list, "PostToolUse", create("1", "Test"))
	list = todo(t, a, list, "PostToolUse", create("2", "Fix"))
	list = todo(t, a, list, "PostToolUse", update("1", "completed"))
	list = todo(t, a, list, "PostToolUse", update("2", "in_progress"))
	want := []Todo{
		{ID: "1", Text: "Test", ActiveText: "Testing", Status: TodoCompleted},
		{ID: "2", Text: "Fix", ActiveText: "Fixing", Status: TodoInProgress},
	}
	if !reflect.DeepEqual(list, want) {
		t.Fatalf("after diffs:\n got %+v\nwant %+v", list, want)
	}

	list = todo(t, a, list, "PostToolUse", update("1", "deleted"))
	if len(list) != 1 || list[0].ID != "2" {
		t.Fatalf("deleted must remove the item, got %+v", list)
	}

	list = todo(t, a, list, "SessionStart", map[string]any{"source": "compact"})
	if len(list) != 1 {
		t.Fatalf("compaction keeps the list, got %+v", list)
	}
	list = todo(t, a, list, "SessionStart", map[string]any{"source": "clear"})
	if list == nil || len(list) != 0 {
		t.Fatalf("/clear starts a new list, got %+v", list)
	}

	list = todo(t, a, list, "PostToolUse", map[string]any{
		"tool_name": "TodoWrite",
		"tool_input": map[string]any{"todos": []any{
			map[string]any{"content": "A", "status": "completed", "activeForm": "Doing A"},
			map[string]any{"content": "B", "status": "pending", "activeForm": "Doing B"},
		}},
	})
	if len(list) != 2 || list[1].Text != "B" || list[0].Status != TodoCompleted {
		t.Fatalf("TodoWrite replaces the list, got %+v", list)
	}
}

func TestStatusFile(t *testing.T) {
	src := claude(t).Sources()
	f := src.StatusFile
	path, err := f.PathFor(SourceVars{Home: "/h", PID: 4242})
	if err != nil || path != "/h/.claude/sessions/4242.json" {
		t.Fatalf("path = %q, %v", path, err)
	}

	r, err := f.Read([]byte(`{"pid":4242,"sessionId":"s9","status":"waiting","waitingFor":"permission prompt",
		"version":"2.1.289","messagingSocketPath":"/tmp/cc-socks/x.sock"}`), src)
	if err != nil {
		t.Fatal(err)
	}
	if r.Signal.State != StateBlocked || r.Signal.Reason != "permission" || r.Signal.AgentSID != "s9" ||
		r.Fields["messaging_socket"] != "/tmp/cc-socks/x.sock" {
		t.Fatalf("reading = %+v", r)
	}

	// Remote Control: bridgeSessionId is null while off (as observed on
	// 2.1.289), a session id while on.
	if !ManifestOf(claude(t)).ObservesRemote() {
		t.Fatal("claude's status file should say whether remote control is on")
	}
	rc := RemoteControlOf(claude(t)).StatusField
	for body, on := range map[string]bool{
		`{"status":"idle","version":"2.1.289","bridgeSessionId":null}`:         false,
		`{"status":"idle","version":"2.1.289"}`:                                false,
		`{"status":"idle","version":"2.1.289","bridgeSessionId":"session_01"}`: true,
	} {
		r, err := f.Read([]byte(body), src)
		if err != nil || (r.Fields[rc] != "") != on {
			t.Fatalf("%s: fields %v, %v; want remote control %v", body, r.Fields, err, on)
		}
	}

	// "shell": idle at the prompt with a background shell running (seen
	// on the terminatr coordinator, T43). Unknown, it distrusted the
	// whole file, the socket and remote control with it.
	r, err = f.Read([]byte(`{"status":"shell","version":"2.1.289","messagingSocketPath":"/tmp/x.sock","bridgeSessionId":"session_01"}`), src)
	if err != nil || r.Signal.State != StateIdle || r.Fields[rc] == "" {
		t.Fatalf("shell: %+v %v", r, err)
	}

	_, err = f.Read([]byte(`{"status":"idle","version":"3.0.0"}`), src)
	if !errors.Is(err, ErrUntestedVersion) {
		t.Fatalf("an untested version must be refused, got %v", err)
	}
	if _, err := f.Read([]byte(`{"status":"thinking","version":"2.1.300"}`), src); err == nil {
		t.Fatal("an unknown status must be refused")
	}
}

func TestJSONLTail(t *testing.T) {
	tail := claude(t).Sources().JSONLTail
	cases := []struct {
		line string
		want State
		ok   bool
	}{
		{`{"type":"user","message":{"role":"user","content":[{"type":"text","text":"[Request interrupted by user for tool use]"}]}}`, StateIdle, true},
		{`{"type":"user","message":{"role":"user","content":"[Request interrupted by user]"}}`, StateIdle, true},
		{`{"type":"system","subtype":"turn_duration","durationMs":1200}`, StateIdle, true},
		{`{"type":"user","message":{"role":"user","content":"hello"}}`, "", false},
		{`not json`, "", false},
	}
	for _, c := range cases {
		sig, ok := tail.Line([]byte(c.line))
		if ok != c.ok || sig.State != c.want {
			t.Errorf("%s -> %v %v, want %v %v", c.line, sig.State, ok, c.want, c.ok)
		}
	}
}

func TestTodoSnapshot(t *testing.T) {
	snap := claude(t).Sources().TodoSnapshot
	dir, _ := snap.DirFor(SourceVars{Home: "/h", AgentSID: "s9"})
	if dir != "/h/.claude/tasks/s9" {
		t.Fatalf("dir = %q", dir)
	}
	got := snap.Parse(map[string][]byte{
		"10.json":        []byte(`{"id":"10","subject":"ten","status":"pending"}`),
		"2.json":         []byte(`{"id":"2","subject":"two","status":"in_progress"}`),
		".highwatermark": []byte(`10`),
		"bad.json":       []byte(`{`),
	})
	want := []Todo{{ID: "2", Text: "two", Status: TodoInProgress}, {ID: "10", Text: "ten", Status: TodoPending}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("snapshot = %+v, want %+v", got, want)
	}
}

func TestHookTrim(t *testing.T) {
	trim := claude(t).Sources().Hook
	long := strings.Repeat("é", 400) // 800 bytes
	got := trim.Trim(map[string]any{
		"hook_event_name": "PostToolUse", "session_id": "s", "tool_name": "Bash",
		"tool_input": map[string]any{"command": "ls"}, "tool_response": "big", "prompt": long,
	})
	if _, ok := got["tool_input"]; ok {
		t.Fatal("tool_input must be dropped for ordinary tools")
	}
	if p := got["prompt"].(string); len(p) > 500 || !strings.HasPrefix(long, p) {
		t.Fatalf("prompt must be cut to <=500 bytes on a rune boundary, got %d bytes", len(p))
	}
	got = trim.Trim(map[string]any{"hook_event_name": "PostToolUse", "tool_name": "TaskUpdate", "tool_input": map[string]any{"taskId": "1"}})
	if _, ok := got["tool_input"]; !ok {
		t.Fatal("tool_input must be kept for TaskUpdate")
	}
}

func TestFilterEnv(t *testing.T) {
	env := []string{"PATH=/bin", "CLAUDECODE=1", "CLAUDE_CODE_SESSION_ID=x", "CLAUDE_CODE_USE_BEDROCK=1", "CLAUDE_CODE_MESSAGING_TOKEN=t"}
	l, err := claude(t).Launch(threadSpec())
	if err != nil {
		t.Fatal(err)
	}
	got := FilterEnv(env, l.Unset)
	want := []string{"PATH=/bin", "CLAUDE_CODE_USE_BEDROCK=1"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("FilterEnv = %q, want %q", got, want)
	}
}

func TestMatchPatterns(t *testing.T) {
	p := map[string]any{"a": "x", "n": map[string]any{"id": float64(3)}, "e": ""}
	cases := []struct {
		want map[string]string
		ok   bool
	}{
		{map[string]string{"a": "x"}, true},
		{map[string]string{"a": "!x"}, false},
		{map[string]string{"a": "!y"}, true},
		{map[string]string{"a": "*"}, true},
		{map[string]string{"e": "*"}, false},
		{map[string]string{"missing": "!*"}, true},
		{map[string]string{"n.id": "3"}, true},
	}
	for _, c := range cases {
		if got := matches(c.want, p); got != c.ok {
			t.Errorf("matches(%v) = %v, want %v", c.want, got, c.ok)
		}
	}
}

func TestUserManifest(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("myagent.toml", `manifest_version = 1
name = "myagent"
[identify]
argv0 = ["myagent"]
[launch]
command = "myagent"
kickoff_args = ["{{.Kickoff}}"]
[[hooks]]
event = "busy"
state = "working"
`)
	write("wrong.toml", "manifest_version = 1\nname = \"other\"\n[launch]\ncommand = \"x\"\n")
	write("badrule.toml", "manifest_version = 1\nname = \"badrule\"\n[launch]\ncommand = \"x\"\n[[rules]]\nid = \"r\"\nstate = \"idle\"\nregex = \"(\"\n")

	r, err := Load(dir)
	if err == nil || !strings.Contains(err.Error(), "wrong.toml") || !strings.Contains(err.Error(), "badrule.toml") {
		t.Fatalf("want errors for wrong.toml and badrule.toml, got %v", err)
	}
	a, ok := r.Get("myagent")
	if !ok {
		t.Fatal("user manifest not loaded")
	}
	if got, _ := r.Identify(ProcessInfo{Argv: []string{"/usr/local/bin/myagent"}}); got != a {
		t.Fatal("Identify failed")
	}
	if _, ok := r.Get("claude"); !ok {
		t.Fatal("built-ins must still load next to user manifests")
	}
}

func TestManifestScreenResize(t *testing.T) {
	const base = "manifest_version = 1\nname = \"a\"\n[launch]\ncommand = \"a\"\n"
	for _, c := range []struct {
		screen string
		follow bool
		ok     bool
	}{
		{"", true, true},
		{"[screen]\nresize = \"follow\"\n", true, true},
		{"[screen]\nresize = \"explicit\"\n", false, true},
		{"[screen]\nresize = \"never\"\n", false, false},
	} {
		m, err := ParseManifest([]byte(base + c.screen))
		if (err == nil) != c.ok {
			t.Errorf("%q: err = %v, want ok %v", c.screen, err, c.ok)
			continue
		}
		if err == nil && FollowsTyping(FromManifest(m)) != c.follow {
			t.Errorf("%q: FollowsTyping = %v", c.screen, !c.follow)
		}
	}
	if !FollowsTyping(nil) {
		t.Error("a pane without an agent must follow typing")
	}
}

func TestBuiltinUnsetEnv(t *testing.T) {
	got := FilterEnv([]string{"CLAUDECODE=1", "CLAUDE_CODE_SESSION_ID=x", "CLAUDE_CODE_USE_BEDROCK=1", "PATH=/bin"}, BuiltinUnsetEnv())
	if len(got) != 2 || got[0] != "CLAUDE_CODE_USE_BEDROCK=1" || got[1] != "PATH=/bin" {
		t.Fatalf("FilterEnv with the built-in unset list = %q", got)
	}
}

func TestTodoSnapshotHeal(t *testing.T) {
	snap := &TodoSnapshot{}
	mirror := []Todo{{ID: "1", Text: "a", ActiveText: "Doing a", Status: TodoInProgress}, {ID: "2", Text: "b", Status: TodoPending}}
	got := snap.Heal(mirror, []Todo{{ID: "1", Text: "a", Status: TodoCompleted}, {ID: "3", Text: "c", Status: TodoPending}})
	want := []Todo{{ID: "1", Text: "a", ActiveText: "Doing a", Status: TodoCompleted}, {ID: "3", Text: "c", Status: TodoPending}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Heal = %+v", got)
	}
}

func TestManifestRemoteControl(t *testing.T) {
	const base = "manifest_version = 1\nname = \"a\"\n[launch]\ncommand = \"a\"\nkickoff_args = [\"--\", \"{{.Kickoff}}\"]\n"
	for _, c := range []struct {
		rc        string
		supported bool
		ok        bool
	}{
		{"", false, true},
		{"[remote_control]\nargs = [\"--rc\", \"{{.RemoteName}}\"]\n", true, true},
		{"[remote_control]\nargs = [\"--rc\"]\nenable = \"/rc {{.RemoteName}}\"\ndisable = \"/rc off\"\n", true, true},
		{"[remote_control]\nenable = \"/rc\"\n", false, false},
		{"[remote_control]\nargs = [\"--rc\"]\nname = \"x\"\n", false, false},
		{"[remote_control]\nargs = [\"--rc\"]\ndisable = \"/rc\"\ndisable_dialog = { contains = \"Disconnect\", keys = \"\\r\", done = \"Disconnected\" }\n", true, true},
		{"[remote_control]\nargs = [\"--rc\"]\ndisable_dialog = { contains = \"Disconnect\", keys = \"\\r\", done = \"x\" }\n", false, false},
		{"[remote_control]\nargs = [\"--rc\"]\ndisable = \"/rc\"\ndisable_dialog = { contains = \"Disconnect\" }\n", false, false},
		{"[remote_control]\nargs = [\"--rc\"]\nstatus_field = \"rc\"\n", true, true}, // no status file: unobserved
	} {
		m, err := ParseManifest([]byte(base + c.rc))
		if (err == nil) != c.ok {
			t.Errorf("%q: err = %v, want ok %v", c.rc, err, c.ok)
			continue
		}
		if err == nil && (RemoteControlOf(FromManifest(m)) != nil) != c.supported {
			t.Errorf("%q: supported = %v", c.rc, !c.supported)
		}
	}

	m, err := ParseManifest([]byte(base))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := FromManifest(m).Launch(LaunchSpec{RemoteControl: true}); err == nil || !strings.Contains(err.Error(), "no remote control") {
		t.Fatalf("remote control on an agent without it: err = %v", err)
	}
}

func TestClaudeRemoteControl(t *testing.T) {
	a := claude(t)
	spec := threadSpec()
	spec.Role, spec.RemoteControl, spec.RemoteName = Role("coordinator"), true, "demo"
	l, err := a.Launch(spec)
	if err != nil {
		t.Fatal(err)
	}
	// The flag takes an optional value, so the name follows it and the
	// kickoff still comes last, after "--".
	n := len(l.Argv)
	if got := l.Argv[n-4:]; !reflect.DeepEqual(got, []string{"--remote-control", "demo", "--", "Run tm skill thread."}) {
		t.Fatalf("argv tail = %q", got)
	}
	spec.Resume = true
	if l, err = a.Launch(spec); err != nil || !contains(l.Argv, "--remote-control") || !contains(l.Argv, "--resume") {
		t.Fatalf("resumed with remote control: %q %v", l.Argv, err)
	}
	rc := RemoteControlOf(a)
	if rc == nil {
		t.Fatal("claude has remote control")
	}
	if s, err := rc.Text(true, spec); err != nil || s != "/remote-control demo" {
		t.Fatalf("enable text = %q %v", s, err)
	}
	// Off is the same command, which opens a menu: a resumed
	// conversation would reconnect, so a restart can't turn it off.
	if s, err := rc.Text(false, spec); err != nil || s != "/remote-control" {
		t.Fatalf("disable text = %q %v", s, err)
	}
	if d := rc.DisableDialog; d == nil || d.Keys != "\x1b[A\x1b[A\r" || d.Contains == "" || d.Done == "" {
		t.Fatalf("disable dialog = %+v", d)
	}
}

func TestManifestAnswer(t *testing.T) {
	const base = "manifest_version = 1\nname = \"a\"\n[launch]\ncommand = \"a\"\n" +
		"[[rules]]\nid = \"q\"\nstate = \"blocked\"\nreason = \"question\"\nregion = \"screen\"\ncontains = [\"Enter to select\"]\n" +
		"[[rules]]\nid = \"idle\"\nstate = \"idle\"\nregion = \"screen\"\ncontains = [\"❯\"]\n"
	for _, c := range []struct {
		answer string
		has    bool
		ok     bool
	}{
		{"", false, true},
		{"[answer]\nrule = \"q\"\n", true, true},
		{"[answer]\nrule = \"q\"\ntext_option = \"Type something\"\nsubmit = \"\\r\"\n", true, true},
		{"[answer]\nrule = \"q\"\ntext_option = \"Type something\"\n", false, false},
		{"[answer]\nrule = \"q\"\ntext_option = \"Type something\"\ntext_key = \"\\t\"\nsubmit = \"\\r\"\n", true, true},
		{"[answer]\nrule = \"q\"\ntext_key = \"\\t\"\n", false, false},
		{"[answer]\nrule = \"q\"\ntext_option = \"x\"\ntext_focus = \"j\"\nsubmit = \"\\r\"\n", true, true},
		{"[answer]\nrule = \"q\"\ntext_focus = \"j\"\n", false, false},
		{"[answer]\nrule = \"idle\"\n", false, false},
		{"[answer]\nrule = \"nope\"\n", false, false},
		{"[answer]\ntext_option = \"x\"\nsubmit = \"\\r\"\n", false, false},
	} {
		m, err := ParseManifest([]byte(base + c.answer))
		if (err == nil) != c.ok {
			t.Errorf("%q: err = %v, want ok %v", c.answer, err, c.ok)
			continue
		}
		if err == nil && (AnswerOf(FromManifest(m)) != nil) != c.has {
			t.Errorf("%q: has answer = %v", c.answer, !c.has)
		}
	}
	r, _ := Load("")
	a, _ := r.Get("claude")
	if ans := AnswerOf(a); ans == nil || ans.Rule != "blocked-question" || ans.TextOption == "" {
		t.Fatalf("claude answer %+v", ans)
	}
}

func TestManifestModels(t *testing.T) {
	const base = "manifest_version = 1\nname = \"a\"\n[launch]\ncommand = \"a\"\n"
	const args = "model_args = [\"--model\", \"{{.Model}}\"]\n"
	for _, c := range []struct {
		toml string
		n    int
		ok   bool
	}{
		{"", 0, true},
		{args + "[[models]]\nname = \"big\"\nabout = \"hard work\"\n[[models]]\nname = \"small-1.5\"\nabout = \"small edits\"\n", 2, true},
		{"[[models]]\nname = \"big\"\nabout = \"hard work\"\n", 0, false}, // no model_args
		{args + "[[models]]\nname = \"two words\"\nabout = \"x\"\n", 0, false},
		{args + "[[models]]\nname = \"big\"\n", 0, false}, // no about
		{args + "[[models]]\nname = \"big\"\nabout = \"a\\nb\"\n", 0, false},
		{args + "[[models]]\nname = \"big\"\nabout = \"x\"\n[[models]]\nname = \"big\"\nabout = \"y\"\n", 0, false},
	} {
		m, err := ParseManifest([]byte(base + c.toml))
		if (err == nil) != c.ok {
			t.Errorf("%q: err = %v, want ok %v", c.toml, err, c.ok)
			continue
		}
		if err == nil && len(ModelsOf(FromManifest(m))) != c.n {
			t.Errorf("%q: %d models", c.toml, len(m.Models))
		}
	}
	reg, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	claude, _ := reg.Get("claude")
	for _, name := range []string{"opus", "sonnet", "haiku"} {
		if _, ok := ManifestOf(claude).FindModel(name); !ok {
			t.Errorf("claude lacks %s", name)
		}
	}
	l, err := claude.Launch(LaunchSpec{Role: RoleThread, Cwd: "/w", RuntimeDir: "/r", AgentSID: "x", Model: "haiku"})
	if err != nil || !strings.Contains(strings.Join(l.Argv, " "), "--model haiku") {
		t.Fatalf("launch with a model: %v %q", err, l.Argv)
	}
}

// TestRuleKeys: only a blocked rule may answer its dialog with keys.
func TestRuleKeys(t *testing.T) {
	const base = "manifest_version = 1\nname = \"a\"\n[launch]\ncommand = \"a\"\n[[rules]]\nid = \"r\"\nregion = \"screen\"\ncontains = [\"x\"]\nkeys = \"3\"\n"
	if _, err := ParseManifest([]byte(base + "state = \"blocked\"\n")); err != nil {
		t.Errorf("blocked rule with keys: %v", err)
	}
	if _, err := ParseManifest([]byte(base + "state = \"idle\"\n")); err == nil {
		t.Error("an idle rule with keys passed")
	}
}
