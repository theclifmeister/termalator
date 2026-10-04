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
}

// SessionStartParams are the params of session.start.
type SessionStartParams struct {
	// Argv defaults to the user's login shell.
	Argv []string `json:"argv,omitempty"`
	// Cwd must be an absolute directory; it defaults to the home directory.
	Cwd  string `json:"cwd,omitempty"`
	Cols uint16 `json:"cols,omitempty"`
	Rows uint16 `json:"rows,omitempty"`
}

// SessionStartResult is the result of session.start.
type SessionStartResult struct {
	Session SessionInfo `json:"session"`
}

// SessionListResult is the result of session.list.
type SessionListResult struct {
	Sessions []SessionInfo `json:"sessions"`
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
