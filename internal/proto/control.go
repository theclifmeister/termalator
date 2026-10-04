package proto

import (
	"encoding/json"
	"fmt"
	"time"
)

// Error codes of control responses. Clients match on these, never on
// messages.
const (
	ErrUnknownMethod  = "unknown-method"
	ErrBadParams      = "bad-params"
	ErrUnknownSession = "unknown-session"
	ErrRefused        = "refused"
	ErrInternal       = "internal"
)

// Error is a structured failure.
type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *Error) Error() string { return e.Code + ": " + e.Message }

// Errorf builds an *Error.
func Errorf(code, format string, args ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...)}
}

// Request is one control call.
type Request struct {
	ID     int64           `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params,omitempty"`
}

// Response answers the Request with the same ID.
type Response struct {
	ID     int64           `json:"id"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *Error          `json:"error,omitempty"`
}

// Control methods served since M1.
const (
	MethodPing         = "ping"
	MethodServerStatus = "server.status"
	MethodServerStop   = "server.stop"
	MethodSessionList  = "session.list"
	MethodSessionStart = "session.start"
	MethodSessionStop  = "session.stop"
	MethodSessionRead  = "session.read"
	MethodSessionKeys  = "session.keys"
)

// Control methods of the agent layer (M3).
const (
	MethodSessionPrompt = "session.prompt"
	MethodSessionWait   = "session.wait"
	MethodAgentList     = "agent.list"
	MethodAgentReload   = "agent.reload"
	MethodAgentExplain  = "agent.explain"
	MethodHookEvent     = "hook.event"
)

// ServerStatus is the result of server.status.
type ServerStatus struct {
	PID      int       `json:"pid"`
	Version  string    `json:"version"`
	Build    string    `json:"build"`
	Protocol int       `json:"protocol"`
	Started  time.Time `json:"started"`
	Sessions int       `json:"sessions"`
	Socket   string    `json:"socket"`
	Home     string    `json:"home"`
	// PreviousShutdown is "clean", "crash" or "" (first start).
	PreviousShutdown string `json:"previous_shutdown,omitempty"`
	// Lost lists sessions of the previous server that were not restored.
	Lost []string `json:"lost,omitempty"`
	// Resumed lists agent sessions of the previous server that were
	// relaunched with their agent session ids (docs/SPEC.md §3.6).
	Resumed []string `json:"resumed,omitempty"`
}

// ServerStopParams are the params of server.stop.
type ServerStopParams struct {
	// Yes stops the server even while agent sessions run.
	Yes bool `json:"yes,omitempty"`
}

// Session roles.
const (
	RoleShell       = "shell"
	RoleCoordinator = "coordinator"
	RoleThread      = "thread"
)

// SessionInfo describes one hosted session.
type SessionInfo struct {
	ID      string    `json:"id"`
	Role    string    `json:"role"`
	Agent   string    `json:"agent,omitempty"`
	Project string    `json:"project,omitempty"`
	Thread  string    `json:"thread,omitempty"`
	Argv    []string  `json:"argv"`
	Cwd     string    `json:"cwd"`
	PID     int       `json:"pid"`
	Cols    uint16    `json:"cols"`
	Rows    uint16    `json:"rows"`
	Title   string    `json:"title,omitempty"`
	Created time.Time `json:"created"`
	Clients int       `json:"clients"`

	// Agent state (docs/SPEC.md §8.4), for agent sessions and for shells
	// whose foreground job was identified as an agent.
	State        string `json:"state,omitempty"`
	Reason       string `json:"reason,omitempty"`
	StateSources string `json:"state_sources,omitempty"`
	AgentSID     string `json:"agent_session_id,omitempty"`
	Identified   bool   `json:"identified,omitempty"` // found by process, not launched
	TodosDone    int    `json:"todos_done,omitempty"`
	TodosTotal   int    `json:"todos_total,omitempty"`
	Current      string `json:"current,omitempty"` // the in-progress todo
	Queued       int    `json:"queued_prompts,omitempty"`
}

// SessionStartParams are the params of session.start.
type SessionStartParams struct {
	// Argv defaults to the user's login shell.
	Argv []string `json:"argv,omitempty"`
	// Cwd must be an absolute directory; it defaults to the home directory.
	Cwd  string `json:"cwd,omitempty"`
	Cols uint16 `json:"cols,omitempty"`
	Rows uint16 `json:"rows,omitempty"`

	// Agent starts the named agent (docs/SPEC.md §8) instead of Argv.
	Agent string `json:"agent,omitempty"`
	// Role is coordinator, thread or shell (the default). It decides the
	// access policy and the context re-injected after a clear.
	Role    string `json:"role,omitempty"`
	Project string `json:"project,omitempty"`
	Thread  string `json:"thread,omitempty"`
	// Brief is a file attached as the agent's system prompt, if the agent
	// supports it; Kickoff is the first prompt.
	Brief   string `json:"brief,omitempty"`
	Kickoff string `json:"kickoff,omitempty"`
	Yolo    bool   `json:"yolo,omitempty"`
	Model   string `json:"model,omitempty"`
	// ResumeSID resumes this agent session id instead of starting fresh
	// (tm thread restart); Kickoff is then not sent.
	ResumeSID string `json:"resume_sid,omitempty"`
}

// SessionStartResult is the result of session.start.
type SessionStartResult struct {
	Session SessionInfo `json:"session"`
}

// SessionListResult is the result of session.list.
type SessionListResult struct {
	Sessions []SessionInfo `json:"sessions"`
	// Alerts counts the server's notifications (a session blocked, a
	// thread reported); a client rings its bell when it goes up
	// (docs/SPEC.md §4).
	Alerts uint64 `json:"alerts"`
}

// SessionIDParams name one session.
type SessionIDParams struct {
	ID string `json:"id"`
}

// SessionReadParams are the params of session.read.
type SessionReadParams struct {
	ID string `json:"id"`
	// Scrollback includes the scrollback above the visible rows.
	Scrollback bool `json:"scrollback,omitempty"`
}

// SessionReadResult is the result of session.read.
type SessionReadResult struct {
	Text string `json:"text"`
	Cols uint16 `json:"cols"`
	Rows uint16 `json:"rows"`
}

// SessionKeysParams are the params of session.keys: raw bytes written to
// the session's PTY as if typed.
type SessionKeysParams struct {
	ID   string `json:"id"`
	Data string `json:"data"`
}

// AttachRequest is the first line a client sends on an attach connection
// after the hello.
type AttachRequest struct {
	Attach AttachParams `json:"attach"`
}

// AttachParams name the session and the client's window size. The size is
// informational: attaching never resizes the pane (docs/SPEC.md §3.3).
type AttachParams struct {
	Session string `json:"session"`
	Cols    uint16 `json:"cols,omitempty"`
	Rows    uint16 `json:"rows,omitempty"`
}

// AttachReply is the server's answer; frames follow only if Error is nil.
type AttachReply struct {
	Attached *SessionInfo `json:"attached,omitempty"`
	Error    *Error       `json:"error,omitempty"`
}

// AppendFrame appends one encoded frame to b, the in-memory form of
// WriteFrame for queues that batch frames.
func AppendFrame(b []byte, t FrameType, payload []byte) []byte {
	n := len(payload)
	b = append(b, byte(t), byte(n>>24), byte(n>>16), byte(n>>8), byte(n))
	return append(b, payload...)
}

// SessionPromptParams are the params of session.prompt.
type SessionPromptParams struct {
	ID   string `json:"id"`
	Text string `json:"text"`
}

// SessionPromptResult says how the prompt went in: "channel" (delivered
// through the agent's structured channel) or "queued" (pasted once the
// agent is idle with an empty prompt box).
type SessionPromptResult struct {
	Via string `json:"via"`
}

// SessionWaitParams are the params of session.wait: block until the
// session's agent state is one of States (any change when empty), or
// until TimeoutMS passes.
type SessionWaitParams struct {
	ID        string   `json:"id"`
	States    []string `json:"states,omitempty"`
	TimeoutMS int      `json:"timeout_ms,omitempty"`
}

// SessionWaitResult is the state when session.wait returned.
type SessionWaitResult struct {
	State    string `json:"state"`
	Reason   string `json:"reason,omitempty"`
	TimedOut bool   `json:"timed_out,omitempty"`
}

// AgentInfo describes one known agent for agent.list.
type AgentInfo struct {
	Name       string   `json:"name"`
	Display    string   `json:"display,omitempty"`
	Source     string   `json:"source"` // builtin:… or a file path
	Command    string   `json:"command"`
	Injector   string   `json:"injector"`
	Tested     []string `json:"tested_versions,omitempty"`
	Unenforced bool     `json:"unenforced,omitempty"` // renders no access policy
}

// AgentListResult is the result of agent.list and agent.reload.
type AgentListResult struct {
	Agents []AgentInfo `json:"agents"`
	// Errors lists broken user manifests, which were skipped.
	Errors []string `json:"errors,omitempty"`
}

// HookEventParams are what `tm hook` sends: one event of one session.
type HookEventParams struct {
	Session string         `json:"session"`
	Agent   string         `json:"agent"`
	Event   string         `json:"event"`
	PPID    int            `json:"ppid,omitempty"`
	At      time.Time      `json:"at"`
	Payload map[string]any `json:"payload"`
}

// HookEventResult is what `tm hook` prints back to the harness.
type HookEventResult struct {
	Stdout string `json:"stdout,omitempty"`
}

// Control methods of projects and threads (M6).
const (
	// MethodCLIRun runs a project command (tm task, thread, report,
	// status, done, …) inside the server for an agent caller: the server
	// tells the caller from the peer pid (docs/SPEC.md §11.1) and writes
	// the project folder with its own permissions, which a sandboxed
	// thread lacks (§5.2).
	MethodCLIRun = "cli.run"
)

// CLIRunParams are the params of cli.run.
type CLIRunParams struct {
	Args  []string `json:"args"`
	Cwd   string   `json:"cwd"`
	Stdin []byte   `json:"stdin,omitempty"`
}

// CLIRunResult is the command's output and exit code.
type CLIRunResult struct {
	Stdout string `json:"stdout"`
	Stderr string `json:"stderr"`
	Code   int    `json:"code"`
}

// MethodCallerWho asks the server who the calling process is (M4,
// docs/SPEC.md §11.1): the server walks the peer pid's ancestors to a
// hosted session.
const MethodCallerWho = "caller.who"

// CallerInfo is the result of caller.who. Kind is "human", "coordinator"
// or "thread".
type CallerInfo struct {
	Kind    string `json:"kind"`
	Project string `json:"project,omitempty"`
	Thread  string `json:"thread,omitempty"`
}
