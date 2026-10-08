package tui

import (
	"cmp"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"

	"github.com/theclifmeister/terminatr/internal/agent"
	"github.com/theclifmeister/terminatr/internal/caller"
	"github.com/theclifmeister/terminatr/internal/config"
	"github.com/theclifmeister/terminatr/internal/project"
	"github.com/theclifmeister/terminatr/internal/proto"
	"github.com/theclifmeister/terminatr/internal/server"
	"github.com/theclifmeister/terminatr/internal/tasks"
	"github.com/theclifmeister/terminatr/internal/thread"
	"github.com/theclifmeister/terminatr/internal/ticker"
	"github.com/theclifmeister/terminatr/internal/worktree"
)

// Data is one poll of everything the dashboard shows. The dashboard holds
// no state of its own (docs/SPEC.md §4): every refresh asks the server
// for sessions and reads the project files.
type Data struct {
	ServerOK bool
	Sessions []proto.SessionInfo
	// Alerts is the server's notification count; the bell rings when it
	// goes up (docs/SPEC.md §4).
	Alerts   uint64
	Projects []ProjectData
	// Defaults are the all-projects settings, which a project follows
	// for each one it doesn't set; nil when they can't be read.
	Defaults *config.Safety
	// Mods, ModsBand and ModsPane are the settings popup's Mods, Mods
	// band and Mods pane (docs/SPEC.md §8.6); the band is on unless set
	// off, the pane off unless set on.
	Mods, ModsBand, ModsPane bool
	// ContextHint is [ui] context_hint: the percent of its context window
	// from which a coordinator is told to consider /clear; 0 for never.
	ContextHint int
	Err         string // why the poll failed, shown in the header
}

// ProjectData is one project's rows.
type ProjectData struct {
	Slug   string
	Goal   string
	Repos  []string
	Counts map[string]int
	// Safety are the project's settings; nil when they can't be read.
	Safety *config.Safety
	// Own are the settings the project sets itself (config keys); it
	// follows all projects in the rest.
	Own []string
	// Unread counts every unhandled inbox item; Items are all of them,
	// for the inbox view.
	Unread int
	Items  []project.Item
	// Threads are the project's unresolved threads (M6).
	Threads []ThreadRow
	// Usage is what all the project's threads used, resolved ones
	// included; TaskUsage splits it by task number.
	Usage     thread.Usage
	TaskUsage map[int]thread.Usage
	// NeedsYou are the tasks in the board's Needs you group (review or
	// blocked), in board order, for NEEDS YOU.
	NeedsYou []*tasks.Task
	// Checkouts are notes on the repos whose local default branch is
	// behind origin, by repo path, as the ticker last saw them.
	Checkouts map[string]string
	Err       string
}

// ThreadRow is one thread: its record (thread.toml) and its STATUS.md,
// nil when it has none yet.
type ThreadRow struct {
	*thread.Record
	Status *thread.Status
	// Report is the latest report (its PR line and ## Next), nil
	// without one; TaskRec is the linked task, for its steps.
	Report  *thread.Report
	TaskRec *tasks.Task
}

