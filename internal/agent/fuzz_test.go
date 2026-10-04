package agent

import (
	"encoding/json"
	"testing"
	"time"
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

// FuzzTracker feeds arbitrary sequences of hook events, status-file
// readings, transcript lines and screen results to the arbiter. It must
// never panic, always give a known state, and exit must stick.
func FuzzTracker(f *testing.F) {
	r, err := Load("")
	if err != nil {
		f.Fatal(err)
	}
	a, _ := r.Get("claude")
	src := a.Sources()
	events := []string{"SessionStart", "UserPromptSubmit", "PreToolUse", "PostToolUse", "PermissionRequest",
		"Notification", "Stop", "SubagentStart", "SubagentStop", "SessionEnd"}
	f.Add([]byte{0, 1, 2, 4, 6, 7, 6, 8, 9}, []byte(`{"tool_name":"Write","agent_id":"a","agent_type":"x"}`))
	f.Add([]byte{10, 11, 12, 13, 1, 3, 255}, []byte(`{"status":"busy","version":"2.1.1","type":"user"}`))
	f.Fuzz(func(t *testing.T, ops []byte, data []byte) {
		var p map[string]any
		json.Unmarshal(data, &p)
		now := time.Unix(0, 0)
		tr := NewTracker(func() time.Time { return now })
		exited := false
		for i, op := range ops {
			now = now.Add(time.Duration(op) * time.Millisecond)
			switch k := int(op) % 16; {
			case k < len(events):
				ev := HookEvent{Event: events[k], Payload: p, Seq: uint64(i + 1), At: now}
				sigs, _, _ := a.Hook(ev, nil)
				tr.Hook(ev, sigs)
			case k == 10:
				rd, err := src.StatusFile.Read(data, src)
				tr.Status(&rd, now, err)
			case k == 11:
				if s, ok := src.JSONLTail.Line(data); ok {
					tr.Tail(s)
				}
			case k == 12:
				tr.Screen(ScreenSignal{Rule: "r", State: []State{StateIdle, StateWorking, StateBlocked, StateUnknown}[int(op)%4]}, nil)
			case k == 13:
				tr.Exited("x")
				exited = true
			}
			st := tr.State().State
			switch st {
			case StateUnknown, StateIdle, StateWorking, StateBlocked, StateExited:
			default:
				t.Fatalf("state %q", st)
			}
			if exited && st != StateExited {
				t.Fatalf("state %s after exit", st)
			}
		}
		_ = tr.Explain()
	})
}
