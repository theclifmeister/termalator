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
	// MethodServerKeychain reports whether the server's sessions can reach
	// the macOS login keychain (docs/SPEC.md §3.1).
	MethodServerKeychain = "server.keychain"
)

// Control methods of the agent layer (M3).
const (
	MethodSessionPrompt = "session.prompt"
	MethodSessionWait   = "session.wait"
	MethodAgentList     = "agent.list"
	MethodAgentReload   = "agent.reload"
	MethodAgentExplain  = "agent.explain"
	MethodHookEvent     = "hook.event"
	// MethodSessionRemote turns a coordinator's remote control on or off
	// in the running session (docs/SPEC.md §8.2).
	MethodSessionRemote = "session.remote"
	// MethodSessionAdopt makes a plain agent session a project thread's
	// (docs/SPEC.md §9, Adopt).
	MethodSessionAdopt = "session.adopt"
)

// SessionAdoptParams are the params of session.adopt: the session, and
// the project and thread it now belongs to. Brief is the thread's brief,
// attached as the system prompt when the server resumes it.
type SessionAdoptParams struct {
	ID      string `json:"id"`
	Project string `json:"project"`
	Thread  string `json:"thread"`
	Brief   string `json:"brief,omitempty"`
}

// ClosedRestarting is the FrameClosed reason of a session that the server
// relaunches at once under the same id (a remote control change): a
// client attaches to it again instead of closing the pane.
const ClosedRestarting = "session restarting"

// SessionRemoteParams are the params of session.remote.
type SessionRemoteParams struct {
	ID string `json:"id"`
	On bool   `json:"on"`
}

// How session.remote applied the change.
const (
	RemoteUnchanged = "unchanged" // it already was so
	RemotePrompted  = "prompted"  // the agent's in-session text was pasted
	RemoteRestarted = "restarted" // the agent was resumed with or without the flag
)

// SessionRemoteResult is the result of session.remote.
type SessionRemoteResult struct {
	RemoteControl bool   `json:"remote_control"`
	How           string `json:"how"`
	// Held: turned off while the project's setting keeps coordinators'
	// remote control on; the ticker leaves it off until the coordinator
	// is started anew (docs/SPEC.md §11.2).
	Held bool `json:"held,omitempty"`
}

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

// KeychainStatus is the result of server.keychain.
type KeychainStatus struct {
	// Checked is false where there is no keychain to check (Linux).
	Checked bool `json:"checked"`
	// OK means the server's sessions can reach the login keychain.
	OK bool `json:"ok"`
	// OverSSH means the server's environment is an SSH login's.
	OverSSH bool `json:"over_ssh,omitempty"`
	// Session is launchd's name for the server's session: Aqua for the
	// desktop's, Background or StandardIO for an SSH login's.
	Session string `json:"session,omitempty"`
	Detail  string `json:"detail,omitempty"`
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
	// QueuedSince is when the oldest queued prompt was queued.
	QueuedSince time.Time `json:"queued_since,omitzero"`
	// QueueHeld says why the next queued prompt isn't pasted although the
	// agent is idle ("prompt box not empty", "dialog on screen"), since
	// QueueHeldSince. The server resolves a prompt held for its bound
	// (docs/SPEC.md §8.6), so this never lasts.
	QueueHeld      string    `json:"queue_held,omitempty"`
	QueueHeldSince time.Time `json:"queue_held_since,omitzero"`
	// RemoteControl: the agent is reachable from another device.
	RemoteControl bool `json:"remote_control,omitempty"`
	// RemoteHeld: the user turned the coordinator's remote control off
	// (session.remote); the ticker doesn't turn it back on until the
	// coordinator is started anew.
	RemoteHeld bool `json:"remote_held,omitempty"`
}

// QueueNotice is how long a queued prompt must be held while its agent is
// idle before listings, tm doctor and tm context call it out: shorter
// holds are a box being typed into or a screen not yet re-read.
const QueueNotice = time.Minute

// QueueNote says why the session's next queued prompt is held, e.g.
// "held 12m: prompt box not empty", once it has been for QueueNotice;
// "" otherwise.
func (s SessionInfo) QueueNote(now time.Time) string {
	if s.Queued == 0 || s.QueueHeld == "" || now.Sub(s.QueueHeldSince) < QueueNotice {
		return ""
	}
	return fmt.Sprintf("held %s: %s", now.Sub(s.QueueHeldSince).Truncate(time.Second), s.QueueHeld)
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
	// RemoteControl starts a coordinator reachable from another device,
	// named after its project; the agent's manifest must support it.
	RemoteControl bool `json:"remote_control,omitempty"`
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