// Source is the dashboard's view of the world; tests use a fake. It
// only reads, opens projects and asks the coordinator to
// act on a task: what happens to threads and tasks is the coordinator's
// (docs/SPEC.md §4).
type Source interface {
	Load() Data
	Board(slug string) (*tasks.Board, error)
	NewProject(slug string) (string, error)
	// OpenProject returns the project's coordinator session, started
	// first if none runs.
	OpenProject(slug string, cols, rows int) (string, error)
	// SetSetting changes a setting, on the human's keypress in a settings
	// popup (docs/SPEC.md §11.2): key in table "" (the top level), "keys",
	// "defaults" (all projects) or "projects.<slug>"; a nil value removes
	// a project's own value, so it follows all projects again. It is
	// refused when tm runs inside an agent.
	SetSetting(table, key string, value any) error
	// SetRepo adds or removes one of a project's repositories.
	SetRepo(slug, path string, add bool) error
	// Agents lists the agents tm can run.
	Agents() []string
	// Models lists the models the agents' manifests offer a thread (the
	// choices of the models setting, docs/SPEC.md §11.2), each name once.
	Models() []string
	// Ask asks the project's coordinator to act on task id: an inbox
	// item of kind (project.KindDelegate, KindAccept or KindSendBack,
	// with the user's note) that is the user's word (docs/SPEC.md §4).
	// It reports false when an item already asks something of the task.
	Ask(slug string, id int, kind, note string) (bool, error)
	// AskAdopt asks the project's coordinator to adopt session s, an
	// agent session outside the projects, as a thread (docs/SPEC.md §4,
	// Adopt). It reports false when an item already asks it.
	AskAdopt(slug string, s proto.SessionInfo) (bool, error)
	// Review is how to check task t, in review, and whether its change
	// has shipped.
	Review(slug string, t *tasks.Task) Review
	// SetRemote turns remote control of the project's running
	// coordinator on or off (prefix+r), saying what it did.
	SetRemote(slug string, on bool) (string, error)
	// Lifecycle pauses, resumes, archives or deletes a project (verb
	// "pause", "resume", "archive", "delete"), saying what it did
	// (lifecycle.go).
	Lifecycle(slug, verb string) (string, error)
	// Memory is the project's CONTEXT.md, MEMORY.md and memory notes'
	// titles, for the project popup's Memory tab.
	Memory(slug string) (project.Memory, error)
	// Library lists the files of every thread's library, newest first,
	// for the project popup's Library tab; LibraryRead reads up to max
	// bytes of one (truncated says it was longer); LibraryRemove
	// deletes one of thread id's files, or all of them when name is
	// "", journaled, saying how many went.
	Library(slug string) ([]thread.LibFile, error)
	LibraryRead(slug, id, name string, max int64) (data []byte, truncated bool, err error)
	LibraryRemove(slug, id, name string) (int, error)
}

// Review is what the user needs to review a task: how to check it (the
// thread report's ## Check) and where its pull request stands.
type Review struct {
	Check []string
	// PR is the pull request's number, 0 when none is known; Ship is
	// where it stands: ShipOpen, ShipClosed, ShipMerged, "" unknown.
	PR   int
	Ship string
}

// Where a task's change stands (Review.Ship).
const (
	ShipOpen   = "open"
	ShipClosed = "closed"
	ShipMerged = "merged"
)

// ServerSource is the real Source: the control socket plus the project
// folders.
type ServerSource struct {
	Paths server.Paths
	// Caller is who acts: the human, unless tm runs inside an agent.
	Caller caller.Caller

	mu  sync.Mutex
	ctl *server.Client
}

// call makes one control call, reconnecting once if the connection broke.
func (s *ServerSource) call(method string, params, result any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for try := 0; ; try++ {
		if s.ctl == nil {
			c, err := server.Connect(s.Paths, true)
			if err != nil {
				return err
			}
			s.ctl = c
		}
		err := s.ctl.Call(method, params, result)
		var perr *proto.Error
		if err == nil || errors.As(err, &perr) || try > 0 {
			return err
		}
		s.ctl.Close()
		s.ctl = nil
	}
}

// Close drops the control connection.
func (s *ServerSource) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ctl != nil {
		s.ctl.Close()
		s.ctl = nil
	}
}

