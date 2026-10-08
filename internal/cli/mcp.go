package cli

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"

	"github.com/theclifmeister/terminatr/internal/proto"
	"github.com/theclifmeister/terminatr/internal/server"
	"github.com/theclifmeister/terminatr/internal/version"
)

// tm mcp (docs/SPEC.md §8.6, Mods): a thread's tools (report, status,
// steps, done) as an MCP server on stdio, for agents without the Claude
// mod that speak MCP, such as Codex. It is a thin bridge: each call goes
// to the server as tool.run, which tells the thread from the process
// tree (the agent started this process, so it is inside the session) and
// runs it as the mod socket's POST /v1/tools/{name} would.

const mcpProtocol = "2025-06-18"

type mcpTool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

func mcpLine(description string, maxLen int) map[string]any {
	m := map[string]any{"type": "string", "minLength": 1, "description": description}
	if maxLen > 0 {
		m["maxLength"] = maxLen
	}
	return m
}

func mcpLines(description string, maxLen int) map[string]any {
	return map[string]any{"type": "array", "items": mcpLine("One line.", maxLen), "description": description}
}

func mcpObject(props map[string]any, required ...string) map[string]any {
	m := map[string]any{"type": "object", "properties": props, "additionalProperties": false}
	if len(required) > 0 {
		m["required"] = required
	}
	return m
}

// mcpTools are the tools, as the mod registers them (mod/hooks/tools.ts).
var mcpTools = []mcpTool{
	{
		Name: "report",
		Description: "Hand in your report to the coordinator (what `tm report` does): whenever you finish or stop to wait. " +
			"Each field is a section of the report; the server checks the format and answers with the report number, or why it refused.",
		InputSchema: mcpObject(map[string]any{
			"pr":       map[string]any{"type": "string", "pattern": "^https://", "description": "The PR URL, if you opened one: https://github.com/<owner>/<repo>/pull/<n> or https://dev.azure.com/<org>/<project>/_git/<repo>/pullrequest/<n>."},
			"report":   map[string]any{"type": "string", "minLength": 1, "description": "The report itself, in Markdown: what you did, what you assumed, any repository outside the project you used. Subheadings are ### (## is reserved for the sections)."},
			"next":     mcpLines("What happens next: one imperative action per item, at most 100 characters each.", 100),
			"check":    mcpLines("Optional: how the user can see the change working (what to run, where to look), a few lines.", 0),
			"remember": mcpLines("Optional: lessons for the project, one per item.", 0),
			"attach":   mcpLines("Optional: files meant for the user, by path (relative to your worktree, or absolute).", 0),
		}, "report", "next"),
	},
	{
		Name: "status",
		Description: "Set your thread status (what `tm status` does). Use needs_you when you are blocked on the human; " +
			"percent and activity only when you have neither task steps nor a todo list.",
		InputSchema: mcpObject(map[string]any{
			"needs_you": mcpLine("The question or decision you are blocked on, for the human; one line, at most 300 characters.", 300),
			"percent":   map[string]any{"type": "integer", "minimum": 0, "maximum": 100, "description": "How far along you are, 0-100."},
			"activity":  mcpLine("What you are doing now, one line, at most 100 characters.", 100),
		}),
	},
	{
		Name: "steps",
		Description: "Work your task's steps (what `tm task steps` does): check the ones you finished, uncheck one done too early, " +
			"or add your plan as steps when the task has none. Steps are numbered from 1; the server answers what changed.",
		InputSchema: mcpObject(map[string]any{
			"check":   map[string]any{"type": "array", "items": map[string]any{"type": "integer", "minimum": 1}, "description": "Step numbers to check, in order."},
			"uncheck": map[string]any{"type": "array", "items": map[string]any{"type": "integer", "minimum": 1}, "description": "Step numbers to uncheck."},
			"add":     mcpLines("Steps to add at the end, one per item, in order.", 0),
		}),
	},
	{
		Name:        "done",
		Description: "Mark your task finished (what `tm done` does), once your report is in. It refuses when no report came since your last prompt.",
		InputSchema: mcpObject(map[string]any{
			"summary": mcpLine("Optional: a one-line summary, at most 80 characters.", 80),
		}),
	},
}

type rpcMsg struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcErr struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// runMCP serves MCP on in/out until in closes. call runs one tool.
func runMCP(in io.Reader, out io.Writer, call func(name string, input json.RawMessage) (text string, isErr bool)) error {
	enc := json.NewEncoder(out)
	reply := func(id json.RawMessage, result any, e *rpcErr) {
		m := map[string]any{"jsonrpc": "2.0", "id": id}
		if e != nil {
			m["error"] = e
		} else {
			m["result"] = result
		}
		enc.Encode(m)
	}
	sc := bufio.NewScanner(in)
	sc.Buffer(make([]byte, 0, 64<<10), 4<<20)
	for sc.Scan() {
		var m rpcMsg
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			reply(json.RawMessage("null"), nil, &rpcErr{-32700, "parse error"})
			continue
		}
		if len(m.ID) == 0 { // a notification
			continue
		}
		switch m.Method {
		case "initialize":
			var p struct {
				ProtocolVersion string `json:"protocolVersion"`
			}
			json.Unmarshal(m.Params, &p)
			ver := mcpProtocol
			if p.ProtocolVersion != "" {
				ver = p.ProtocolVersion
			}
			reply(m.ID, map[string]any{
				"protocolVersion": ver,
				"capabilities":    map[string]any{"tools": map[string]any{}},
				"serverInfo":      map[string]any{"name": "terminatr", "version": version.Version},
			}, nil)
		case "ping":
			reply(m.ID, map[string]any{}, nil)
		case "tools/list":
			reply(m.ID, map[string]any{"tools": mcpTools}, nil)
		case "tools/call":
			var p struct {
				Name      string          `json:"name"`
				Arguments json.RawMessage `json:"arguments"`
			}
			if err := json.Unmarshal(m.Params, &p); err != nil || p.Name == "" {
				reply(m.ID, nil, &rpcErr{-32602, "tools/call wants a name"})
				continue
			}
			if len(p.Arguments) == 0 || string(p.Arguments) == "null" {
				p.Arguments = json.RawMessage("{}")
			}
			text, isErr := call(p.Name, p.Arguments)
			reply(m.ID, map[string]any{
				"content": []map[string]any{{"type": "text", "text": text}},
				"isError": isErr,
			}, nil)
		default:
			reply(m.ID, nil, &rpcErr{-32601, "method not found: " + m.Method})
		}
	}
	return sc.Err()
}

// runMCPCmd is tm mcp.
func runMCPCmd(e *Env, args []string) error {
	if len(args) > 0 {
		return usagef("usage: tm mcp   (an MCP server on stdio: a thread's tools)")
	}
	call := func(name string, input json.RawMessage) (string, bool) {
		p, err := server.ResolvePaths()
		if err == nil {
			var c *server.Client
			if c, err = server.Connect(p, false); err == nil {
				defer c.Close()
				var res proto.ToolRunResult
				if err = c.Call(proto.MethodToolRun, proto.ToolRunParams{Name: name, Input: input}, &res); err == nil {
					if res.Text == "" && !res.IsError {
						res.Text = "done"
					}
					return res.Text, res.IsError
				}
			}
		}
		return fmt.Sprintf("tm server: %v", err), true
	}
	return runMCP(e.Stdin, e.Stdout, call)
}
