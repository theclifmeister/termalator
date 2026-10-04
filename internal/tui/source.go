package tui

import (
	"errors"
	"fmt"
	"os"
	"sync"

	"github.com/theclifmeister/termalator/internal/caller"
	"github.com/theclifmeister/termalator/internal/project"
	"github.com/theclifmeister/termalator/internal/proto"
	"github.com/theclifmeister/termalator/internal/server"
	"github.com/theclifmeister/termalator/internal/tasks"
)

// Data is one poll of everything the dashboard shows. The dashboard holds
// no state of its own (docs/SPEC.md §4): every refresh asks the server
// for sessions and reads the project files.
type Data struct {
	ServerOK bool
	Sessions []proto.SessionInfo
	Projects []ProjectData
	Err      string // why the poll failed, shown in the header
}

// ProjectData is one project's rows.
type ProjectData struct {
	Slug   string
	Name   string
	Counts map[string]int
	// NeedsYou are the tasks in review or blocked.
	NeedsYou []*tasks.Task
	// Inbox are the unhandled items marked needs_user.
	Inbox []InboxRow
	// Unread counts every unhandled inbox item.
	Unread int
	Err    string
}

// InboxRow is an inbox item; Task is set for a done confirmation (§6.4),
// which d completes.
type InboxRow struct {
	project.Item
	Task *tasks.Task
}

// Source is the dashboard's view of the world; tests use a fake.
type Source interface {
	Load() Data
	Board(slug string) (*tasks.Board, error)
	// StartShell and StartAgent start a session of cols×rows in cwd.
	StartShell(cwd string, cols, rows int) (string, error)
	StartAgent(cwd string, cols, rows int) (string, error)
	NewProject(name string) (string, error)
	// OpenProject returns the project's coordinator session, started
	// first if none runs.
	OpenProject(slug string, cols, rows int) (string, error)
	// MarkDone sets a task done: the human's acceptance (§6.4).
	MarkDone(slug string, id int) error
}

// ServerSource is the real Source: the control socket plus the project
// folders.
type ServerSource struct {
	Paths server.Paths
	// Agent is the agent of coordinators and of the c key.
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
		d.ServerOK, d.Sessions = true, res.Sessions
	}
	list, err := project.List()
	if err != nil && d.Err == "" {
		d.Err = err.Error()
	}
	for _, sum := range list {
		pd := ProjectData{Slug: sum.Slug, Name: sum.Name, Counts: sum.Counts, Err: sum.Error}
		if p, err := project.Open(sum.Slug); err == nil {
			b, err := p.Tasks().Load()
			if err == nil {
				for _, t := range b.Tasks {
					if tasks.GroupOf(t.Status) == tasks.NeedsYou {
						pd.NeedsYou = append(pd.NeedsYou, t)
					}
				}
			}
			items, _ := p.Inbox()
			pd.Unread = len(items)
			for _, it := range items {
				if !it.NeedsUser {
					continue
				}
				r := InboxRow{Item: it}
				if id, ok := tasks.ParseRef(it.Subject); ok && it.Kind == project.KindConfirmDone && b != nil {
					r.Task = b.Find(id)
				}
				pd.Inbox = append(pd.Inbox, r)
			}
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

func (s *ServerSource) StartAgent(cwd string, cols, rows int) (string, error) {
	fi, err := os.Stat(cwd)
	if err != nil || !fi.IsDir() {
		return "", fmt.Errorf("%s is not a directory", cwd)
	}
	return s.start(proto.SessionStartParams{Agent: s.Agent, Cwd: cwd, Cols: uint16(cols), Rows: uint16(rows)})
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
	return OpenCoordinator(s.call, slug, s.Agent, cols, rows)
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
	var started proto.SessionStartResult
	err = call(proto.MethodSessionStart, proto.SessionStartParams{
		Agent: agentName, Role: proto.RoleCoordinator, Project: slug, Cwd: p.Dir,
		Cols: uint16(cols), Rows: uint16(rows), Kickoff: coordinatorKickoff}, &started)
	if err != nil {
		return "", err
	}
	return started.Session.ID, nil
}

func (s *ServerSource) MarkDone(slug string, id int) error {
	p, err := project.Open(slug)
	if err != nil {
		return err
	}
	_, err = p.Tasks().SetStatus(s.Caller, id, tasks.Done, "")
	return err
}
