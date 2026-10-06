// Package caller says who is calling tm: the human, a project's
// coordinator, or one of its threads (docs/SPEC.md §11.1).
//
// The server decides this from the peer pid of the socket connection.
// Until the server serves project calls, FromEnv reads the session
// environment the server gives every hosted process (§3.4). That check is
// soft, as the spec says plainly: an agent can unset the variables. The
// agent's own permission rules and sandbox are the second layer (§5.2).
package caller

import "os"

// Kind is the class of caller.
type Kind string

const (
	Human       Kind = "human"
	Coordinator Kind = "coordinator"
	Thread      Kind = "thread"
	// Ticker is the server's own ticker acting on the human's settings,
	// e.g. resolving a thread after its PR merged (docs/SPEC.md §9). It
	// has a human's rights and is journaled as "ticker".
	Ticker Kind = "ticker"
)

// Caller is one tm invocation's identity.
type Caller struct {
	Kind    Kind
	Project string // the agent's project slug; empty for the human
	Thread  string // the thread id, e.g. "t-0003", for Kind == Thread
}

// IsAgent reports whether the caller is a coordinator or thread agent.
func (c Caller) IsAgent() bool { return c.Kind == Coordinator || c.Kind == Thread }

// String is the name used in the journal: "human", "coordinator" or the
// thread id.
func (c Caller) String() string {
	if c.Kind == Thread && c.Thread != "" {
		return c.Thread
	}
	return string(c.Kind)
}

// Environment variables set by the server on hosted sessions (§3.4).
// TERMINATR_ROLE is the session's role: coordinator, thread or shell.
const (
	EnvSession = "TERMINATR_SESSION"
	EnvRole    = "TERMINATR_ROLE"
	EnvProject = "TERMINATR_PROJECT"
	EnvThread  = "TERMINATR_THREAD"
)

// FromEnv derives the caller from the environment. Outside a hosted
// session, or in a shell session, the caller is the human.
func FromEnv() Caller { return FromLookup(os.Getenv) }

// FromLookup is FromEnv with an injectable getenv, for tests.
func FromLookup(getenv func(string) string) Caller {
	if getenv(EnvSession) == "" {
		return Caller{Kind: Human}
	}
	switch getenv(EnvRole) {
	case string(Coordinator):
		return Caller{Kind: Coordinator, Project: getenv(EnvProject)}
	case string(Thread):
		return Caller{Kind: Thread, Project: getenv(EnvProject), Thread: getenv(EnvThread)}
	case "":
		// No role but a thread id: err on the side of the narrower rights.
		if getenv(EnvThread) != "" {
			return Caller{Kind: Thread, Project: getenv(EnvProject), Thread: getenv(EnvThread)}
		}
	}
	return Caller{Kind: Human}
}

// rank orders kinds by how much they may do: the narrower, the higher.
func rank(k Kind) int {
	switch k {
	case Thread:
		return 2
	case Coordinator:
		return 1
	}
	return 0
}

// Narrower returns whichever of a and b has fewer rights; b wins a tie.
// The environment's view (a, §3.4) and the server's (b, from the peer
// pid, §11.1) are combined this way, so neither unsetting the variables
// nor a process that left its session's tree widens an agent's rights.
func Narrower(a, b Caller) Caller {
	if rank(b.Kind) >= rank(a.Kind) {
		return b
	}
	return a
}
