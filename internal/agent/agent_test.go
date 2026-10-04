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
		Cwd: "/w", RuntimeDir: "/run/s1", BriefPath: "/w/.termalator/brief.md",
		Kickoff: "Read your brief.", TMBin: "/bin/tm", Socket: "/run/tm.sock",
	}
	l, err := a.Launch(spec)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"claude",
		"--plugin-dir", "/run/s1/claude-plugin",
		"--session-id", "uuid-1",
		"--append-system-prompt-file", "/w/.termalator/brief.md",
		"Read your brief."}
	if !reflect.DeepEqual(l.Argv, want) {
		t.Fatalf("argv\n got %q\nwant %q", l.Argv, want)
	}
	var hooks map[string]any
	if err := json.Unmarshal(l.Files["claude-plugin/hooks/hooks.json"], &hooks); err != nil {
		t.Fatalf("hooks.json is not JSON: %v\n%s", err, l.Files["claude-plugin/hooks/hooks.json"])
	}

	spec.Resume, spec.Yolo = true, true
	l, err = a.Launch(spec)
	if err != nil {
		t.Fatal(err)
	}
	want = []string{"claude",
		"--plugin-dir", "/run/s1/claude-plugin",
		"--append-system-prompt-file", "/w/.termalator/brief.md",
		"--resume", "uuid-1", "--dangerously-skip-permissions"}
	if !reflect.DeepEqual(l.Argv, want) {
		t.Fatalf("resume argv\n got %q\nwant %q", l.Argv, want)
	}
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
