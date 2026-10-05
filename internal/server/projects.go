package server

// Projects and threads in the server (docs/SPEC.md §7, M6): agent calls
// to project commands run here, with the caller told from the peer pid
// (§11.1), and thread sessions' todos and agent session ids are mirrored
// into threads/<id>/.

import (
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
// else (a shell outside termilator, a shell session) is the human. This
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
	prompted := st.State == agent.StateWorking || st.State == agent.StateBlocked
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