func (s *ServerSource) Load() Data {
	d := Data{ModsBand: true, ContextHint: config.DefaultContextHint}
	var res proto.SessionListResult
	if err := s.call(proto.MethodSessionList, nil, &res); err != nil {
		d.Err = err.Error()
	} else {
		d.ServerOK, d.Sessions, d.Alerts = true, res.Sessions, res.Alerts
	}
	if cfg, err := config.Load(); err == nil {
		d.Mods, d.ModsBand, d.ModsPane, d.ContextHint = cfg.Mods, cfg.ModsBand, cfg.ModsPane, cfg.ContextHint
		if all, err := cfg.AllProjects(); err == nil {
			d.Defaults = &all
		}
	}
	list, err := project.List()
	if err != nil && d.Err == "" {
		d.Err = err.Error()
	}
	for _, sum := range list {
		if sum.Safety != nil && sum.Safety.Archived {
			continue // hidden: tm project unarchive brings it back
		}
		pd := ProjectData{Slug: sum.Slug, Goal: sum.Goal, Repos: sum.Repos,
			Counts: sum.Counts, Safety: sum.Safety, Own: sum.Own, Err: sum.Error,
			Checkouts: ticker.Checkouts(ticker.StatePath(s.Paths.Sessions), sum.Slug)}
		if p, err := project.Open(sum.Slug); err == nil {
			b, err := p.Tasks().Load()
			if err != nil {
				b = nil
			}
			if b != nil {
				for _, t := range b.Tasks {
					if tasks.GroupOf(t.Status) == tasks.NeedsYou {
						pd.NeedsYou = append(pd.NeedsYou, t)
					}
				}
			}
			recs, _ := thread.List(p)
			pd.TaskUsage, pd.Usage = thread.TaskUsage(recs)
			for _, r := range recs {
				if r.State == thread.Resolved {
					continue
				}
				tr := ThreadRow{Record: r}
				tr.Status, _ = thread.ReadStatus(p, r.ID)
				tr.Report, _ = thread.ReadReport(p, r.ID)
				if b != nil && r.TaskID() > 0 {
					tr.TaskRec = b.Find(r.TaskID())
				}
				pd.Threads = append(pd.Threads, tr)
			}
			items, _ := p.Inbox()
			pd.Unread, pd.Items = len(items), items
		}
		d.Projects = append(d.Projects, pd)
	}
	return d
}

func (s *ServerSource) Board(slug string) (*tasks.Board, error) {
	p, err := project.Open(slug)
	if err != nil {
		return nil, err
	}
	return p.Tasks().Load()
}

func (s *ServerSource) Memory(slug string) (project.Memory, error) {
	p, err := project.Open(slug)
	if err != nil {
		return project.Memory{}, err
	}
	return p.ReadMemory()
}

func (s *ServerSource) Library(slug string) ([]thread.LibFile, error) {
	p, err := project.Open(slug)
	if err != nil {
		return nil, err
	}
	return thread.Library(p)
}

func (s *ServerSource) LibraryRead(slug, id, name string, max int64) ([]byte, bool, error) {
	p, err := project.Open(slug)
	if err != nil {
		return nil, false, err
	}
	return thread.ReadLibraryFile(p, id, name, max)
}

func (s *ServerSource) LibraryRemove(slug, id, name string) (int, error) {
	if s.Caller.IsAgent() {
		return 0, errors.New("human-only: library files are deleted by the human")
	}
	p, err := project.Open(slug)
	if err != nil {
		return 0, err
	}
	return thread.RemoveLibrary(p, s.Caller, id, name)
}

func (s *ServerSource) NewProject(slug string) (string, error) {
	if s.Caller.IsAgent() {
		return "", errors.New("human-only: only the human creates projects")
	}
	p, err := project.New(project.Options{Slug: slug})
	if err != nil {
		return "", err
	}
	return p.Slug, nil
}

// coordinatorKickoff is the coordinator's first prompt. Its context (the
// role rules and tm context) arrives through the agent's session-start
// hook (docs/SPEC.md §7.8); this only asks it to speak first.
const coordinatorKickoff = "You are this project's coordinator. Greet the user: say in a few lines where the project stands, from your context, then ask what to do next."

func (s *ServerSource) OpenProject(slug string, cols, rows int) (string, error) {
	return OpenCoordinator(s.call, slug, "", cols, rows)
}

// errHumanOnly refuses a settings change from inside an agent.
var errHumanOnly = errors.New("human-only: settings are changed by the human")

