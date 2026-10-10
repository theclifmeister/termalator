package server

import (
	"context"
	"fmt"

	"github.com/theclifmeister/terminatr/internal/agent"
	"github.com/theclifmeister/terminatr/internal/caller"
	"github.com/theclifmeister/terminatr/internal/config"
	"github.com/theclifmeister/terminatr/internal/models"
	"github.com/theclifmeister/terminatr/internal/project"
	"github.com/theclifmeister/terminatr/internal/proto"
	"github.com/theclifmeister/terminatr/internal/session"
)

// The models each installed agent offers (docs/SPEC.md §8.2, Models):
// the server asks every installed agent when it starts, and an agent
// again once a session finds its version changed; tm doctor and the
// Settings popup's Models page ask too. Each ask runs in the background,
// one at a time per agent, and never holds a launch up.

// refreshModels asks each installed agent for its models.
func (s *Server) refreshModels() {
	if s.agents == nil {
		return
	}
	env := s.baseEnv()
	for _, name := range models.Installed(s.agents, env) {
		a, _ := s.agents.Get(name)
		s.refreshAgentModels(a, env)
	}
}

// refreshAgentModels asks agent a for its models in the background,
// unless an ask of it is running.
func (s *Server) refreshAgentModels(a agent.Agent, env []string) {
	m := agent.ManifestOf(a)
	if m == nil || m.ListModels == nil {
		return
	}
	s.modelsMu.Lock()
	if s.modelsBusy == nil {
		s.modelsBusy = map[string]bool{}
	}
	if s.modelsBusy[m.Name] {
		s.modelsMu.Unlock()
		return
	}
	s.modelsBusy[m.Name] = true
	s.modelsMu.Unlock()
	go func() {
		defer func() {
			s.modelsMu.Lock()
			delete(s.modelsBusy, m.Name)
			s.modelsMu.Unlock()
		}()
		ctx := s.modelsCtx
		if ctx == nil {
			ctx = context.Background()
		}
		c, err := models.Refresh(ctx, m, env)
		switch {
		case err != nil:
			s.log.Printf("models: %s: %v", m.Name, err)
		case c.Status == models.StatusLoggedOut:
			s.log.Printf("models: %s %s: logged out, so its models are unknown", m.Name, c.Version)
		default:
			s.log.Printf("models: %s %s: %d models (%s)", m.Name, c.Version, len(c.Models), c.Account)
		}
	}()
}

// modelsVersion asks agent a again when the version a session found
// isn't the one its models were asked of.
func (s *Server) modelsVersion(a agent.Agent, version string, env []string) {
	m := agent.ManifestOf(a)
	if m == nil || m.ListModels == nil || version == "" {
		return
	}
	if c, ok := models.Load(m.Name); ok && c.Version == version {
		return
	}
	s.refreshAgentModels(a, env)
}

// launchModel is the model a session launches with when none is chosen:
// a coordinator's coordinator_model while its agent offers it in the
// project's scope, else the user's default_model for the agent on the
// same terms; "" is the agent's own default.
func launchModel(a agent.Agent, project string, coordinator bool) string {
	m := agent.ManifestOf(a)
	if m == nil {
		return ""
	}
	cfg, _ := config.Load()
	var safety config.Safety
	if project != "" {
		safety, _ = cfg.Safety(project)
	}
	c := models.Get(m, cfg, true)
	if coordinator {
		return c.CoordinatorModel(safety)
	}
	return c.LaunchModel(safety.Models)
}

