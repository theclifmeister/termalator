package server

// Projects and threads in the server (docs/SPEC.md §7, M6): agent calls
// to project commands run here, with the caller told from the peer pid
// (§11.1), and thread sessions' todos and agent session ids are mirrored
// into threads/<id>/.

import (
	"path/filepath"

	"github.com/theclifmeister/termilator/internal/agent"
	"github.com/theclifmeister/termilator/internal/caller"
	"github.com/theclifmeister/termilator/internal/project"
	"github.com/theclifmeister/termilator/internal/proto"
	"github.com/theclifmeister/termilator/internal/pty"
	"github.com/theclifmeister/termilator/internal/session"
	"github.com/theclifmeister/termilator/internal/thread"
)

// callerOf tells who is calling from the peer pid: a process that
// descends from a coordinator or thread session is that agent; anything
// else (a shell outside terminatr, a shell session) is the human. This
// is soft, as the spec says: an agent can start a process outside its
// tree. File access rules are the second layer (§5.2).
func (s *Server) callerOf(pid int) caller.Caller {
	s.mu.Lock()
	byPID := make(map[int]SessionRecord, len(s.sessions))
	for id, sess := range s.sessions {
		byPID[sess.PID()] = s.records[id]
	}
	s.mu.Unlock()
	for i := 0; i < 64 && pid > 1; i++ {
		if r, ok := byPID[pid]; ok {
			switch r.Role {
			case proto.RoleCoordinator:
				return caller.Caller{Kind: caller.Coordinator, Project: r.Project}
			case proto.RoleThread:
				return caller.Caller{Kind: caller.Thread, Project: r.Project, Thread: r.Thread}
			}
			return caller.Caller{Kind: caller.Human}
		}
		ppid, err := pty.ParentPID(pid)
		if err != nil || ppid == pid {
			break
		}
		pid = ppid
	}
	return caller.Caller{Kind: caller.Human}
}

func (s *Server) cliRun(p proto.CLIRunParams, peerPID int) (any, *proto.Error) {
	if s.opts.RunCLI == nil {
		return nil, proto.Errorf(proto.ErrRefused, "this server runs no project commands")
	}
	if len(p.Args) == 0 {
		return nil, proto.Errorf(proto.ErrBadParams, "no command")
	}
	return s.opts.RunCLI(p, s.callerOf(peerPID)), nil
}

// syncThread mirrors a thread session's todo list into STATUS.md and its
// latest agent session id into thread.toml, for the dashboard, tm
// context and resume.
func (s *Server) syncThread(r SessionRecord, st session.AgentState) {
	s.threadMu.Lock()
	defer s.threadMu.Unlock()
	p, err := project.Open(r.Project)
	if err != nil {
		return
	}
	rec, err := thread.Load(p, r.Thread)
	if err != nil || rec.Session != r.ID {
		return
	}
	prompted := worked(st)
	if (st.AgentSID != "" && st.AgentSID != rec.AgentSID) || (prompted && !rec.Prompted) {
		if _, err := thread.Update(p, r.Thread, func(x *thread.Record) error {
			if st.AgentSID != "" {
				x.AgentSID = st.AgentSID
			}
			x.Prompted = x.Prompted || prompted
			return nil
		}); err != nil {
			s.log.Printf("thread %s: %v", r.Thread, err)
		}
	}
	cur, err := thread.ReadStatus(p, r.Thread)
	if err != nil || thread.SameTodos(cur.Todos, st.Todos) {
		return
	}
	if _, err := thread.UpdateStatus(p, r.Thread, func(x *thread.Status) error {
		x.Todos = append([]agent.Todo(nil), st.Todos...)
		return nil
	}); err != nil {
		s.log.Printf("thread %s: STATUS.md: %v", r.Thread, err)
	}
}

// adopt makes a running agent session outside the projects a thread's
// (session.adopt, docs/SPEC.md §9 Adopt): its record and its session
// take the thread role, so the caller check, the context after a clear,
// the todo sync, the views and a resume treat it as the thread's.
func (s *Server) adopt(p proto.SessionAdoptParams) (any, *proto.Error) {
	if p.Project == "" || !thread.ValidID(p.Thread) {
		return nil, proto.Errorf(proto.ErrBadParams, "session.adopt needs a project and a thread id")
	}
	if p.Brief != "" && !filepath.IsAbs(p.Brief) {
		return nil, proto.Errorf(proto.ErrBadParams, "brief must be an absolute path: %q", p.Brief)
	}
	sess, perr := s.session(p.ID)
	if perr != nil {
		return nil, perr
	}
	if sess.ExitStatus() != "" {
		return nil, proto.Errorf(proto.ErrUnknownSession, "session %q has exited", p.ID)
	}
	st, hasAgent := sess.AgentState()
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.records[p.ID]
	if !ok {
		return nil, proto.Errorf(proto.ErrUnknownSession, "no session %q", p.ID)
	}
	if cfg := sess.Config(); cfg.Role != proto.RoleShell || cfg.Project != "" {
		return nil, proto.Errorf(proto.ErrRefused, "session %s is a %s session of project %s, not one outside the projects", p.ID, cfg.Role, cfg.Project)
	}
	if !hasAgent || st.State == agent.StateExited {
		return nil, proto.Errorf(proto.ErrRefused, "no agent runs in session %s", p.ID)
	}
	r.Role, r.Project, r.Thread, r.Brief = proto.RoleThread, p.Project, p.Thread, p.Brief
	if r.Agent == "" {
		// An agent found in a shell: from now on the record resumes it.
		r.Agent = st.Agent
	}
	if st.AgentSID != "" {
		r.AgentSessionID = st.AgentSID
	}
	r.Prompted = r.Prompted || worked(st)
	s.records[p.ID] = r
	sess.Adopt(session.Identity{Role: r.Role, Project: r.Project, Thread: r.Thread})
	if err := s.saveLocked(""); err != nil {
		s.log.Printf("sessions.json: %v", err)
	}
	if s.tick != nil {
		s.tick.Kick()
	}
	s.log.Printf("session %s: adopted as thread %s of %s", p.ID, p.Thread, p.Project)
	return proto.SessionStartResult{Session: sess.Info()}, nil
}