func (s *ServerSource) SetSetting(table, key string, value any) error {
	if s.Caller.IsAgent() {
		return errHumanOnly
	}
	if table == config.DefaultsTable {
		var err error
		if value == nil {
			err = config.UnsetDefaults(key)
		} else {
			err = config.SetDefaults(key, value)
		}
		if err != nil {
			return err
		}
		journalAll(s.Caller, key, value)
		return nil
	}
	slug, isProject := strings.CutPrefix(table, "projects.")
	if !isProject {
		return config.Set(table, key, value)
	}
	detail := fmt.Sprint(value)
	if value == nil {
		if err := config.UnsetProject(slug, key); err != nil {
			return err
		}
		detail = "(follows all projects)"
	} else if err := config.SetProject(slug, key, value); err != nil {
		return err
	}
	// The project's journal records the change, as every tm action.
	if p, err := project.Open(slug); err == nil {
		p.Journal(s.Caller, "settings."+key, slug, detail)
	}
	return nil
}

// journalAll records a change of an all-projects setting in the journal
// of each project that follows it, i.e. doesn't set key itself.
func journalAll(c caller.Caller, key string, value any) {
	cfg, err := config.Load()
	if err != nil {
		return
	}
	if key == "auto_resolve" {
		key = "auto_close"
	}
	list, _ := project.List()
	for _, sum := range list {
		if slices.Contains(cfg.Own(sum.Slug), key) {
			continue
		}
		if p, err := project.Open(sum.Slug); err == nil {
			p.Journal(c, "settings."+key, sum.Slug, fmt.Sprint(value)+" (all projects)")
		}
	}
}

func (s *ServerSource) Lifecycle(slug, verb string) (string, error) {
	switch verb {
	case "pause", "resume":
		return PauseProject(s.Caller, slug, verb == "pause")
	case "archive":
		return ArchiveProject(s.call, s.Caller, slug, true)
	case "delete":
		return DeleteProject(s.call, s.Caller, slug)
	}
	return "", fmt.Errorf("unknown project action %q", verb)
}

func (s *ServerSource) SetRepo(slug, path string, add bool) error {
	if s.Caller.IsAgent() {
		return errHumanOnly
	}
	p, err := project.Open(slug)
	if err != nil {
		return err
	}
	changed, err := p.SetRepo(path, add)
	if err != nil || !changed {
		return err
	}
	verb := map[bool]string{true: "add", false: "remove"}[add]
	return p.Journal(s.Caller, "project.repo."+verb, slug, path)
}

func (s *ServerSource) Ask(slug string, id int, kind, note string) (bool, error) {
	if s.Caller.IsAgent() {
		return false, errors.New("human-only: only the human asks the coordinator about a task")
	}
	p, err := project.Open(slug)
	if err != nil {
		return false, err
	}
	t, err := p.Tasks().Get(id)
	if err != nil {
		return false, err
	}
	if why := notAskable(t, kind); why != "" {
		return false, errors.New(why)
	}
	switch kind {
	case project.KindAccept:
		return p.AskAccept(s.Caller, t.Ref())
	case project.KindSendBack:
		return p.AskSendBack(s.Caller, t.Ref(), note)
	}
	return p.AskDelegate(s.Caller, t.Ref())
}

func (s *ServerSource) AskAdopt(slug string, sess proto.SessionInfo) (bool, error) {
	if s.Caller.IsAgent() {
		return false, errors.New("human-only: only the human asks the coordinator to adopt a session")
	}
	p, err := project.Open(slug)
	if err != nil {
		return false, err
	}
	return p.AskAdopt(s.Caller, sess.ID, adoptWhere(sess, ""))
}

// adoptWhere says what runs in session s and where, for an adopt ask:
// "claude in /src/app on fix-login"; home, when set, is shortened to ~.
func adoptWhere(s proto.SessionInfo, home string) string {
	where := s.Cwd
	if home != "" && strings.HasPrefix(where, home) {
		where = "~" + where[len(home):]
	}
	out := s.Agent + " in " + where
	if pl, ok := worktree.Locate(s.Cwd); ok && pl.Branch != "" {
		out += " on " + pl.Branch
	}
	return out
}