// modelRefused learns, from a session's transcript, that the user's
// account refused the model it was launched with: the model is marked
// refused for this account and agent version, and the session's
// coordinator gets an inbox item. A session that passed no model ran
// the agent's own default, which is the agent's to sort out.
func (s *Server) modelRefused(sess *session.Session, msg string) {
	s.mu.Lock()
	r, ok := s.records[sess.ID()]
	model := s.launched[sess.ID()]
	s.mu.Unlock()
	if !ok {
		return
	}
	msg = oneLine(msg, 200)
	if model == "" {
		s.log.Printf("session %s: %s refused its own default model: %s", r.ID, r.Agent, msg)
		return
	}
	s.mu.Lock()
	seen := s.refusedSeen[sess.ID()]
	if s.refusedSeen == nil {
		s.refusedSeen = map[string]bool{}
	}
	s.refusedSeen[sess.ID()] = true
	s.mu.Unlock()
	if seen {
		return // one per session: the agent repeats it each turn
	}
	if err := models.MarkRefused(r.Agent, model, msg); err != nil {
		s.log.Printf("session %s: models: %v", r.ID, err)
	}
	s.log.Printf("session %s: %s refused model %s for this account: %s", r.ID, r.Agent, model, msg)
	if r.Project == "" {
		return
	}
	p, err := project.Open(r.Project)
	if err != nil {
		return
	}
	who := caller.Caller{Kind: caller.Coordinator, Project: r.Project}
	ref := r.ID
	if r.Role == proto.RoleThread && r.Thread != "" {
		who = caller.Caller{Kind: caller.Thread, Project: r.Project, Thread: r.Thread}
		ref = r.Thread
	}
	if err := p.Journal(who, "model.refused", ref, fmt.Sprintf("%s %s: %s", r.Agent, model, msg)); err != nil {
		s.log.Printf("session %s: journal: %v", r.ID, err)
	}
	what := "its coordinator"
	if r.Role == proto.RoleThread {
		what = "the thread"
	}
	summary := fmt.Sprintf("%s: %s refused model %s for the user's account (%s). tm won't offer it again for this login; restart %s with another model or none (tm thread restart, or the Coordinator model setting)", ref, r.Agent, model, msg, what)
	if _, err := p.AddItem("model-refused", ref, summary, false); err != nil {
		s.log.Printf("session %s: inbox: %v", r.ID, err)
	}
}

// syncModel applies the project's coordinator_model to its running
// coordinator (session.model; docs/SPEC.md §8.2, Models): the model the
// setting wants on the coordinator's agent (models.Catalog's
// CoordinatorModel) is switched to live with the agent's switch command
// ([inject] switch_model), pasted like any prompt once the agent is idle
// with an empty prompt box, so the conversation stays; an agent that
// can't switch gets it on its next start, and the session says so until
// then. Threads keep the model they were launched with.
func (s *Server) syncModel(id string) (any, *proto.Error) {
	s.mu.Lock()
	sess, ok := s.sessions[id]
	r := s.records[id]
	running := s.launched[id]
	s.mu.Unlock()
	if !ok {
		return nil, proto.Errorf(proto.ErrUnknownSession, "no session %s", id)
	}
	if r.Role != proto.RoleCoordinator || r.Project == "" {
		return nil, proto.Errorf(proto.ErrRefused, "the model setting is for coordinators; a thread keeps the model it was launched with")
	}
	a := sess.Agent()
	if a == nil {
		a = s.agentOr(r.Agent)
	}
	m := agent.ManifestOf(a)
	if m == nil {
		return nil, proto.Errorf(proto.ErrRefused, "agent %s has no manifest", r.Agent)
	}
	cfg, err := config.Load()
	if err != nil {
		return nil, proto.Errorf(proto.ErrRefused, "%v", err)
	}
	safety, err := cfg.Safety(r.Project)
	if err != nil {
		return nil, proto.Errorf(proto.ErrRefused, "%v", err)
	}
	want := models.Get(m, cfg, true).CoordinatorModel(safety)
	set := func(model, next string, pending bool) {
		s.mu.Lock()
		rec := s.records[id]
		if model != running || rec.ModelNext != next || rec.ModelPending != pending {
			s.launched[id] = model
			rec.Model, rec.ModelNext, rec.ModelPending = model, next, pending
			s.records[id] = rec
			if err := s.saveLocked(""); err != nil {
				s.log.Printf("sessions.json: %v", err)
			}
		}
		s.mu.Unlock()
		sess.SetModel(session.ModelState{Model: model, Next: next, Pending: pending})
	}
	if want == running {
		if r.ModelPending {
			set(running, "", false)
		}
		return proto.SessionModelResult{Model: want, How: proto.ModelUnchanged}, nil
	}
	text := m.SwitchText(want)
	if text == "" {
		if !r.ModelPending || r.ModelNext != want {
			s.log.Printf("session %s: coordinator model %s on its next start (%s can't switch live)", id, cmpOr(want, "default"), r.Agent)
		}
		set(running, want, true)
		return proto.SessionModelResult{Model: want, How: proto.ModelNextStart}, nil
	}
	if _, err := sess.Prompt(text); err != nil {
		return nil, sessionError(id, err)
	}
	set(want, "", false)
	s.log.Printf("session %s: coordinator model %s -> %s (%q, queued until idle)", id, cmpOr(running, "default"), cmpOr(want, "default"), text)
	return proto.SessionModelResult{Model: want, How: proto.ModelSwitched}, nil
}

func cmpOr(s, alt string) string {
	if s == "" {
		return alt
	}
	return s
}
