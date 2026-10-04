package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
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

func TestClaudeLaunch(t *testing.T) {
	a := claude(t)
	spec := LaunchSpec{
		Role: RoleThread, SessionID: "s1", AgentSID: "uuid-1",
		Cwd: "/w", RuntimeDir: "/run/s1", BriefPath: "/h/.termalator/projects/p/threads/t-0001/brief.md",
		Kickoff: "Read your brief.", TMBin: "/bin/tm", Socket: "/run/tm.sock",
		Access: Access{Read: []string{"/h/.termalator/projects/p"}, NoWrite: []string{"/h/.termalator/projects/p"}},
	}
	l, err := a.Launch(spec)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"claude",
		"--plugin-dir", "/run/s1/claude-plugin",
		"--settings", "/run/s1/claude-settings.json",
		"--session-id", "uuid-1",
		"--append-system-prompt-file", "/h/.termalator/projects/p/threads/t-0001/brief.md",
		"Read your brief."}
	if !reflect.DeepEqual(l.Argv, want) {
		t.Fatalf("argv\n got %q\nwant %q", l.Argv, want)
	}
	var hooks map[string]any
	if err := json.Unmarshal(l.Files["claude-plugin/hooks/hooks.json"], &hooks); err != nil {
		t.Fatalf("hooks.json is not JSON: %v\n%s", err, l.Files["claude-plugin/hooks/hooks.json"])
	}

	settings := parseSettings(t, l.Files["claude-settings.json"])
	if want := []string{"Read(//h/.termalator/projects/p/**)"}; !reflect.DeepEqual(settings.Permissions.Allow, want) {
		t.Fatalf("allow = %q, want %q", settings.Permissions.Allow, want)
	}
	if want := []string{"Edit(//h/.termalator/projects/p/**)", "Write(//h/.termalator/projects/p/**)"}; !reflect.DeepEqual(settings.Permissions.Deny, want) {
		t.Fatalf("deny = %q, want %q", settings.Permissions.Deny, want)
	}
	if !settings.Sandbox.Enabled || !reflect.DeepEqual(settings.Sandbox.Network.AllowUnixSockets, []string{"/run/tm.sock"}) {
		t.Fatalf("thread sandbox = %+v", settings.Sandbox)
	}

	coord := spec
	coord.Role, coord.Access = RoleCoordinator, Access{Read: []string{"/h/.termalator/worktrees/p"}}
	l, err = a.Launch(coord)
	if err != nil {
		t.Fatal(err)
	}
	settings = parseSettings(t, l.Files["claude-settings.json"])
	if settings.Sandbox.Enabled || len(settings.Permissions.Deny) != 0 {
		t.Fatalf("coordinator must not get the thread sandbox or deny rules: %+v", settings)
	}

	spec.Resume, spec.Yolo = true, true
	l, err = a.Launch(spec)
	if err != nil {
		t.Fatal(err)
	}
	want = []string{"claude",
		"--plugin-dir", "/run/s1/claude-plugin",
		"--settings", "/run/s1/claude-settings.json",
		"--append-system-prompt-file", "/h/.termalator/projects/p/threads/t-0001/brief.md",
		"--resume", "uuid-1", "--dangerously-skip-permissions"}
	if !reflect.DeepEqual(l.Argv, want) {
		t.Fatalf("resume argv\n got %q\nwant %q", l.Argv, want)
	}
}

type claudeSettings struct {
	Permissions struct{ Allow, Deny []string }
	Sandbox     struct {
		Enabled bool
		Network struct{ AllowUnixSockets []string }
	}
}

func parseSettings(t *testing.T, b []byte) claudeSettings {
	t.Helper()
	var s claudeSettings
	if err := json.Unmarshal(b, &s); err != nil {
		t.Fatalf("claude-settings.json is not JSON: %v\n%s", err, b)
	}
	return s
}

func TestClaudeHooks(t *testing.T) {
	a := claude(t)
	sig := func(event string, payload map[string]any) []Signal {
		s, _, err := a.Hook(HookEvent{Event: event, Payload: payload}, nil)
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	if s := sig("PermissionRequest", map[string]any{"tool_name": "Bash"}); len(s) != 1 || s[0].State != StateBlocked {
		t.Fatalf("PermissionRequest -> %+v", s)
	}
	if s := sig("PreToolUse", map[string]any{"tool_name": "AskUserQuestion"}); len(s) != 2 || s[0].Reason != "question" {
		t.Fatalf("AskUserQuestion -> %+v", s)
	}
	if s := sig("Stop", map[string]any{"agent_id": "sub-1"}); len(s) != 0 {
		t.Fatalf("subagent Stop must be ignored, got %+v", s)
	}

	_, res, err := a.Hook(HookEvent{Event: "SessionStart", Payload: map[string]any{"session_id": "x", "source": "clear"}},
		func() ([]byte, error) { return []byte("# Context\n\"quoted\""), nil })
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

func TestClaudeTodos(t *testing.T) {
	a := claude(t)
	payload := map[string]any{
		"tool_name": "TodoWrite",
		"tool_input": map[string]any{"todos": []any{
			map[string]any{"content": "Reproduce", "status": "completed", "activeForm": "Reproducing"},
			map[string]any{"content": "Fix", "status": "in_progress", "activeForm": "Fixing"},
			map[string]any{"content": "Open a PR", "status": "pending", "activeForm": "Opening a PR"},
		}},
	}
	sigs, _, err := a.Hook(HookEvent{Event: "PostToolUse", Payload: payload}, nil)
	if err != nil {
		t.Fatal(err)
	}
	var got *[]Todo
	for _, s := range sigs {
		if s.Todos != nil {
			got = s.Todos
		}
	}
	want := []Todo{{"Reproduce", TodoCompleted}, {"Fix", TodoInProgress}, {"Open a PR", TodoPending}}
	if got == nil || !reflect.DeepEqual(*got, want) {
		t.Fatalf("todos = %+v, want %+v", got, want)
	}

	// A subagent's todo list is not the thread's.
	payload["agent_id"] = "sub-1"
	sigs, _, _ = a.Hook(HookEvent{Event: "PostToolUse", Payload: payload}, nil)
	if len(sigs) != 0 {
		t.Fatalf("subagent todos must be ignored, got %+v", sigs)
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

	r, err := Load(dir)
	if err == nil {
		t.Fatal("want an error for wrong.toml")
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
