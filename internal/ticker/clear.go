package ticker

// Auto-clear (docs/SPEC.md §7.5, §11.2): with auto_clear on, the ticker
// clears the coordinator's conversation once its context reaches [ui]
// context_hint, but only while nothing waits on it. The project's state
// lives in files (tm context, CONTEXT.md with its Needs you), which the
// coordinator gets back after the clear, so nothing is lost.

import (
	"fmt"
	"strconv"
	"time"

	"github.com/theclifmeister/terminatr/internal/caller"
	"github.com/theclifmeister/terminatr/internal/config"
	"github.com/theclifmeister/terminatr/internal/project"
	"github.com/theclifmeister/terminatr/internal/proto"
	"github.com/theclifmeister/terminatr/internal/thread"
)

// Defaults of auto-clear.
const (
	// DefaultClearGrace is how long every condition must hold, sweep
	// after sweep, before the ticker clears: a coordinator that just
	// answered the user is left a moment for the reply.
	DefaultClearGrace = 2 * time.Minute
	// DefaultClearEvery is how often the ticker clears at most, per
	// coordinator process: a clear that didn't take isn't repeated at
	// every sweep.
	DefaultClearEvery = 30 * time.Minute
)

// clearMemo is what autoClear saw of a project's coordinator.
type clearMemo struct {
	// Seen is the coordinator's session id and pid while every condition
	// held, since Ready.
	Seen  string    `json:"seen,omitempty"`
	Ready time.Time `json:"ready,omitzero"`
	// Cleared is when the ticker last cleared process ClearedID.
	ClearedID string    `json:"cleared_id,omitempty"`
	Cleared   time.Time `json:"cleared,omitzero"`
}

// autoClear clears the project's coordinator when auto_clear is on and
// its context is at or past hint percent of its window, only while it is
// idle with no prompt queued and no question open, its inbox is empty,
// and no thread waits on a question (an open menu, a needs-you, a
// blocked agent); every condition must hold for ClearGrace, and one
// process is cleared at most once per ClearEvery. Never while paused.
// Each clear is journaled as ticker coordinator.clear.
func (t *Ticker) autoClear(p *project.Project, sessions []proto.SessionInfo, safety config.Safety, hint int, now time.Time) {
	pm := t.projectMemo(p.Slug)
	if !safety.AutoClear || safety.Paused || hint <= 0 {
		pm.Clear = nil
		return
	}
	cm := pm.Clear
	if cm == nil {
		cm = &clearMemo{}
		pm.Clear = cm
	}
	coord, why := clearBlocked(p, sessions, hint, false)
	if why != "" {
		cm.Seen, cm.Ready = "", time.Time{}
		return
	}
	// A relaunch under the same id is a new process: its grace starts
	// again.
	seen := coord.ID + "/" + strconv.Itoa(coord.PID)
	if cm.Seen != seen {
		cm.Seen, cm.Ready = seen, now
	}
	if now.Sub(cm.Ready) < t.o.ClearGrace {
		return
	}
	if cm.ClearedID == seen && now.Sub(cm.Cleared) < t.o.ClearEvery {
		return
	}
	// Delivery is at once for an idle coordinator with nothing queued;
	// still checks again anyway, so a clear never lands on new work.
	still := func() bool {
		_, why := clearBlocked(p, t.o.Host.Sessions(), hint, true)
		return why == ""
	}
	pct := coord.ContextPercent()
	if err := t.o.Host.Clear(coord.ID, still); err != nil {
		t.o.Log.Printf("ticker: %s: auto-clear %s: %v", p.Slug, coord.ID, err)
		// Not tried again before ClearEvery: an agent without a clear
		// stays without one.
		cm.ClearedID, cm.Cleared = seen, now
		return
	}
	cm.ClearedID, cm.Cleared = seen, now
	cm.Seen, cm.Ready = "", time.Time{}
	detail := fmt.Sprintf("%s at %d%% of its context window (hint %d%%), idle with nothing waiting", coord.ID, pct, hint)
	if err := p.Journal(caller.Caller{Kind: caller.Ticker}, "coordinator.clear", p.Slug, detail); err != nil {
		t.o.Log.Printf("ticker: %s: journal: %v", p.Slug, err)
	}
	t.o.Log.Printf("ticker: %s: auto-clear: %s", p.Slug, detail)
}

// clearBlocked finds the project's coordinator and says what keeps it
// from being cleared now; "" when nothing does. At delivery the clear
// itself may still count as queued, and a prompt queued after it lands
// in the fresh conversation: the queue isn't looked at then.
func clearBlocked(p *project.Project, sessions []proto.SessionInfo, hint int, delivery bool) (*proto.SessionInfo, string) {
	var coord *proto.SessionInfo
	for i, s := range sessions {
		if s.Role == proto.RoleCoordinator && s.Project == p.Slug {
			coord = &sessions[i]
			break
		}
	}
	switch {
	case coord == nil:
		return nil, "no coordinator"
	case coord.State != "idle":
		return coord, "not idle"
	case coord.Queued > 0 && !delivery:
		return coord, "prompt queued"
	case coord.Question != nil:
		return coord, "question open"
	case coord.ContextPercent() < hint:
		return coord, "context below the hint"
	}
	for _, s := range sessions {
		if s.Project == p.Slug && s.Role == proto.RoleThread && (s.Question != nil || s.State == "blocked") {
			return coord, "thread " + s.Thread + " waits"
		}
	}
	items, err := p.Inbox()
	if err != nil {
		return coord, "inbox: " + err.Error()
	}
	if len(items) > 0 {
		return coord, "inbox not empty"
	}
	recs, err := thread.List(p)
	if err != nil {
		return coord, "threads: " + err.Error()
	}
	for _, r := range recs {
		if r.State == thread.Resolved {
			continue
		}
		if st, err := thread.ReadStatus(p, r.ID); err == nil && st.NeedsYou != "" {
			return coord, "thread " + r.ID + " needs the user"
		}
	}
	return coord, ""
}
