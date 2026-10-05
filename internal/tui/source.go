package tui

import (
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/theclifmeister/termilator/internal/agent"
	"github.com/theclifmeister/termilator/internal/caller"
	"github.com/theclifmeister/termilator/internal/config"
	"github.com/theclifmeister/termilator/internal/project"
	"github.com/theclifmeister/termilator/internal/proto"
	"github.com/theclifmeister/termilator/internal/server"
	"github.com/theclifmeister/termilator/internal/tasks"
	"github.com/theclifmeister/termilator/internal/thread"
	"github.com/theclifmeister/termilator/internal/ticker"
	"github.com/theclifmeister/termilator/internal/worktree"
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
	Err      string // why the poll failed, shown in the header
}

// ProjectData is one project's rows.
type ProjectData struct {
	Slug   string
	Name   string
	Goal   string
	Repos  []string
	Counts map[string]int
	// Safety are the project's settings; nil when they can't be read.
	Safety *config.Safety
	// Unread counts every unhandled inbox item; Items are all of them,
	// for the inbox view.
	Unread int
	Items  []project.Item
	// Threads are the project's unresolved threads (M6).
	Threads []ThreadRow
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
// only reads, starts shells, opens projects and asks the coordinator to
// act on a task: what happens to threads and tasks is the coordinator's
// (docs/SPEC.md §4).
type Source interface {
	Load() Data
	Board(slug string) (*tasks.Board, error)
	// StartShell starts a shell of cols×rows in cwd.
	StartShell(cwd string, cols, rows int) (string, error)
	NewProject(name string) (string, error)
	// OpenProject returns the project's coordinator session, started
	// first if none runs.
	OpenProject(slug string, cols, rows int) (string, error)
	// SetSetting changes a setting, on the human's keypress in a settings
	// popup (docs/SPEC.md §11.2): key in table "" (the top level), "keys"
	// or "projects.<slug>". It is refused when tm runs inside an agent.
	SetSetting(table, key string, value any) error
	// SetRepo adds or removes one of a project's repositories.
	SetRepo(slug, path string, add bool) error
	// Agents lists the agents tm can run.
	Agents() []string
	// Ask asks the project's coordinator to act on task id: an inbox
	// item of kind (project.KindDelegate, KindAccept or KindSendBack,
	// with the user's note) that is the user's word (docs/SPEC.md §4).
	// It reports false when an item already asks something of the task.
	Ask(slug string, id int, kind, note string) (bool, error)
	// Review is how to check task t, in review, and whether its change
	// has shipped.
	Review(slug string, t *tasks.Task) Review
	// SetRemote turns remote control of the project's running
	// coordinator on or off (prefix+r), saying what it did.
	SetRemote(slug string, on bool) (string, error)
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
	// Agent is the agent of coordinators.
	Agent string
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
	var d Data
	var res proto.SessionListResult
	if err := s.call(proto.MethodSessionList, nil, &res); err != nil {
		d.Err = err.Error()
	} else {
		d.ServerOK, d.Sessions, d.Alerts = true, res.Sessions, res.Alerts
	}
	list, err := project.List()
	if err != nil && d.Err == "" {
		d.Err = err.Error()
	}
	for _, sum := range list {
		pd := ProjectData{Slug: sum.Slug, Name: sum.Name, Goal: sum.Goal, Repos: sum.Repos,
			Counts: sum.Counts, Safety: sum.Safety, Err: sum.Error,
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

func (s *ServerSource) start(p proto.SessionStartParams) (string, error) {
	var res proto.SessionStartResult
	if err := s.call(proto.MethodSessionStart, p, &res); err != nil {
		return "", err
	}
	return res.Session.ID, nil
}

func (s *ServerSource) StartShell(cwd string, cols, rows int) (string, error) {
	return s.start(proto.SessionStartParams{Cwd: cwd, Cols: uint16(cols), Rows: uint16(rows)})
}

func (s *ServerSource) NewProject(name string) (string, error) {
	if s.Caller.IsAgent() {
		return "", errors.New("human-only: only the human creates projects")
	}
	p, err := project.New(project.Options{Name: name})
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
	return OpenCoordinator(s.call, slug, config.DefaultAgent(s.Agent), cols, rows)
}

// errHumanOnly refuses a settings change from inside an agent.
var errHumanOnly = errors.New("human-only: settings are changed by the human")

func (s *ServerSource) SetSetting(table, key string, value any) error {
	if s.Caller.IsAgent() {
		return errHumanOnly
	}
	slug, isProject := strings.CutPrefix(table, "projects.")
	if !isProject {
		return config.Set(table, key, value)
	}
	if err := config.SetProject(slug, key, value); err != nil {
		return err
	}
	// The project's journal records the change, as every tm action.
	if p, err := project.Open(slug); err == nil {
		p.Journal(s.Caller, "settings."+key, slug, fmt.Sprint(value))
	}
	return nil
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

// OpenCoordinator returns the id of the project's coordinator session,
// starting one (agent, role coordinator, cwd the project folder) if none
// runs: `tm project open` and the dashboard's project rows.
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
	// The project's remote control setting; a broken config.toml shows
	// in the settings popup, it doesn't keep the coordinator from starting.
	safety := config.Defaults
	if cfg, err := config.Load(); err == nil {
		safety, _ = cfg.Safety(slug)
	}
	var started proto.SessionStartResult
	err = call(proto.MethodSessionStart, proto.SessionStartParams{
		Agent: agentName, Role: proto.RoleCoordinator, Project: slug, Cwd: p.Dir,
		Cols: uint16(cols), Rows: uint16(rows), Kickoff: coordinatorKickoff,
		RemoteControl: safety.CoordinatorRemoteControl}, &started)
	if err != nil {
		return "", err
	}
	return started.Session.ID, nil
}
