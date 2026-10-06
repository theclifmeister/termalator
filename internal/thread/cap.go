package thread

import (
	"time"

	"github.com/theclifmeister/terminatr/internal/proto"
)

// The parallel threads cap (docs/SPEC.md §9, §11.2): tm thread start
// refuses a new thread while Working counts the project's cap or more.

// DoneSince is when the thread called tm done, if it did so after its
// last prompt; zero otherwise (a new prompt gives it work again).
func (r *Record) DoneSince() time.Time {
	if !r.Done || r.DoneAt.Before(r.LastPrompt) {
		return time.Time{}
	}
	return r.DoneAt
}

// AgentState is the live agent state of a thread's session in sessions
// ("working", "blocked", "idle", "unknown", …), and whether it runs.
func AgentState(r *Record, sessions []proto.SessionInfo) (state string, live bool) {
	if r.Session == "" {
		return "", false
	}
	for _, s := range sessions {
		if s.ID == r.Session && s.Thread == r.ID {
			if s.State == "" {
				return "unknown", true
			}
			return s.State, true
		}
	}
	return "", false
}

// IsWorking reports whether a thread counts toward the cap: it is open
// (not stopped or resolved), its session runs, its agent isn't idle
// (working, blocked and unknown all count), and it hasn't called tm done
// since its last prompt.
func IsWorking(r *Record, sessions []proto.SessionInfo) bool {
	if r.State != Running || !r.DoneSince().IsZero() {
		return false
	}
	st, live := AgentState(r, sessions)
	return live && st != "idle" && st != "exited" && st != "stopped"
}

// Working counts the threads of recs that count toward the cap.
func Working(recs []*Record, sessions []proto.SessionInfo) int {
	n := 0
	for _, r := range recs {
		if IsWorking(r, sessions) {
			n++
		}
	}
	return n
}
