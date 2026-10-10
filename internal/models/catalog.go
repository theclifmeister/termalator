package models

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/theclifmeister/terminatr/internal/agent"
	"github.com/theclifmeister/terminatr/internal/config"
)

// Model is one model of an agent's catalog.
type Model struct {
	agent.ListedModel
	// Yours: the user added it (config.toml add); the agent doesn't list it.
	Yours bool
	// Refusal, when set, is why the user's account refused it.
	Refusal *Refusal
}

// Catalog is what an agent offers the user: its answer, with the user's
// settings and the learnt refusals laid over it (docs/SPEC.md §8.2).
type Catalog struct {
	Agent     string
	Installed bool
	// Known: the agent answered for a logged-in user. Otherwise Reason
	// says why the models are unknown, and no model may be chosen.
	Known  bool
	Reason string
	Cache  Cache // the answer, when HasCache
	// HasCache: the agent was asked at least once.
	HasCache bool
	// Models are the available models: listed and not hidden or refused,
	// then the user's added ones. Hidden and Refused are the others.
	Models   []Model
	Hidden   []Model
	Refused  []Model
	Settings config.AgentSettings
}

// Get is agent m's catalog: installed or not (an installed check the
// caller made), its cache, and cfg's settings for it.
func Get(m *agent.Manifest, cfg *config.Config, installed bool) Catalog {
	c := Catalog{Agent: m.Name, Installed: installed, Settings: cfg.Agent(m.Name)}
	c.Cache, c.HasCache = Load(m.Name)
	switch {
	case !installed:
		c.Reason = fmt.Sprintf("%s not installed", m.Name)
	case m.ListModels == nil:
		c.Reason = fmt.Sprintf("%s can't list its models (no list_models in its manifest)", m.Name)
	case !c.HasCache:
		c.Reason = "not asked yet (tm doctor asks)"
	case c.Cache.Status == StatusLoggedOut:
		c.Reason = fmt.Sprintf("logged out of %s", m.Name)
	case c.Cache.Status != StatusOK:
		c.Reason = "probe failed: " + c.Cache.Reason
	default:
		c.Known = true
	}
	return c.With(c.Settings)
}

// With is the catalog with the user's settings s in place of its own,
// as the Settings popup shows a change before it is saved.
func (c Catalog) With(s config.AgentSettings) Catalog {
	c.Settings, c.Models, c.Hidden, c.Refused = s, nil, nil, nil
	if !c.Known {
		return c
	}
	refusal := func(name string) *Refusal {
		if r, ok := c.Cache.Refused[name]; ok {
			return &r
		}
		return nil
	}
	for _, x := range c.Cache.Models {
		mm := Model{ListedModel: x, Refusal: refusal(x.Name)}
		switch {
		case mm.Refusal != nil:
			c.Refused = append(c.Refused, mm)
		case slices.Contains(c.Settings.Hide, x.Name):
			c.Hidden = append(c.Hidden, mm)
		default:
			c.Models = append(c.Models, mm)
		}
	}
	for _, name := range c.Settings.Added() {
		if c.listed(name) {
			continue
		}
		mm := Model{ListedModel: agent.ListedModel{Name: name}, Yours: true, Refusal: refusal(name)}
		if mm.Refusal != nil {
			c.Refused = append(c.Refused, mm)
		} else {
			c.Models = append(c.Models, mm)
		}
	}
	return c
}

func (c Catalog) listed(name string) bool {
	return slices.ContainsFunc(c.Cache.Models, func(x agent.ListedModel) bool { return x.Name == name })
}

// Has reports whether name is available.
func (c Catalog) Has(name string) bool {
	return slices.ContainsFunc(c.Models, func(x Model) bool { return x.Name == name })
}

// Find returns the model named name: available, hidden or refused.
func (c Catalog) Find(name string) (Model, bool) {
	for _, l := range [][]Model{c.Models, c.Hidden, c.Refused} {
		for _, x := range l {
			if x.Name == name {
				return x, true
			}
		}
	}
	return Model{}, false
}

