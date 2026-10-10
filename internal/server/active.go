package server

// Active and inactive projects (docs/SPEC.md §3.6, §5.1): only an active
// project's coordinator and threads run. An inactive project's are
// dormant: records kept in sessions.json, not running, resumed under
// their ids when the user activates the project.

import (
	"os"
	"slices"
	"strings"
	"sync"

	"github.com/theclifmeister/terminatr/internal/caller"
	"github.com/theclifmeister/terminatr/internal/config"
	"github.com/theclifmeister/terminatr/internal/project"
	"github.com/theclifmeister/terminatr/internal/proto"
	"github.com/theclifmeister/terminatr/internal/thread"
)

// safetyOf is a project's settings; ok is false when they can't be read
// (a broken config.toml), which counts as active and not archived.
func safetyOf(cfg *config.Config, slug string) (config.Safety, bool) {
	if cfg == nil {
		return config.Defaults, false
	}
	s, err := cfg.Safety(slug)
	return s, err == nil
}

// projectBoots reads the settings once and says, per project, whether
// its agents run: active and not archived. Sessions outside a project
// always do.
func projectBoots() func(slug string) bool {
	cfg, err := config.Load()
	if err != nil {
		cfg = nil
	}
	return func(slug string) bool {
		if slug == "" {
			return true
		}
		s, ok := safetyOf(cfg, slug)
		return !ok || s.Active && !s.Archived
	}
}

// projectActive says whether a project is active; true when the
// settings can't be read.
func projectActive(slug string) bool {
	if slug == "" {
		return true
	}
	cfg, err := config.Load()
	if err != nil {
		return true
	}
	s, ok := safetyOf(cfg, slug)
	return !ok || s.Active
}

func inactiveErr(slug string) *proto.Error {
	return proto.Errorf("project-inactive", "%s is inactive: activate it first (space on it in the sidebar, or tm project activate %s)", slug, slug)
}

// dormantRole says whether r is a session a project's activation
// governs: its coordinator or a thread.
func dormantRole(r SessionRecord) bool {
	return r.Project != "" && (r.Role == proto.RoleCoordinator || r.Role == proto.RoleThread)
}

// dormantGone says why a dormant record is no longer to be resumed: its
// project is gone (deleted, renamed outside the server), or its thread
// was stopped, resolved or restarted meanwhile; "" when it still is.
func dormantGone(r SessionRecord) string {
	p, err := project.Open(r.Project)
	if err != nil {
		return "its project is gone"
	}
	if r.Role == proto.RoleThread {
		t, err := thread.Load(p, r.Thread)
		if err != nil || t.State != thread.Running || t.Session != r.ID {
			return "the thread was stopped, resolved or restarted"
		}
	}
	return ""
}

// sleepLocked moves the record of a session stopped for its project's
// deactivation to dormant (when there is a conversation to resume), so
// the project's activation resumes it. s.mu held.
func (s *Server) sleepLocked(id string) {
	delete(s.sleeping, id)
	r := s.records[id]
	os.RemoveAll(s.runtimeDir(id))
	delete(s.records, id)
	if r.Agent != "" && r.AgentSessionID != "" {
		r.CleanExit = true
		s.dormant[id] = r
	}
	if err := s.saveLocked(""); err != nil {
		s.log.Printf("sessions.json: %v", err)
	}
}

// setActive is project.active: the human activates or deactivates a
// project. The setting is written first, so a server start in between
// does what it says; then the project's coordinator and threads are
// stopped into dormant, or its dormant ones resumed. Doing it again does
// what is left (a setting changed by hand, a resume that failed).
func (s *Server) setActive(p proto.ProjectActiveParams, c caller.Caller) (any, *proto.Error) {
	if c.IsAgent() {
		return nil, proto.Errorf("human-only", "the user activates and deactivates projects")
	}
	proj, err := project.Open(p.Project)
	if err != nil {
		return nil, codeErr(err)
	}
	changed, err := proj.SetFlag(c, "active", p.Active)
	if err != nil {
		return nil, codeErr(err)
	}
	res := proto.ProjectActiveResult{Changed: changed}
	if p.Active {
		res.Resumed, res.Lost = s.wake(proj)
	} else {
		res.Stopped = s.sleep(proj.Slug)
		s.views.collapse(proj.Slug)
	}
	s.log.Printf("project.active %s %v: changed %v, stopped %v, resumed %v, lost %v", proj.Slug, p.Active, changed, res.Stopped, res.Resumed, res.Lost)
	return res, nil
}

