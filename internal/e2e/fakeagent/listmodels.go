package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"slices"
)

// listModels answers tm's models probe (docs/SPEC.md §8.2, Models), as
// the agent it plays would: Claude's SDK initialize over stream-json
// (-p --input-format stream-json …), Codex's app server (codex
// app-server), or a made-up agent's "models" command. The replies are
// the JSON lines of $FAKEAGENT_MODELS, each printed when a request with
// its id (JSON-RPC) or request_id (Claude's control request) arrives;
// without the file, a logged-in answer with two made-up models.
// $FAKEAGENT_MODELS_MODE "hang" reads and never answers, "garbage"
// prints a line that isn't the answer and exits. It reports whether
// args were a probe.
func listModels(args []string) bool {
	flavour := ""
	switch {
	case isCodex() && len(args) > 0 && args[0] == "app-server":
		flavour = "codex"
	case !isCodex() && slices.Contains(args, "-p") && slices.Contains(args, "stream-json"):
		flavour = "claude"
	case len(args) > 0 && args[0] == "models":
		flavour = "other"
	default:
		return false
	}
	logModels(args)
	switch os.Getenv("FAKEAGENT_MODELS_MODE") {
	case "hang":
		// Never answers; gone with its parent, as a real agent is.
		io.Copy(io.Discard, os.Stdin)
		return true
	case "garbage":
		fmt.Println(`{"id":3,"result":{"data":"not a list"},"type":"control_response","response":{"request_id":"tm-models","response":{"models":"not a list"}}}`)
		os.Exit(2)
	}
	replies := defaultReplies(flavour)
	if f := os.Getenv("FAKEAGENT_MODELS"); f != "" {
		data, err := os.ReadFile(f)
		if err != nil {
			fmt.Fprintln(os.Stderr, "fakeagent:", err)
			os.Exit(1)
		}
		replies = nil
		for _, l := range splitLines(data) {
			var obj map[string]any
			if json.Unmarshal(l, &obj) == nil {
				replies = append(replies, obj)
			}
		}
	}
	sc := bufio.NewScanner(os.Stdin)
	sc.Buffer(nil, 1<<20)
	for sc.Scan() {
		var req map[string]any
		if json.Unmarshal(sc.Bytes(), &req) != nil {
			continue
		}
		key := idOf(req)
		if key == "" {
			continue
		}
		for _, r := range replies {
			if idOf(r) == key {
				b, _ := json.Marshal(r)
				fmt.Println(string(b))
			}
		}
	}
	return true
}

// idOf is a request's or reply's id: JSON-RPC's id, or Claude's
// request_id (a control response carries it under response).
func idOf(obj map[string]any) string {
	if v, ok := obj["id"]; ok && v != nil {
		return fmt.Sprint(v)
	}
	if v, ok := obj["request_id"].(string); ok {
		return v
	}
	if r, ok := obj["response"].(map[string]any); ok {
		if v, ok := r["request_id"].(string); ok {
			return v
		}
	}
	return ""
}

func defaultReplies(flavour string) []map[string]any {
	switch flavour {
	case "claude":
		return []map[string]any{{"type": "control_response", "response": map[string]any{"subtype": "success", "request_id": "tm-models",
			"response": map[string]any{
				"models": []any{
					map[string]any{"value": "default", "resolvedModel": "fake-big", "displayName": "Default"},
					map[string]any{"value": "fake-big", "resolvedModel": "fake-big", "displayName": "Fake Big", "description": "for big work", "supportsAutoMode": true},
					map[string]any{"value": "fake-small", "resolvedModel": "fake-small", "displayName": "Fake Small", "description": "for small work", "supportsAutoMode": false},
				},
				"account": map[string]any{"subscriptionType": "Fake Plan", "apiProvider": "firstParty", "tokenSource": "claude.ai"},
			}}}}
	case "codex":
		return []map[string]any{
			{"id": 1, "result": map[string]any{"userAgent": "fake"}},
			{"id": 2, "result": map[string]any{"account": map[string]any{"type": "chatgpt", "planType": "plus"}, "requiresOpenaiAuth": true}},
			{"id": 3, "result": map[string]any{"data": []any{
				map[string]any{"id": "fake-codex-1", "displayName": "Fake Codex 1", "description": "the default", "hidden": false, "isDefault": true},
				map[string]any{"id": "fake-codex-2", "displayName": "Fake Codex 2", "description": "another", "hidden": false, "isDefault": false},
				map[string]any{"id": "fake-hidden", "hidden": true, "isDefault": false},
			}}},
		}
	}
	return nil
}

// logModels notes the probe in the fake's log, for tests to see it ran.
func logModels(args []string) {
	path := os.Getenv("FAKEAGENT_LOG")
	if path == "" {
		return
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	b, _ := json.Marshal(map[string]any{"event": "models", "args": args})
	f.Write(append(b, '\n'))
}

func splitLines(data []byte) [][]byte {
	var out [][]byte
	start := 0
	for i, c := range data {
		if c == '\n' {
			out = append(out, data[start:i])
			start = i + 1
		}
	}
	if start < len(data) {
		out = append(out, data[start:])
	}
	return out
}
