package agent

import (
	"context"
	"time"

	"github.com/theclifmeister/terminatr/internal/guard"
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

// Role says why a session was started: one of proto's agent roles
// (proto.RoleCoordinator, proto.RoleThread).
type Role string

// RoleThread is a thread's role; tm agent check renders a sample with it.
const RoleThread Role = "thread"

// LaunchSpec is what the core asks an agent to start. All paths are absolute.
type LaunchSpec struct {
	Role      Role
	SessionID string // terminatr session id, exported as TERMINATR_SESSION
	AgentSID  string // agent's own session id, pre-assigned when the agent allows it
	Cwd       string
	// RepoRoot is the main checkout's folder of the git repo Cwd is in
	// (a linked worktree's main repo, e.g. for a folder-trust key), and
	// GitDir the checkout's own git dir (<repo>/.git/worktrees/<name>
	// for a worktree). Both empty outside a repo.
	RepoRoot   string
	GitDir     string
	RuntimeDir string // per-session scratch dir owned by the server; generated files go here
	BriefPath  string // thread brief, or the coordinator role file
	Kickoff    string // the first user prompt, e.g. "Read your brief and do what it says."
	Resume     bool   // resume AgentSID instead of starting fresh; AgentSID must be set
	Yolo       bool   // skip the agent's own permission prompts (project setting)
	Model      string // optional
	// RemoteControl starts the agent reachable from another device
	// ([remote_control] args), listed there as RemoteName.
	RemoteControl bool
	RemoteName    string
	TMBin         string // absolute path of the running tm binary, for hooks
	Socket        string // server socket path; the agent's sandbox must allow it
	// Mods loads terminatr's own mod into the agent (a Modder), beside
	// its command hooks. The core sets it only when [mods] enabled is on
	// and the agent's version is at least ModsMinVersion.
	Mods bool

	// Access is the policy the core decided for this role. The manifest
	// turns it into the agent's own permission and sandbox settings.
	Access Access
}

// Access is a harness-neutral file access policy (docs/SPEC.md §5.2).
// Paths are absolute directories; each grant covers everything below it.
type Access struct {
	Read    []string // readable without a prompt
	Write   []string // writable besides the cwd, e.g. a worktree's git dir
	NoWrite []string // must never be written, even where a broader rule would allow it
	// NoWriteFiles are single files that must never be written, e.g.
	// config.toml, which holds the human's safety settings.
	NoWriteFiles []string
	// Commands are command prefixes that run without a permission prompt,
	// e.g. the coordinator's PR merge command, opt-in (§11.2).
	Commands []string
}

// Launch is the agent's answer: what to exec in the PTY, and which files to
// write first (paths relative to LaunchSpec.RuntimeDir).
type Launch struct {
	Argv []string
	Env  []string // KEY=VALUE, added to the session environment
	// Unset lists variables to remove from the inherited environment before
	// Env is added; a trailing * matches a prefix (see FilterEnv).
	Unset []string
	Files map[string][]byte
	// Kickoff says the kickoff prompt is in Argv: the agent starts on it
	// by itself.
	Kickoff bool
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

	// Counter, when set, is "+name" or "-name": one unit of background
	// activity (e.g. a background subagent) started or ended. CounterKey
	// identifies the unit so a repeated end can't go below zero. While any
	// counter is above zero, an idle state reads as working, reason
	// "background" (docs/SPEC.md §8.4).
	Counter    string
	CounterKey string

	// Todo is a change to the agent's own live todo list, when the event
	// carried one (docs/SPEC.md §7.3). The core applies it with ApplyTodo.
	Todo *TodoChange
}

// TodoStatus is the harness-neutral status of one todo item.
type TodoStatus string

const (
	TodoPending    TodoStatus = "pending"
	TodoInProgress TodoStatus = "in_progress"
	TodoCompleted  TodoStatus = "completed"
)

// Todo is one item of an agent's live todo list (docs/SPEC.md §7.3).
type Todo struct {
	ID         string     `json:"id,omitempty"` // the agent's own id, for diff-style updates
	Text       string     `json:"text"`
	ActiveText string     `json:"active_text,omitempty"` // shown while in progress, if the agent gives one
	Status     TodoStatus `json:"status"`
}

// TodoOp says how a TodoChange applies to the stored list.
type TodoOp string

const (
	TodoReplace TodoOp = "replace" // Items is the whole new list
	TodoUpsert  TodoOp = "upsert"  // Patch creates or updates one item by ID
	TodoReset   TodoOp = "reset"   // start a new, empty list
)

// TodoChange is one change to a todo list.
type TodoChange struct {
	Op    TodoOp
	Items []Todo    // replace
	Patch TodoPatch // upsert
}

// TodoPatch updates one item. Nil fields keep the stored value.
type TodoPatch struct {
	ID         string
	Text       *string
	ActiveText *string
	Status     *TodoStatus
	Default    TodoStatus // status for a new item when Status is nil
	Remove     bool
}

// HookResult is what `tm hook` prints back to the harness, if anything
// (for example Claude's SessionStart additionalContext).
type HookResult struct {
	Stdout []byte
}

// HookEnv is what the core gives a hook's response, on demand, so agents
// never build it themselves. Either may be nil.
type HookEnv struct {
	// Context renders `tm context` (coordinator) or the brief pointer
	// (thread).
	Context func() ([]byte, error)
	// Guard judges a tool call against the session's guard rules
	// (docs/SPEC.md §8.6, Guard) and records a refusal: the refusal, or
	// nil.
	Guard func(tool string, input map[string]any) *guard.Denial
}

// Injector says how follow-up prompts reach a running session.
type Injector string

const (
	InjectPaste   Injector = "paste"   // bracketed paste, then Enter, only while idle (core does this)
	InjectChannel Injector = "channel" // structured, via Agent.Prompt (Go); falls back to paste when it fails
	InjectNone    Injector = "none"
)

// PromptTarget is what a structured prompt channel needs to reach one
// live session: the ids and the extra values the status file exposes
// (StatusFile.Fields, e.g. a messaging socket path).
type PromptTarget struct {
	SessionID string
	AgentSID  string
	PID       int
	Version   string            // the agent's version, when the status file says
	Fields    map[string]string // from the status file; nil when it isn't trusted
	// Token authenticates to the channel (Claude's messaging token), as
	// the agent's hooks last reported it (inject.token_env); empty when
	// none did.
	Token string
}

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
	// output the harness expects back (context re-injection and guard
	// refusals go here), drawing on what env gives.
	Hook(ev HookEvent, env HookEnv) ([]Signal, HookResult, error)

	// Rules returns the screen rules that cross-check hook state. The
	// core's rule engine (package detect) evaluates them; agents never
	// read the screen directly.
	Rules() []Rule

	// Injector says how Prompt is delivered.
	Injector() Injector

	// Prompt delivers text through a structured channel. Only called when
	// Injector() == InjectChannel. Any error makes the core fall back to
	// the paste injector.
	Prompt(ctx context.Context, target PromptTarget, text string) error

	// Sources returns the declarative state sources the core runs for this
	// agent besides hooks and the screen: a status file, a JSONL tail and a
	// todo snapshot, plus hook payload trimming (docs/SPEC.md §8.2).
	Sources() *Sources
}

