// tmhook is the hook command Claude Code runs for every subscribed event.
//
// It reads the hook payload from stdin, wraps it with the pane id, a
// per-process sequence stamp and a timestamp, and sends it as one datagram to
// the daemon's unix socket. It never blocks Claude: every step has a short
// deadline, every error is swallowed, and the exit code is always 0.
//
// For SessionStart it can also print additionalContext so the brief or
// project context is re-injected after startup, /clear, resume and compaction.
// The context comes from the daemon (request/response on a stream socket) and
// falls back to $TERMALATOR_CONTEXT_FILE when the daemon is down.
package main

import (
	"encoding/json"
	"io"
	"net"
	"os"
	"strconv"
	"time"
)

func main() {
	defer os.Exit(0)
	start := time.Now()

	payload, _ := io.ReadAll(io.LimitReader(os.Stdin, 4<<20))
	var p map[string]any
	_ = json.Unmarshal(payload, &p)
	event, _ := p["hook_event_name"].(string)

	env := map[string]any{
		"pane":     os.Getenv("TERMALATOR_PANE_ID"),
		"sent_ns":  time.Now().UnixNano(),
		"ppid":     os.Getppid(),
		"argv_ev":  argOr(1, ""),
		"payload":  trimmed(p, payload),
		"start_ns": start.UnixNano(),
		// Claude's built-in message-injection socket, if the session has one.
		"msg_sock":  os.Getenv("CLAUDE_CODE_MESSAGING_SOCKET"),
		"msg_token": os.Getenv("CLAUDE_CODE_MESSAGING_TOKEN"),
	}
	msg, _ := json.Marshal(env)

	sock := os.Getenv("TERMALATOR_SOCK")
	if sock != "" {
		send(sock, msg)
	}

	if event == "SessionStart" {
		if ctx := contextFor(p); ctx != "" {
			out, _ := json.Marshal(map[string]any{
				"hookSpecificOutput": map[string]any{
					"hookEventName":     "SessionStart",
					"additionalContext": ctx,
				},
			})
			os.Stdout.Write(out)
		}
	}
	if os.Getenv("TERMALATOR_HOOK_TIMING") != "" {
		f, err := os.OpenFile(os.Getenv("TERMALATOR_HOOK_TIMING"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err == nil {
			f.WriteString(event + " " + strconv.FormatInt(time.Since(start).Microseconds(), 10) + "us\n")
			f.Close()
		}
	}
}

// send writes the envelope over a stream socket (default) or one datagram
// (TERMALATOR_TRANSPORT=dgram; macOS caps datagrams at 2048 bytes, see
// FINDINGS). A missing or dead daemon fails immediately (ENOENT /
// ECONNREFUSED); a stalled one costs at most the 100ms deadline.
func send(sock string, msg []byte) {
	network := "unix"
	if os.Getenv("TERMALATOR_TRANSPORT") == "dgram" {
		network = "unixgram"
	}
	c, err := net.DialTimeout(network, sock, 50*time.Millisecond)
	if err != nil {
		return
	}
	defer c.Close()
	c.SetWriteDeadline(time.Now().Add(100 * time.Millisecond))
	c.Write(append(msg, '\n'))
}

// trimmed keeps the fields a state reducer needs and drops the bulky ones
// (tool_input, tool_response); prompt and last_assistant_message are cut to
// 500 bytes. TERMALATOR_FULL_PAYLOAD=1 sends everything (spike scenarios).
func trimmed(p map[string]any, raw []byte) json.RawMessage {
	if os.Getenv("TERMALATOR_FULL_PAYLOAD") != "" || p == nil {
		return json.RawMessage(orEmpty(raw))
	}
	keep := map[string]any{}
	for _, k := range []string{"hook_event_name", "session_id", "prompt_id", "transcript_path", "cwd",
		"permission_mode", "agent_id", "agent_type", "source", "reason", "notification_type", "message",
		"tool_name", "tool_use_id", "stop_reason", "trigger", "model", "error", "is_interrupt"} {
		if v, ok := p[k]; ok {
			keep[k] = v
		}
	}
	// The task tools carry the agent's todo list (TaskCreate/TaskUpdate); keep
	// their input and response so the daemon can mirror the list.
	if tn, _ := p["tool_name"].(string); tn == "TaskCreate" || tn == "TaskUpdate" {
		keep["tool_input"] = p["tool_input"]
		keep["tool_response"] = p["tool_response"]
	}
	for _, k := range []string{"task_id", "task_subject", "task_description"} {
		if v, ok := p[k]; ok {
			keep[k] = v
		}
	}
	for _, k := range []string{"last_assistant_message", "prompt"} {
		if v, ok := p[k].(string); ok {
			if len(v) > 500 {
				v = v[:500]
			}
			keep[k] = v
		}
	}
	b, _ := json.Marshal(keep)
	return b
}

// contextFor asks the daemon for the context to inject; falls back to a file.
func contextFor(p map[string]any) string {
	if s := os.Getenv("TERMALATOR_CTX_SOCK"); s != "" {
		c, err := net.DialTimeout("unix", s, 100*time.Millisecond)
		if err == nil {
			defer c.Close()
			c.SetDeadline(time.Now().Add(500 * time.Millisecond))
			req, _ := json.Marshal(map[string]any{"pane": os.Getenv("TERMALATOR_PANE_ID"), "source": p["source"]})
			c.Write(append(req, '\n'))
			if uc, ok := c.(*net.UnixConn); ok {
				uc.CloseWrite()
			}
			b, err := io.ReadAll(io.LimitReader(c, 1<<20))
			if err == nil && len(b) > 0 {
				return string(b)
			}
		}
	}
	if f := os.Getenv("TERMALATOR_CONTEXT_FILE"); f != "" {
		b, err := os.ReadFile(f)
		if err == nil {
			return string(b)
		}
	}
	return ""
}

func argOr(i int, d string) string {
	if len(os.Args) > i {
		return os.Args[i]
	}
	return d
}

func orEmpty(b []byte) []byte {
	if !json.Valid(b) {
		q, _ := json.Marshal(string(b))
		return q
	}
	return b
}
