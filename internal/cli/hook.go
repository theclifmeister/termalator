package cli

import (
	"bufio"
	"encoding/json"
	"io"
	"net"
	"os"
	"time"

	"github.com/theclifmeister/termilator/internal/agent"
	"github.com/theclifmeister/termilator/internal/proto"
	"github.com/theclifmeister/termilator/internal/server"
	"github.com/theclifmeister/termilator/internal/version"
)

// Hook deadlines (docs/SPEC.md §8.2). The hook runs synchronously inside
// the agent, so it must never hold the agent up, even with a wedged
// server.
var (
	hookDial     = 50 * time.Millisecond
	hookWrite    = 100 * time.Millisecond
	hookAck      = 250 * time.Millisecond // total, for events nobody answers
	hookResponse = 500 * time.Millisecond // total, when a response is expected
)

// maxHookPayload bounds what tm hook reads from the agent.
const maxHookPayload = 4 << 20

func init() { commands["hook"] = runHook }

// runHook is `tm hook --agent NAME`: the endpoint every generated hook
// file calls. It reads the payload on stdin, trims it with the agent's
// [hook] rules, sends it to the server over a stream connection and
// prints the server's response, if any. It always exits 0 and never
// writes to stderr: a failing hook shows up as an error in the agent.
func runHook(e *Env, args []string) error {
	name := ""
	for i := 0; i < len(args); i++ {
		switch a := args[i]; {
		case a == "--agent" && i+1 < len(args):
			name = args[i+1]
			i++
		case len(a) > 8 && a[:8] == "--agent=":
			name = a[8:]
		}
	}
	session := e.Getenv("TERMINATR_SESSION")
	data, _ := io.ReadAll(io.LimitReader(e.Stdin, maxHookPayload))
	if name == "" || session == "" {
		return nil
	}
	var payload map[string]any
	if json.Unmarshal(data, &payload) != nil {
		return nil
	}
	event, _ := payload["hook_event_name"].(string)
	respond := false
	paths, err := server.ResolvePaths()
	if err != nil {
		return nil
	}
	if reg, _ := agent.Load(paths.AgentsDir()); reg != nil {
		if a, ok := reg.Get(name); ok {
			payload = a.Sources().Hook.Trim(payload)
			if m := agent.ManifestOf(a); m != nil {
				for _, h := range m.Hooks {
					respond = respond || (h.Event == event && h.Respond != "")
				}
			}
		}
	}
	total := hookAck
	if respond {
		total = hookResponse
	}
	params := proto.HookEventParams{Session: session, Agent: name, Event: event,
		PPID: os.Getppid(), At: time.Now(), Payload: payload}
	if out := sendHook(paths.Socket, params, total); out != "" {
		io.WriteString(e.Stdout, out)
	}
	return nil
}

// sendHook delivers one event and returns the server's stdout for the
// agent. Every failure (no server, a stale socket, a wedged server, a
// protocol mismatch) is silent and returns "".
func sendHook(socket string, p proto.HookEventParams, total time.Duration) string {
	deadline := time.Now().Add(total)
	c, err := net.DialTimeout("unix", socket, hookDial)
	if err != nil {
		return ""
	}
	defer c.Close()
	c.SetWriteDeadline(time.Now().Add(hookWrite))
	c.SetReadDeadline(deadline)
	hello, _ := json.Marshal(proto.Hello{Protocol: proto.Protocol, Version: version.Version, Build: version.BuildID(), Kind: proto.KindHook})
	params, err := json.Marshal(p)
	if err != nil {
		return ""
	}
	req, _ := json.Marshal(proto.Request{ID: 1, Method: proto.MethodHookEvent, Params: params})
	// Hello and request in one write: one round trip less.
	if _, err := c.Write(append(append(append(hello, '\n'), req...), '\n')); err != nil {
		return ""
	}
	br := bufio.NewReader(c)
	var srv proto.Hello
	if line, err := br.ReadBytes('\n'); err != nil || json.Unmarshal(line, &srv) != nil {
		return ""
	}
	var resp proto.Response
	line, err := br.ReadBytes('\n')
	if err != nil || json.Unmarshal(line, &resp) != nil || resp.Error != nil {
		return ""
	}
	var res proto.HookEventResult
	if json.Unmarshal(resp.Result, &res) != nil {
		return ""
	}
	return res.Stdout
}
