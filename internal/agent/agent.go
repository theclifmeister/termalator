package agent

import (
	"context"
	"time"
)

// State is the harness-neutral state of an agent session.
type State string

const (
	StateUnknown State = "unknown" // no signal yet
	StateIdle    State = "idle"    // waiting for a prompt
	StateWorking State = "working" // running a turn
	StateBlocked State = "blocked" // waiting on a permission prompt or a question
	StateExited  State = "exited"  // the process or the agent session ended
)

// Role says why a session was started.
type Role string

const (
	RoleCoordinator Role = "coordinator"
	RoleThread      Role = "thread"
	RoleShell       Role = "shell" // a plain shell; no agent
)

// LaunchSpec is what the core asks an agent to start. All paths are absolute.
type LaunchSpec struct {
	Role       Role
	SessionID  string // termalator session id, exported as TERMALATOR_SESSION
	AgentSID   string // agent's own session id, pre-assigned when the agent allows it
	Cwd        string
	RuntimeDir string // per-session scratch dir owned by the server; generated files go here
	BriefPath  string // thread brief, or the coordinator role file
	Kickoff    string // the first user prompt, e.g. "Read your brief and do what it says."
	Resume     bool   // resume AgentSID instead of starting fresh
	Yolo       bool   // skip the agent's own permission prompts (project setting)
	Model      string // optional
	TMBin      string // absolute path of the running tm binary, for hooks
	Socket     string // server socket path
}

// Launch is the agent's answer: what to exec in the PTY, and which files to
// write first (paths relative to LaunchSpec.RuntimeDir).
type Launch struct {
	Argv  []string
	Env   []string // KEY=VALUE, added to the session environment
	Files map[string][]byte
}

// ProcessInfo describes the foreground process of a PTY, used to recognise
// an agent started by hand in a shell session.
type ProcessInfo struct {
	Argv []string // already unwrapped from node/bun/sh -c by the core
}

// HookEvent is one structured event delivered by `tm hook` (or by a Go
// channel such as a protocol client) for a session.
type HookEvent struct {
	Agent   string
	Event   string         // the harness's event name, e.g. "PermissionRequest"
	Payload map[string]any // the harness's JSON payload, unmodified
	Seq     uint64         // per-source sequence number; stale events are dropped
	At      time.Time
}

// Signal is a state observation from one source. The core arbitrates
// signals from all sources into the session state (docs/SPEC.md §8.4).
type Signal struct {
	Source    string // "hook", "screen", "exit", "report", or a channel name
	State     State
	Reason    string // e.g. "permission", "question"; free text for the dashboard
	AgentSID  string // set when the event reveals the agent's session id
	Seq       uint64
	At        time.Time
	Transient bool // an edge (e.g. a tool call) rather than a level
}

// HookResult is what `tm hook` prints back to the harness, if anything
// (for example Claude's SessionStart additionalContext).
type HookResult struct {
	Stdout   []byte
	ExitCode int
}

// Injector says how follow-up prompts reach a running session.
type Injector string

const (
	InjectPaste   Injector = "paste"   // bracketed paste, then Enter, only while idle (core does this)
	InjectChannel Injector = "channel" // structured, via Agent.Prompt
	InjectNone    Injector = "none"
)

// Agent is the whole contract between the core and one agent harness.
//
// The manifest-backed implementation (FromManifest) covers every method
// from data. A Go agent embeds it and overrides only what it must.
type Agent interface {
	// Name is the manifest name, e.g. "claude". It is the value of --agent.
	Name() string

	// Identify reports whether a foreground process is this agent.
	Identify(p ProcessInfo) bool

	// Launch builds argv, env and generated files (hook plugin, extension,
	// settings) for a new or resumed session. Brief injection and context
	// re-injection after clear/compact are wired here, through launch args
	// and the generated hook files.
	Launch(spec LaunchSpec) (Launch, error)

	// Hook maps one structured event to zero or more signals, and to the
	// output the harness expects back (context re-injection goes here).
	// ctxFn renders `tm context` (coordinator) or the brief pointer
	// (thread) on demand, so agents never build context themselves.
	Hook(ev HookEvent, ctxFn func() ([]byte, error)) ([]Signal, HookResult, error)

	// Rules returns the screen rules that cross-check hook state. The
	// core's rule engine (package detect) evaluates them; agents never
	// read the screen directly.
	Rules() []Rule

	// Injector says how Prompt is delivered.
	Injector() Injector

	// Prompt delivers text through a structured channel. Only called when
	// Injector() == InjectChannel.
	Prompt(ctx context.Context, sessionID, text string) error
}

// Rule is one screen rule. It is plain data, evaluated by package detect.
type Rule struct {
	ID       string   `toml:"id"`
	State    State    `toml:"state"`
	Reason   string   `toml:"reason"`
	Priority int      `toml:"priority"`
	Region   string   `toml:"region"`   // "title", "bottom:N", "screen"
	Contains []string `toml:"contains"` // all must appear
	Regex    string   `toml:"regex"`    // optional, RE2, multiline
	Not      []string `toml:"not"`      // none may appear
}
