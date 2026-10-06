package ticker

// Keeping a coordinator's remote control on (docs/SPEC.md §7.5, §11.2):
// while coordinator_remote_control is on, the ticker turns the running
// coordinator's remote control back on whenever it reads off, after a
// start, a resume, a server restart or a drop, the same way prefix+r
// does. A manual off holds until the coordinator is started anew.

import (
	"strconv"
	"time"

	"github.com/theclifmeister/terminatr/internal/caller"
	"github.com/theclifmeister/terminatr/internal/config"
	"github.com/theclifmeister/terminatr/internal/project"
	"github.com/theclifmeister/terminatr/internal/proto"
)

// Defaults of the remote control enforcement.
const (
	// DefaultRemoteEvery is how often the ticker tries at most, per
	// coordinator.
	DefaultRemoteEvery = 10 * time.Minute
	// DefaultRemoteGrace is how long a coordinator must read off before
	// the ticker acts: an agent that starts or resumes with remote
	// control on reads off until it has connected.
	DefaultRemoteGrace = time.Minute
)

// remoteMemo is what the enforcement saw of a project's coordinator.
type remoteMemo struct {
	// Seen is the coordinator's session id and pid while it read off,
	// since Off.
	Seen string    `json:"seen,omitempty"`
	Off  time.Time `json:"off,omitzero"`
	// Tried is when the ticker last turned it on, for session TriedID.
	TriedID string    `json:"tried_id,omitempty"`
	Tried   time.Time `json:"tried,omitzero"`
}

// keepRemote turns the project's coordinator's remote control on when
// the setting says so and it reads off: only while the coordinator is
// idle with no prompt queued, after it read off for RemoteGrace, at most
// once per RemoteEvery, and never while the user's own off holds
// (SessionInfo.RemoteHeld). Each try is journaled as ticker remote.on.
func (t *Ticker) keepRemote(p *project.Project, sessions []proto.SessionInfo, safety config.Safety, now time.Time) {
	pm := t.projectMemo(p.Slug)
	if !safety.CoordinatorRemoteControl {
		pm.Remote = nil
		return
	}
	var coord *proto.SessionInfo
	for i, s := range sessions {
		if s.Role == proto.RoleCoordinator && s.Project == p.Slug {
			coord = &sessions[i]
			break
		}
	}
	rm := pm.Remote
	if rm == nil {
		rm = &remoteMemo{}
		pm.Remote = rm
	}
	if coord == nil || coord.RemoteControl || coord.RemoteHeld {
		rm.Seen, rm.Off = "", time.Time{}
		return
	}
	// A relaunch under the same id is a new process: its grace starts
	// again.
	seen := coord.ID + "/" + strconv.Itoa(coord.PID)
	if rm.Seen != seen {
		rm.Seen, rm.Off = seen, now
	}
	if now.Sub(rm.Off) < t.o.RemoteGrace || coord.State != "idle" || coord.Queued > 0 {
		return
	}
	if rm.TriedID == coord.ID && now.Sub(rm.Tried) < t.o.RemoteEvery {
		return
	}
	rm.TriedID, rm.Tried = coord.ID, now
	res, err := t.o.Host.Remote(coord.ID, true)
	if err != nil {
		t.o.Log.Printf("ticker: %s: remote control on for %s: %v", p.Slug, coord.ID, err)
		return
	}
	if err := p.Journal(caller.Caller{Kind: caller.Ticker}, "remote.on", p.Slug, coord.ID+" "+res.How); err != nil {
		t.o.Log.Printf("ticker: %s: journal: %v", p.Slug, err)
	}
	t.o.Log.Printf("ticker: %s: remote control on for %s (%s)", p.Slug, coord.ID, res.How)
}
