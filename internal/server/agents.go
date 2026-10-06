package server

// The agent layer of the server (docs/SPEC.md §8, M3): the manifest
// registry, launching and resuming agent sessions with their generated
// files, hook events, prompts, waits and explain. Nothing here names an
// agent; everything agent-specific comes through internal/agent.

import (
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/theclifmeister/terminatr/internal/agent"
	"github.com/theclifmeister/terminatr/internal/caller"
	"github.com/theclifmeister/terminatr/internal/project"
	"github.com/theclifmeister/terminatr/internal/proto"
	"github.com/theclifmeister/terminatr/internal/session"
	"github.com/theclifmeister/terminatr/internal/skill"
	"github.com/theclifmeister/terminatr/internal/thread"
	"github.com/theclifmeister/terminatr/internal/ticker"
	"github.com/theclifmeister/terminatr/internal/version"
	"github.com/theclifmeister/terminatr/internal/worktree"
)

// AgentsDir is where user manifests live, under TERMINATR_HOME.
func (p Paths) AgentsDir() string { return filepath.Join(p.Home, "agents") }

// loadAgents (re)reads the registry. Broken user manifests are logged
// and skipped; the built-ins always load.
func (s *Server) loadAgents() {
	reg, err := agent.Load(s.opts.Paths.AgentsDir())
	if reg == nil {
		// A broken built-in is a bug in this binary; run without agents.
		s.log.Printf("agents: %v", err)
		reg, _ = agent.Load("")
		if reg == nil {
			return
		}
	}
	var errs []string
	if err != nil {
		s.log.Printf("agents: %v", err)
		errs = strings.Split(err.Error(), "\n")
	}
	s.mu.Lock()
	s.agents, s.agentErrs = reg, errs
	s.mu.Unlock()
}