func (s *ServerSource) Review(slug string, t *tasks.Task) Review {
	var rv Review
	p, err := project.Open(slug)
	if err != nil {
		return rv
	}
	var rec *thread.Record
	if thread.ValidID(t.Thread) {
		rec, _ = thread.Load(p, t.Thread)
	}
	reportPR, repo := "", ""
	if rec != nil {
		repo = rec.Repo
		if rep, _ := thread.ReadReport(p, rec.ID); rep != nil {
			rv.Check, reportPR = rep.Check, rep.PR
		}
	}
	if repo == "" && len(p.Meta.Repos) > 0 {
		repo = p.Meta.Repos[0]
	}
	pr := ticker.PRs(ticker.StatePath(s.Paths.Sessions), slug)[t.Thread]
	rv.PR = pr.Number
	if rv.PR == 0 {
		rv.PR = ticker.PRNumber(reportPR)
	}
	rv.Ship = shipped(repo, rv.PR, pr)
	return rv
}

// shipped is where pull request n stands (Review.Ship), from repo's
// history as last fetched, else the ticker's last look at it (pr).
func shipped(repo string, n int, pr ticker.PR) string {
	switch {
	case n == 0:
		return ""
	case pr.State == "MERGED" || pr.Merge != "" || repo != "" && worktree.MergeCommit(repo, n) != "":
		return ShipMerged
	case pr.State == "OPEN":
		return ShipOpen
	case pr.State == "CLOSED":
		return ShipClosed
	}
	return ""
}

func (s *ServerSource) SetRemote(slug string, on bool) (string, error) {
	res, err := SetRemote(s.call, slug, on)
	if err != nil {
		return "", err
	}
	return RemoteMessage(slug, res), nil
}

func (s *ServerSource) Agents() []string {
	reg, _ := agent.Load(s.Paths.AgentsDir())
	if reg == nil {
		return nil
	}
	return reg.Names()
}

func (s *ServerSource) Models() []string {
	reg, _ := agent.Load(s.Paths.AgentsDir())
	if reg == nil {
		return nil
	}
	var out []string
	for _, n := range reg.Names() {
		a, _ := reg.Get(n)
		for _, m := range agent.ModelsOf(a) {
			if !slices.Contains(out, m.Name) {
				out = append(out, m.Name)
			}
		}
	}
	return out
}

// OpenCoordinator returns the id of the project's coordinator session,
// starting one (agent, role coordinator, cwd the project folder) if none
// runs: `tm project open` and the dashboard's project rows. An empty
// agentName is the project's coordinator_agent setting (§11.2).
func OpenCoordinator(call func(method string, params, result any) error, slug, agentName string, cols, rows int) (string, error) {
	p, err := project.Open(slug)
	if err != nil {
		return "", err
	}
	var res proto.SessionListResult
	if err := call(proto.MethodSessionList, nil, &res); err != nil {
		return "", err
	}
	for _, s := range res.Sessions {
		if s.Role == proto.RoleCoordinator && s.Project == slug {
			return s.ID, nil
		}
	}
	// The project's agent and remote control settings; a broken config.toml shows
	// in the settings popup, it doesn't keep the coordinator from starting.
	safety := config.Defaults
	if cfg, err := config.Load(); err == nil {
		safety, _ = cfg.Safety(slug)
	}
	var started proto.SessionStartResult
	err = call(proto.MethodSessionStart, proto.SessionStartParams{
		Agent: cmp.Or(agentName, safety.CoordinatorAgent), Role: proto.RoleCoordinator, Project: slug, Cwd: p.Dir,
		Cols: uint16(cols), Rows: uint16(rows), Kickoff: coordinatorKickoff,
		RemoteControl: safety.CoordinatorRemoteControl}, &started)
	if err != nil {
		return "", err
	}
	return started.Session.ID, nil
}
