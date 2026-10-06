package server

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/theclifmeister/terminatr/internal/agent"
	"github.com/theclifmeister/terminatr/internal/caller"
	"github.com/theclifmeister/terminatr/internal/config"
	"github.com/theclifmeister/terminatr/internal/proto"
	"github.com/theclifmeister/terminatr/internal/rename"
	"github.com/theclifmeister/terminatr/internal/session"
	"github.com/theclifmeister/terminatr/internal/tasks"
)

// renameProject is project.rename (docs/SPEC.md §5.1): refused while a
// thread of the project runs; its coordinator is stopped, the project
// renamed between two ticker sweeps (which carry their memos over), and
// the coordinator started again under the new slug.
func (s *Server) renameProject(p proto.ProjectRenameParams, c caller.Caller) (any, *proto.Error) {
	if c.IsAgent() {
		return nil, proto.Errorf("human-only", "the user renames projects")
	}
	o := rename.Options{From: p.From, To: p.To, Name: p.Name, Caller: c, MoveAgentDir: s.moveAgentDir}
	if err := rename.Check(o); err != nil {
		return nil, codeErr(err)
	}
	var busy []string
	var coord *session.Session
	var rec SessionRecord
	s.mu.Lock()
	for id, sess := range s.sessions {
		r := s.records[id]
		if r.Project != p.From || sess.ExitStatus() != "" {
			continue
		}
		switch r.Role {
		case proto.RoleThread:
			busy = append(busy, r.Thread)
		case proto.RoleCoordinator:
			coord, rec = sess, r
		}
	}
	s.mu.Unlock()
	if len(busy) > 0 {
		return nil, proto.Errorf("sessions-running", "%s still runs %s: stop them first (tm thread stop <id>; tm thread restart <id> brings each back after the rename)", p.From, strings.Join(busy, ", "))
	}
	if coord != nil {
		s.log.Printf("project.rename %s → %s: stopping coordinator %s", p.From, p.To, coord.ID())
		coord.Stop(StopGrace)
	}
	var res *rename.Result
	var after error // an error once the rename stood
	do := func() error {
		var err error
		res, err = rename.Project(o)
		if res == nil {
			return err
		}
		after = err
		return nil
	}
	var err error
	if s.tick != nil {
		err = s.tick.RenameProject(p.From, p.To, do)
	} else {
		err = do()
	}
	if err != nil {
		// Nothing moved: the coordinator comes back where it was.
		if coord != nil {
			s.restartCoordinator(rec, p.From, rec.Cwd)
		}
		return nil, codeErr(err)
	}
	s.views.renameProject(p.From, p.To)
	if s.tick != nil {
		s.tick.Kick()
	}
	s.watch.wake()
	out := proto.ProjectRenameResult{Dir: res.Dir, Worktrees: res.Worktrees, Threads: res.Threads, Notes: res.Notes}
	if after != nil {
		out.Notes = append(out.Notes, after.Error())
	}
	if coord != nil {
		id, err := s.restartCoordinator(rec, p.To, res.Dir)
		if err != nil {
			out.Notes = append(out.Notes, "coordinator not started again: "+err.Error()+" (tm project open "+p.To+")")
		}
		out.Coordinator = id
	}
	s.log.Printf("project.rename %s → %s: %d thread records, %d notes", p.From, p.To, out.Threads, len(out.Notes))
	return out, nil
}

// restartCoordinator starts the coordinator rec described again, for
// project slug in dir, as tm project open would: fresh, with the
// project's remote control setting.
func (s *Server) restartCoordinator(rec SessionRecord, slug, dir string) (string, error) {
	remote := config.Defaults.CoordinatorRemoteControl
	if cfg, err := config.Load(); err == nil {
		if safety, err := cfg.Safety(slug); err == nil {
			remote = safety.CoordinatorRemoteControl
		}
	}
	res, perr := s.startSession(proto.SessionStartParams{
		Agent: rec.Agent, Role: proto.RoleCoordinator, Project: slug, Cwd: dir, Model: rec.Model,
		Cols: rec.Cols, Rows: rec.Rows, Kickoff: rec.Kickoff, RemoteControl: remote,
	})
	if perr != nil {
		return "", perr
	}
	started, ok := res.(proto.SessionStartResult)
	if !ok {
		return "", fmt.Errorf("unexpected result %T", res)
	}
	return started.Session.ID, nil
}

// moveAgentDir carries every agent's state for a moved folder over.
func (s *Server) moveAgentDir(from, to string) error {
	reg := s.registry()
	home, err := os.UserHomeDir()
	if reg == nil || err != nil {
		return err
	}
	return MoveAgentDirs(reg, home, from, to)
}

// MoveAgentDirs asks each agent in reg that keeps state by folder
// (agent.Mover) to carry it from one folder over to another.
func MoveAgentDirs(reg *agent.Registry, home, from, to string) error {
	var errs []error
	for _, name := range reg.Names() {
		a, ok := reg.Get(name)
		if m, mover := a.(agent.Mover); ok && mover {
			if err := m.MoveDir(home, from, to); err != nil {
				errs = append(errs, fmt.Errorf("%s: %w", name, err))
			}
		}
	}
	return errors.Join(errs...)
}

// codeErr keeps a refusal's code over the wire.
func codeErr(err error) *proto.Error {
	var te *tasks.Error
	if errors.As(err, &te) {
		return proto.Errorf(te.Code, "%s", te.Msg)
	}
	return proto.Errorf(proto.ErrRefused, "%v", err)
}