// Liveness is what a probe found out about the agent behind a target.
type Liveness string

const (
	LiveUnknown Liveness = ""
	Live        Liveness = "live"
	Gone        Liveness = "gone"
)

// Prober is an Agent with a cheap check that its process is still there
// (docs/SPEC.md §8.6), which the core runs beside the status file and
// the pid. Gone must be certain; anything doubtful is LiveUnknown, with
// the error that says why.
type Prober interface {
	Probe(ctx context.Context, target PromptTarget) (Liveness, error)
}

// Truster is an Agent that can mark a directory as trusted ahead of
// launch, so its folder-trust screen never shows there (docs/SPEC.md
// §8.6). The core calls it only for a thread's own worktree, which tm
// created; home is the user's home directory. An error is logged and the
// launch goes on: the trust screen then shows as blocked / trust.
type Truster interface {
	TrustDir(home, dir string) error
}

// Mover is an Agent that keeps state by folder path (conversations to
// resume, per-folder settings) and can carry it over when tm moves a
// folder it ran in: a thread's worktree or a project folder, on tm
// project rename (docs/SPEC.md §5.1). The folder has moved already; an
// error is reported, and the rename stands.
type Mover interface {
	MoveDir(home, from, to string) error
}

// Modder is an Agent that ships terminatr's mod: a plugin module the
// agent loads beside its command hooks (docs/SPEC.md §8.6, Mods). The
// agent's Launch writes it when LaunchSpec.Mods is set.
type Modder interface {
	// ModsMinVersion is the oldest agent version the mod was tested on;
	// the core leaves it out for an older one, or one it can't read.
	ModsMinVersion() string
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
	// SkipDim ignores dim/faint cells when matching, so ghost text such as
	// Claude's prompt suggestion isn't read as typed input.
	SkipDim bool `toml:"skip_dim"`
}