// writeLaunchFiles writes an agent's generated files (hooks, settings,
// brief) under its runtime dir. A session must not start without them.
func writeLaunchFiles(rt string, files map[string][]byte) error {
	for name, data := range files {
		p := filepath.Join(rt, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			return err
		}
		if err := os.WriteFile(p, data, 0o600); err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) registry() *agent.Registry {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.agents
}

func (s *Server) agentList() proto.AgentListResult {
	reg := s.registry()
	res := proto.AgentListResult{Agents: []proto.AgentInfo{}}
	s.mu.Lock()
	res.Errors = append(res.Errors, s.agentErrs...)
	s.mu.Unlock()
	if reg == nil {
		return res
	}
	for _, n := range reg.Names() {
		a, _ := reg.Get(n)
		info := proto.AgentInfo{Name: n, Source: reg.Source[n], Injector: string(a.Injector())}
		if m := agent.ManifestOf(a); m != nil {
			info.Display, info.Command, info.Tested = m.Display, m.Launch.Command, m.TestedVersions
			info.Unenforced = !m.RendersAccess()
		}
		res.Agents = append(res.Agents, info)
	}
	return res
}

// newUUID returns a random version 4 UUID, the agent session id the
// server pre-assigns (docs/SPEC.md §8.5).
func newUUID() string {
	var b [16]byte
	rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// runtimeDir is a session's scratch dir for generated files. It sits in
// the short run dir, which is private (0700).
func (s *Server) runtimeDir(id string) string {
	return filepath.Join(s.opts.Paths.RunDir, "s", id)
}

// realPath resolves symlinks, because agents check permission rules
// against real paths (docs/SPEC.md §5.2).
func realPath(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return p
}

// ownWorktree reports whether dir is a worktree tm created: one level
// below a project's folder in ~/.terminatr/worktrees.
func (s *Server) ownWorktree(dir string) bool {
	root := realPath(filepath.Join(s.opts.Paths.Home, "worktrees"))
	rel, err := filepath.Rel(root, realPath(dir))
	if err != nil {
		return false
	}
	parts := strings.Split(rel, string(filepath.Separator))
	return len(parts) == 2 && parts[0] != ".." && parts[0] != "." && parts[1] != ".."
}

// accessFor is the role's file access policy (docs/SPEC.md §5.2). A
// session outside a project gets no grants and no restrictions.
func (s *Server) accessFor(role, slug, cwd string) (agent.Access, error) {
	if slug == "" || role == proto.RoleShell {
		return agent.Access{}, nil
	}
	dir, err := project.Dir(slug)
	if err != nil {
		return agent.Access{}, err
	}
	dir = realPath(dir)
	// The human's safety settings (docs/SPEC.md §11.2).
	cfg := filepath.Join(realPath(s.opts.Paths.Home), "config.toml")
	switch role {
	case proto.RoleCoordinator:
		wt := realPath(filepath.Join(s.opts.Paths.Home, "worktrees", slug))
		return agent.Access{Read: []string{dir, wt}, NoWriteFiles: []string{cfg}}, nil
	case proto.RoleThread:
		a := agent.Access{Read: []string{dir}, NoWrite: []string{dir}, NoWriteFiles: []string{cfg}}
		// A worktree's commits go to the main repo's git dir, outside
		// the cwd the sandbox allows.
		if gd, err := worktree.CommonDir(cwd); err == nil {
			a.Write = []string{realPath(gd)}
		}
		return a, nil
	}
	return agent.Access{}, fmt.Errorf("unknown role %q", role)
}

// contextFor renders the context a role gets back after a clear or a
// compaction (docs/SPEC.md §7.8): the role's rules plus `tm context` for
// a coordinator, the rules plus the brief for a thread, nothing for a
// session outside a project.
func contextFor(role, slug, threadID, brief, tickerState string) func() ([]byte, error) {
	return func() ([]byte, error) {
		switch role {
		case proto.RoleCoordinator:
			rules, _ := skill.Text("coordinator", version.Version)
			p, err := project.Open(slug)
			if err != nil {
				return []byte(rules), nil
			}
			secs, err := p.Context(ticker.Seen(tickerState, slug))
			if err != nil {
				return []byte(rules), nil
			}
			return []byte(rules + "\n\n" + project.RenderContext(secs)), nil
		case proto.RoleThread:
			rules, _ := skill.Text("thread", version.Version)
			if p, err := project.Open(slug); err == nil && threadID != "" {
				if ctx := thread.ResetContext(p, threadID); ctx != "" {
					return []byte(rules + "\n\n" + ctx), nil
				}
			}
			if brief != "" {
				rules += "\n\nYour brief: " + brief + "\nRead it again, then continue with your task's unchecked steps.\n"
			}
			return []byte(rules), nil
		}
		return nil, nil
	}
}

// contextOf is contextFor the session's current record, so that a
// session adopted as a thread gets a thread's context (§9, Adopt).
func (s *Server) contextOf(id string) func() ([]byte, error) {
	return func() ([]byte, error) {
		s.mu.Lock()
		r := s.records[id]
		s.mu.Unlock()
		return contextFor(r.Role, r.Project, r.Thread, r.Brief, ticker.StatePath(s.opts.Paths.Sessions))()
	}
}

// agentLaunch is everything needed to (re)start one agent session.
type agentLaunch struct {
	rec    SessionRecord
	resume bool
	kick   string
	cols   uint16
	rows   uint16
}

// launchAgent builds the argv, environment and files for an agent
// session and starts it; s.mu held.
func (s *Server) launchAgent(l agentLaunch) (*session.Session, *proto.Error) {
	r := l.rec
	reg := s.agents
	if reg == nil {
		return nil, proto.Errorf(proto.ErrRefused, "no agents loaded")
	}
	a, ok := reg.Get(r.Agent)
	if !ok {
		return nil, proto.Errorf(proto.ErrBadParams, "unknown agent %q (tm agent list)", r.Agent)
	}
	if l.resume && r.AgentSessionID == "" {
		return nil, proto.Errorf(proto.ErrRefused, "session %s: no agent session id to resume", r.ID)
	}
	access, err := s.accessFor(r.Role, r.Project, r.Cwd)
	if err != nil {
		return nil, proto.Errorf(proto.ErrBadParams, "%v", err)
	}
	rt := s.runtimeDir(r.ID)
	os.RemoveAll(rt)
	if err := os.MkdirAll(rt, 0o700); err != nil {
		return nil, proto.Errorf(proto.ErrInternal, "%v", err)
	}
	spec := agent.LaunchSpec{
		Role: agent.Role(r.Role), SessionID: r.ID, AgentSID: r.AgentSessionID,
		Cwd: r.Cwd, RuntimeDir: rt, BriefPath: r.Brief, Kickoff: l.kick, Resume: l.resume,
		Yolo: r.Yolo, Model: r.Model, TMBin: s.opts.Bin, Socket: s.opts.Paths.Socket, Access: access,
		RemoteControl: r.RemoteControl, RemoteName: remoteName(r),
		Mods: s.modsFor(a, r.ID),
	}
	launch, err := a.Launch(spec)
	if err != nil {
		return nil, proto.Errorf(proto.ErrRefused, "%v", err)
	}
	if err := writeLaunchFiles(rt, launch.Files); err != nil {
		os.RemoveAll(rt)
		return nil, proto.Errorf(proto.ErrInternal, "%v", err)
	}
	set := s.terminatrEnv(r)
	if spec.Mods && !bandSetting() {
		set[envBand] = "off"
	}
	for _, kv := range launch.Env {
		k, v, _ := strings.Cut(kv, "=")
		set[k] = v
	}
	base := agent.FilterEnv(s.baseEnv(), launch.Unset)
	env := sessionEnv(base, set)
	r.Argv = launch.Argv
	home, _ := os.UserHomeDir()
	// A thread never waits on a folder-trust screen for the worktree tm
	// made for it (docs/SPEC.md §8.6): an agent that can, trusts it first.
	if t, ok := a.(agent.Truster); ok && r.Role == proto.RoleThread && home != "" && s.ownWorktree(r.Cwd) {
		if err := t.TrustDir(home, r.Cwd); err != nil {
			s.log.Printf("session %s: trust %s: %v", r.ID, r.Cwd, err)
		}
	}
	sess, err := session.Start(session.Config{
		ID: r.ID, Role: r.Role, Project: r.Project, Thread: r.Thread, Argv: launch.Argv, Cwd: r.Cwd, Env: env,
		Cols: l.cols, Rows: l.rows, Created: r.Created,
		Xtversion: "terminatr " + version.Version,
		Scheme:    s.scheme,
		Logf:      s.log.Printf,
		OnExit:    s.sessionExited,
		// Set before Start: the first session.list must show it.
		RemoteControl: r.RemoteControl, RemoteHeld: r.RemoteHeld,
		Agent: &session.AgentConfig{
			Agent: a, AgentSID: r.AgentSessionID, Kickoff: launch.Kickoff, Home: home,
			Context:    s.contextOf(r.ID),
			OnChange:   s.agentChanged,
			PromptHold: envDuration(envPromptHold), OnPromptResolved: s.promptResolved,
		},
	})
	if err != nil {
		os.RemoveAll(rt)
		return nil, proto.Errorf(proto.ErrRefused, "%v", err)
	}
	s.sessions[r.ID] = sess
	s.records[r.ID] = r
	if err := s.saveLocked(""); err != nil {
		s.log.Printf("sessions.json: %v", err)
	}
	how := "started"
	if l.resume {
		how = "resumed " + r.AgentSessionID
	}
	s.log.Printf("session %s: %s agent %s pid %d %q in %s", r.ID, how, r.Agent, sess.PID(), launch.Argv, r.Cwd)
	return sess, nil
}

// terminatrEnv is the session environment of docs/SPEC.md §3.4.
func (s *Server) terminatrEnv(r SessionRecord) map[string]string {
	set := map[string]string{
		"TERM":                 "xterm-256color",
		"COLORTERM":            "truecolor",
		"TERM_PROGRAM":         "terminatr",
		"TERM_PROGRAM_VERSION": version.Version,
		"TERMINATR":            "1",
		"TERMINATR_SESSION":    r.ID,
		"TERMINATR_SOCKET":     s.opts.Paths.Socket,
		"TERMINATR_HOME":       s.opts.Paths.Home,
		"TERMINATR_BIN":        s.opts.Bin,
		"TERMINATR_ROLE":       r.Role,
	}
	if r.Project != "" {
		set["TERMINATR_PROJECT"] = r.Project
	}
	if r.Thread != "" {
		set["TERMINATR_THREAD"] = r.Thread
	}
	return set
}

func (s *Server) baseEnv() []string {
	if s.opts.Env != nil {
		return s.opts.Env
	}
	return os.Environ()
}

// promptResolved logs and journals a queued prompt the session resolved
// after it was held for PromptHold while the agent was idle (§8.6), and
// rings the bell for a dropped one.
func (s *Server) promptResolved(sess *session.Session, res session.PromptResolution) {
	held := res.Held.Round(time.Second)
	if res.Via == "stale" {
		// Nothing was lost: what it said was handled meanwhile.
		s.log.Printf("session %s: queued prompt (queued %s) went stale and was not delivered", sess.ID(), res.Queued.Format(time.DateTime))
		return
	}
	msg := fmt.Sprintf("session %s: queued prompt (queued %s) held %s while idle, %s: ", sess.ID(), res.Queued.Format(time.DateTime), held, res.Why)
	if res.Via == "sent" {
		msg += "sent through the agent's channel (delivery not confirmed)"
	} else {
		msg += "dropped"
		if res.Err != nil {
			msg += fmt.Sprintf(" (channel: %v)", res.Err)
		}
	}
	s.mu.Lock()
	r, ok := s.records[sess.ID()]
	s.mu.Unlock()
	if ok && r.Project != "" {
		if p, err := project.Open(r.Project); err == nil {
			if err := p.Journal(caller.Caller{Kind: caller.Ticker}, "prompt."+res.Via, sess.ID(), fmt.Sprintf("held %s: %s", held, res.Why)); err != nil {
				s.log.Printf("session %s: journal: %v", sess.ID(), err)
			}
		}
	}
	if res.Via == "sent" {
		s.log.Print(msg)
		return
	}
	s.alert(msg)
}

// agentChanged records the agent's latest session id (Claude rotates it
// on /clear) for resume, and logs state changes.
func (s *Server) agentChanged(sess *session.Session) {
	st, ok := sess.AgentState()
	if !ok {
		return
	}
	s.mu.Lock()
	r, ok := s.records[sess.ID()]
	s.mu.Unlock()
	if ok && r.Role == proto.RoleThread {
		s.syncThread(r, st)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.tick != nil && ok && r.Project != "" {
		s.tick.Kick()
	}
	s.watch.wake()
	s.log.Printf("session %s: %s %s/%s (%s)", sess.ID(), st.Agent, st.State, st.Reason, st.Sources)
	if blocked := st.State == agent.StateBlocked; blocked != s.blocked[sess.ID()] {
		if blocked {
			s.blocked[sess.ID()] = true
			s.alerts.Add(1)
		} else {
			delete(s.blocked, sess.ID())
		}
	}
	r, ok = s.records[sess.ID()]
	// An agent found in a shell is the shell's business, unless the
	// shell was adopted as a thread: then resume needs its ids.
	if !ok || (st.Observed && r.Role != proto.RoleThread) {
		return
	}
	prompted := r.Prompted || worked(st)
	// The agent's own word on remote control wins: a resume reconnects.
	remote := r.RemoteControl
	if _, restarting := s.relaunch[sess.ID()]; st.RemoteKnown && !restarting {
		remote = st.RemoteControl
	}
	if (st.AgentSID == "" || r.AgentSessionID == st.AgentSID) && prompted == r.Prompted && remote == r.RemoteControl {
		return
	}
	r.RemoteControl = remote
	if st.AgentSID != "" {
		r.AgentSessionID = st.AgentSID
	}
	r.Prompted = prompted
	s.records[sess.ID()] = r
	if err := s.saveLocked(""); err != nil {
		s.log.Printf("sessions.json: %v", err)
	}
}

// worked reports whether the agent's state shows it worked on a prompt;
// a kickoff it hasn't started on doesn't count.
func worked(st session.AgentState) bool {
	return (st.State == agent.StateWorking && st.Reason != agent.ReasonKickoff) || st.State == agent.StateBlocked
}

// resume relaunches the previous server's agent sessions with their
// latest agent session ids (docs/SPEC.md §3.6), and says what became of
// each.
func (s *Server) resume(recs []SessionRecord) (outs []restartOutcome) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range recs {
		r.CleanExit = false
		if _, err := os.Stat(r.Cwd); err != nil {
			s.log.Printf("session %s: not resumed: %v", r.ID, err)
			s.lost = append(s.lost, r.ID)
			outs = append(outs, restartOutcome{rec: r, how: "lost"})
			continue
		}
		cols, rows := r.Cols, r.Rows
		if cols == 0 || rows == 0 {
			cols, rows = 80, 24
		}
		l := agentLaunch{rec: r, resume: true, cols: cols, rows: rows}
		how := "resumed"
		if !r.Prompted {
			how = "fresh"
			// Nothing to resume: the agent saved no conversation yet.
			l.resume, l.kick, l.rec.AgentSessionID = false, r.Kickoff, newUUID()
		}
		if _, perr := s.launchAgent(l); perr != nil {
			s.log.Printf("session %s: not resumed: %v", r.ID, perr)
			s.lost = append(s.lost, r.ID)
			outs = append(outs, restartOutcome{rec: r, how: "lost"})
			continue
		}
		s.resumed = append(s.resumed, r.ID)
		outs = append(outs, restartOutcome{rec: r, how: how})
	}
	return outs
}

func (s *Server) hookEvent(p proto.HookEventParams) proto.HookEventResult {
	sess, perr := s.session(p.Session)
	if perr != nil {
		return proto.HookEventResult{}
	}
	if a := sess.Agent(); a == nil || a.Name() != p.Agent {
		return proto.HookEventResult{}
	}
	if p.Token != "" {
		sess.SetPromptToken(p.Token)
	}
	res, err := sess.Hook(p.Event, p.Payload)
	if err != nil && !errors.Is(err, session.ErrNoAgent) {
		s.log.Printf("session %s: hook %s: %v", p.Session, p.Event, err)
	}
	return proto.HookEventResult{Stdout: string(res.Stdout)}
}

func (s *Server) prompt(p proto.SessionPromptParams) (any, *proto.Error) {
	return s.promptWith(p, session.PromptOptions{})
}

func (s *Server) promptWith(p proto.SessionPromptParams, o session.PromptOptions) (any, *proto.Error) {
	sess, perr := s.session(p.ID)
	if perr != nil {
		return nil, perr
	}
	if strings.TrimSpace(p.Text) == "" {
		return nil, proto.Errorf(proto.ErrBadParams, "empty prompt")
	}
	via, err := sess.PromptWith(p.Text, o)
	if errors.Is(err, session.ErrNoAgent) {
		return nil, proto.Errorf(proto.ErrRefused, "session %s runs no agent; use tm session keys", p.ID)
	}
	if err != nil {
		return nil, sessionError(p.ID, err)
	}
	s.watch.wake()
	return proto.SessionPromptResult{Via: via}, nil
}

// maxWait bounds session.wait.
const maxWait = 10 * time.Minute

func (s *Server) wait(p proto.SessionWaitParams) (any, *proto.Error) {
	sess, perr := s.session(p.ID)
	if perr != nil {
		return nil, perr
	}
	timeout := time.Duration(p.TimeoutMS) * time.Millisecond
	if timeout <= 0 || timeout > maxWait {
		timeout = maxWait
	}
	deadline := time.After(timeout)
	first := true
	for {
		ch := sess.StateChanged()
		st, ok := sess.AgentState()
		if !ok {
			return nil, proto.Errorf(proto.ErrRefused, "session %s runs no agent", p.ID)
		}
		res := proto.SessionWaitResult{State: string(st.State), Reason: st.Reason}
		if len(p.States) == 0 && !first {
			return res, nil
		}
		for _, want := range p.States {
			if want == string(st.State) {
				return res, nil
			}
		}
		if st.State == agent.StateExited {
			return res, nil
		}
		first = false
		select {
		case <-ch:
		case <-deadline:
			res.TimedOut = true
			return res, nil
		}
	}
}

// ExplainResult is the result of agent.explain.
type ExplainResult struct {
	Session proto.SessionInfo `json:"session"`
	agent.Explanation
}

func (s *Server) explain(id string) (any, *proto.Error) {
	sess, perr := s.session(id)
	if perr != nil {
		return nil, perr
	}
	e, ok := sess.Explain()
	if !ok {
		return nil, proto.Errorf(proto.ErrRefused, "session %s runs no agent", id)
	}
	return ExplainResult{Session: s.info(sess), Explanation: e}, nil
}