// sleep stops the project's running coordinator and threads, keeping
// their records dormant, and names them.
func (s *Server) sleep(slug string) (names []string) {
	s.mu.Lock()
	var stop []func()
	for id, sess := range s.sessions {
		r := s.records[id]
		if r.Project != slug || !dormantRole(r) || sess.ExitStatus() != "" {
			continue
		}
		s.sleeping[id] = true
		names = append(names, restartLabel(r))
		stop = append(stop, func() { sess.Stop(StopGrace) })
	}
	s.mu.Unlock()
	var wg sync.WaitGroup
	for _, f := range stop {
		wg.Go(f)
	}
	wg.Wait()
	return sortLabels(names)
}

// wake resumes the project's dormant sessions, unless it is archived.
// A thread resolved or stopped meanwhile, or restarted under another
// session, is dropped; so is a session that can't be resumed.
func (s *Server) wake(p *project.Project) (resumed, lost []string) {
	if cfg, err := config.Load(); err == nil {
		if safety, ok := safetyOf(cfg, p.Slug); ok && safety.Archived {
			return nil, nil
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, r := range s.dormant {
		if r.Project != p.Slug {
			continue
		}
		delete(s.dormant, id)
		if why := dormantGone(r); why != "" {
			s.log.Printf("session %s: dormant %s dropped: %s", id, restartLabel(r), why)
			continue
		}
		if _, live := s.sessions[id]; live {
			continue
		}
		o := s.resumeLocked(r)
		if o.how == "lost" {
			lost = append(lost, restartLabel(r))
			continue
		}
		resumed = append(resumed, restartLabel(r))
	}
	if err := s.saveLocked(""); err != nil {
		s.log.Printf("sessions.json: %v", err)
	}
	return sortLabels(resumed), sortLabels(lost)
}

// renameDormant files a renamed project's dormant records under its new
// slug, their cwd and brief under its new folders (project.rename).
func (s *Server) renameDormant(from, to, oldDir, newDir, oldWT, newWT string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, r := range s.dormant {
		if r.Project != from {
			continue
		}
		r.Project = to
		r.Cwd, r.Brief = movePath(r.Cwd, oldDir, newDir, oldWT, newWT), movePath(r.Brief, oldDir, newDir, oldWT, newWT)
		s.dormant[id] = r
	}
	if err := s.saveLocked(""); err != nil {
		s.log.Printf("sessions.json: %v", err)
	}
}

// movePath is path under newDir or newWT when it was under oldDir or
// oldWT; else path.
func movePath(path, oldDir, newDir, oldWT, newWT string) string {
	for _, m := range [][2]string{{oldDir, newDir}, {oldWT, newWT}} {
		if m[0] == "" || m[1] == "" {
			continue
		}
		if path == m[0] {
			return m[1]
		}
		if rel, ok := under(path, m[0]); ok {
			return m[1] + string(os.PathSeparator) + rel
		}
	}
	return path
}

// under is path relative to dir when path is inside dir.
func under(path, dir string) (string, bool) {
	rel, ok := strings.CutPrefix(path, dir+string(os.PathSeparator))
	return rel, ok && rel != ""
}

// sortLabels orders session names: the coordinator first, then the
// threads by id.
func sortLabels(l []string) []string {
	slices.SortFunc(l, func(a, b string) int {
		if (a == "coordinator") != (b == "coordinator") {
			if a == "coordinator" {
				return -1
			}
			return 1
		}
		return strings.Compare(a, b)
	})
	return l
}
