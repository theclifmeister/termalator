package cli

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestMCPServes(t *testing.T) {
	in := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26"}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"status","arguments":{"percent":40}}}`,
		`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"done"}}`,
		`{"jsonrpc":"2.0","id":5,"method":"nope"}`,
		`not json`,
	}, "\n") + "\n"
	var calls []string
	call := func(name string, input json.RawMessage) (string, bool) {
		calls = append(calls, name+" "+string(input))
		return "out " + name, name == "done"
	}
	var out strings.Builder
	if err := runMCP(strings.NewReader(in), &out, call); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(calls, "|"); got != `status {"percent":40}|done {}` {
		t.Errorf("calls = %q", got)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 6 { // the notification gets no answer
		t.Fatalf("%d replies: %s", len(lines), out.String())
	}
	var r struct {
		ID     json.RawMessage `json:"id"`
		Result struct {
			ProtocolVersion string    `json:"protocolVersion"`
			Tools           []mcpTool `json:"tools"`
			IsError         bool      `json:"isError"`
			Content         []struct{ Text string }
		}
		Error *rpcErr
	}
	json.Unmarshal([]byte(lines[0]), &r)
	if r.Result.ProtocolVersion != "2025-03-26" {
		t.Errorf("initialize: %s", lines[0])
	}
	r.Result.Tools = nil
	json.Unmarshal([]byte(lines[1]), &r)
	var names []string
	for _, tl := range r.Result.Tools {
		names = append(names, tl.Name)
	}
	if strings.Join(names, ",") != "report,status,steps,done" {
		t.Errorf("tools = %v", names)
	}
	r.Result.Content = nil
	json.Unmarshal([]byte(lines[3]), &r)
	if !r.Result.IsError || r.Result.Content[0].Text != "out done" {
		t.Errorf("done: %s", lines[3])
	}
	json.Unmarshal([]byte(lines[4]), &r)
	if r.Error == nil || r.Error.Code != -32601 {
		t.Errorf("unknown method: %s", lines[4])
	}
	if !strings.Contains(lines[5], "-32700") {
		t.Errorf("bad json: %s", lines[5])
	}
}
