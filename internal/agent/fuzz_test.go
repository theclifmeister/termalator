package agent

import (
	"encoding/json"
	"testing"
)

// FuzzParseManifest: user manifests come from disk and must never crash
// the server, only fail validation.
func FuzzParseManifest(f *testing.F) {
	data, err := builtin.ReadFile("manifests/claude.toml")
	if err != nil {
		f.Fatal(err)
	}
	f.Add(data)
	f.Add([]byte("manifest_version = 1\nname = \"x\"\n[launch]\ncommand = \"x\"\n"))
	f.Add([]byte("[[rules]]\nregex = \"(\"\n"))
	f.Fuzz(func(t *testing.T, data []byte) {
		m, err := ParseManifest(data)
		if err != nil {
			return
		}
		a := FromManifest(m)
		_, _ = a.Launch(LaunchSpec{AgentSID: "x", Kickoff: "k"})
		_, _, _ = a.Hook(HookEvent{Event: "Stop", Payload: map[string]any{}}, nil)
	})
}

// FuzzHookPayload drives the Claude manifest with arbitrary JSON payloads
// for every event it maps, the way `tm hook` would.
func FuzzHookPayload(f *testing.F) {
	r, err := Load("")
	if err != nil {
		f.Fatal(err)
	}
	a, _ := r.Get("claude")
	f.Add("PostToolUse", []byte(`{"tool_name":"TaskCreate","tool_input":{"subject":"a"},"tool_response":{"task":{"id":"1"}}}`))
	f.Add("PostToolUse", []byte(`{"tool_name":"TaskUpdate","tool_input":{"taskId":7,"status":"weird"}}`))
	f.Add("PostToolUse", []byte(`{"tool_name":"TodoWrite","tool_input":{"todos":[1,"x",{"status":3}]}}`))
	f.Add("SessionEnd", []byte(`{"reason":null,"agent_id":{}}`))
	f.Fuzz(func(t *testing.T, event string, payload []byte) {
		var p map[string]any
		if json.Unmarshal(payload, &p) != nil {
			return
		}
		sigs, _, _ := a.Hook(HookEvent{Event: event, Payload: p}, func() ([]byte, error) { return []byte("ctx"), nil })
		var list []Todo
		for _, s := range sigs {
			if s.Todo != nil {
				list = ApplyTodo(list, *s.Todo)
			}
		}
		_ = a.Sources().Hook.Trim(p)
	})
}

// FuzzSourceLines feeds arbitrary bytes to the status-file reader and the
// JSONL tail, which read files the agent writes.
func FuzzSourceLines(f *testing.F) {
	r, err := Load("")
	if err != nil {
		f.Fatal(err)
	}
	a, _ := r.Get("claude")
	src := a.Sources()
	f.Add([]byte(`{"status":"waiting","waitingFor":"permission prompt","version":"2.1.289","sessionId":"s"}`))
	f.Add([]byte(`{"type":"user","message":{"content":[{"type":"text","text":"[Request interrupted by user]"}]}}`))
	f.Add([]byte(`{"type":"system","subtype":"turn_duration"}`))
	f.Fuzz(func(t *testing.T, data []byte) {
		_, _ = src.StatusFile.Read(data, src)
		_, _ = src.JSONLTail.Line(data)
		_ = src.TodoSnapshot.Parse(map[string][]byte{"1.json": data})
	})
}