// Names lists the available models' names.
func (c Catalog) Names() []string {
	out := make([]string, len(c.Models))
	for i, x := range c.Models {
		out[i] = x.Name
	}
	return out
}

// InScope reports whether a project's models scope takes name; an
// empty (unset) scope takes every available model.
func InScope(scope []string, name string) bool {
	return len(scope) == 0 || slices.Contains(scope, name)
}

// InScope lists the available models a project's scope takes.
func (c Catalog) InScope(scope []string) []Model {
	var out []Model
	for _, x := range c.Models {
		if InScope(scope, x.Name) {
			out = append(out, x)
		}
	}
	return out
}

// LaunchModel is the model a launch passes when none is chosen: the
// user's default_model while it is available and in the project's
// scope; "" otherwise, so the agent runs its own default.
func (c Catalog) LaunchModel(scope []string) string {
	if d := c.Settings.DefaultModel; d != "" && c.Has(d) && InScope(scope, d) {
		return d
	}
	return ""
}

// CoordinatorModel is the model a project's coordinator runs on this
// agent: its coordinator_model while the agent offers it in the
// project's scope, else LaunchModel's ("" the agent's own default).
func (c Catalog) CoordinatorModel(s config.Safety) string {
	if m := s.CoordinatorModel; m != "" && c.Check(m, s.Models) == nil {
		return m
	}
	return c.LaunchModel(s.Models)
}

// Check refuses a model the project may not start a thread with: none
// may be chosen while the agent's models are unknown; one the agent
// doesn't offer, one the account refused, and one outside the project's
// scope are each refused with their code. "" (the agent's default)
// always passes.
func (c Catalog) Check(model string, scope []string) error {
	if model == "" {
		return nil
	}
	if !c.Known {
		return &Error{"models-unknown", fmt.Sprintf("%s's models are unknown (%s); start without --model for %s's own default", c.Agent, c.Reason, c.Agent)}
	}
	if x, ok := c.Find(model); ok && x.Refusal != nil {
		return &Error{"model-refused", fmt.Sprintf("%s refused %s for the user's account on %s: %s (start with another model, or without --model)", c.Agent, model, x.Refusal.At.Local().Format("2006-01-02"), x.Refusal.Reason)}
	}
	if !c.Has(model) {
		return &Error{"unknown-model", fmt.Sprintf("%q isn't one of %s's models: %s (tm context says when each fits)", model, c.Agent, cmpOr(strings.Join(c.Names(), ", "), "none"))}
	}
	if !InScope(scope, model) {
		var in []string
		for _, x := range c.InScope(scope) {
			in = append(in, x.Name)
		}
		return &Error{"model-not-allowed", fmt.Sprintf("the user's settings don't allow model %q for this project; allowed for %s: %s (or start without --model for the agent's default)", model, c.Agent, cmpOr(strings.Join(in, ", "), "none"))}
	}
	return nil
}

// Source says where the models come from, for doctor and Settings:
// "asked 2.1.296 today (Claude Max, firstParty)", or why they're
// unknown.
func (c Catalog) Source(now time.Time) string {
	if !c.Known {
		return "unknown: " + c.Reason
	}
	s := "asked"
	if c.Cache.Version != "" {
		s += " " + c.Cache.Version
	}
	s += " " + Day(c.Cache.Asked, now)
	if c.Cache.Account != "" {
		s += " (" + c.Cache.Account + ")"
	}
	if c.Cache.Note != "" {
		s += "; " + c.Cache.Note
	}
	return s
}

// Day is t as "today", "yesterday" or a date.
func Day(t, now time.Time) string {
	y1, m1, d1 := t.Local().Date()
	y2, m2, d2 := now.Local().Date()
	switch {
	case y1 == y2 && m1 == m2 && d1 == d2:
		return "today"
	case t.Local().AddDate(0, 0, 1).Format("2006-01-02") == now.Local().Format("2006-01-02"):
		return "yesterday"
	}
	return t.Local().Format("2006-01-02")
}

func cmpOr(s, alt string) string {
	if s == "" {
		return alt
	}
	return s
}
