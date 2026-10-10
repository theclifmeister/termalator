package doctor

import (
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/theclifmeister/terminatr/internal/agent"
	"github.com/theclifmeister/terminatr/internal/config"
	"github.com/theclifmeister/terminatr/internal/models"
)

// Models checks which agents are installed and the models each one
// offers (docs/SPEC.md §8.2, §11.2): it asks each installed agent again
// (Deps.ProbeModels), then reports per agent what it offers, or why
// its models are unknown. It fails when no agent is installed or a
// setting names an agent that isn't, and warns about settings the
// agents' answers no longer back: models and coordinator_model names
// no agent offers, a default_model, the older models list.
func Models(d Deps) []Check {
	const g = "agents"
	cfg, err := config.Load()
	if err != nil {
		return nil // Settings reports it
	}
	reg, _ := agent.Load(d.Paths.AgentsDir())
	if reg == nil {
		return nil // Agents reports it
	}
	var installed []string
	for _, name := range reg.Names() {
		a, _ := reg.Get(name)
		if m := agent.ManifestOf(a); m != nil && d.LookPath != nil {
			if _, err := d.LookPath(m.Launch.Command); err == nil {
				installed = append(installed, name)
			}
		}
	}
	var out []Check
	if len(installed) == 0 {
		return append(out, Check{Group: g, Name: "agent", Status: Fail, Detail: errMsg(models.NoAgent(reg))})
	}
	now := time.Now()
	var cats []models.Catalog
	for _, name := range installed {
		a, _ := reg.Get(name)
		m := agent.ManifestOf(a)
		if d.ProbeModels != nil && m.ListModels != nil {
			_, _ = d.ProbeModels(m)
		}
		c := models.Get(m, cfg, true)
		cats = append(cats, c)
		ck := Check{Group: g, Name: name + " models"}
		if !c.Known {
			ck.Status = Warn
			ck.Detail = fmt.Sprintf("unknown: %s. Threads run %s's own default and --model is refused", c.Reason, name)
			if c.HasCache && c.Cache.Status == models.StatusLoggedOut {
				ck.Detail += fmt.Sprintf("; log in to %s to see your account's models", m.Launch.Command)
			}
			out = append(out, ck)
			continue
		}
		ck.Status = OK
		ck.Detail = fmt.Sprintf("%d models, %s", len(c.Models), c.Source(now))
		var extra []string
		if n := len(c.Hidden); n > 0 {
			extra = append(extra, fmt.Sprintf("hidden by you: %d", n))
		}
		var yours []string
		for _, x := range c.Models {
			if x.Yours {
				yours = append(yours, x.Name)
			}
		}
		if len(yours) > 0 {
			extra = append(extra, "yours: "+strings.Join(yours, ", "))
		}
		for _, x := range c.Refused {
			extra = append(extra, fmt.Sprintf("refused for your account: %s (%s)", x.Name, x.Refusal.Reason))
		}
		if len(extra) > 0 {
			ck.Detail += "; " + strings.Join(extra, "; ")
		}
		out = append(out, ck)
	}
	warn := func(format string, args ...any) {
		out = append(out, Check{Group: g, Name: "models", Status: Warn, Detail: fmt.Sprintf(format, args...)})
	}
	fail := func(format string, args ...any) {
		out = append(out, Check{Group: g, Name: "agent", Status: Fail, Detail: fmt.Sprintf(format, args...)})
	}
	catOf := func(name string) (models.Catalog, bool) {
		for _, c := range cats {
			if c.Agent == name {
				return c, true
			}
		}
		return models.Catalog{}, false
	}
	for _, name := range cfg.AgentNames() {
		if _, ok := reg.Get(name); !ok {
			warn("config.toml has models settings for agent %s, which tm doesn't know (tm agent list)", name)
			continue
		}
		s := cfg.Agent(name)
		if s.HasLegacy {
			warn("config.toml [agents.%s] models is the older full list: tm now asks %s for its models; the names it doesn't list count as yours, and Settings > Models saves the new form", name, name)
		}
		c, ok := catOf(name)
		if ok && c.Known && s.DefaultModel != "" && !c.Has(s.DefaultModel) {
			warn("%s's default model %s isn't offered now, so threads run %s's own default; pick another in Settings > Models", name, s.DefaultModel, name)
		}
	}
	for _, name := range reg.Names() {
		a, _ := reg.Get(name)
		if m := agent.ManifestOf(a); m != nil && len(m.LegacyModels) > 0 {
			warn("%s's manifest (%s) has [[models]], which tm no longer reads: tm asks the agent ([list_models], docs/SPEC.md §8.2)", name, reg.Source[name])
		}
	}
	// The settings that name agents and models, for all projects and
	// each project with its own.
	type scope struct {
		who    string
		safety config.Safety
		own    []string
	}
	var scopes []scope
	if all, err := cfg.AllProjects(); err == nil {
		scopes = append(scopes, scope{config.AllProjectsName, all, []string{"thread_agent", "coordinator_agent", "models", "coordinator_model"}})
	}
	for _, slug := range cfg.ProjectSlugs() {
		if s, err := cfg.Safety(slug); err == nil {
			scopes = append(scopes, scope{slug, s, cfg.Own(slug)})
		}
	}
	stale := map[string][]string{}
	for _, sc := range scopes {
		for _, k := range []struct{ key, value, what string }{
			{"thread_agent", sc.safety.ThreadAgent, "thread agent"},
			{"coordinator_agent", sc.safety.CoordinatorAgent, "coordinator agent"},
		} {
			if !slices.Contains(sc.own, k.key) {
				continue
			}
			if _, _, err := models.Resolve(reg, installed, k.value, k.what); err != nil && k.value != "" {
				fail("%s: %s %s", sc.who, k.key, errMsg(err))
			}
		}
		if slices.Contains(sc.own, "models") {
			for _, n := range sc.safety.Models {
				offered := false
				for _, c := range cats {
					offered = offered || c.Has(n)
				}
				if !offered {
					stale[n] = append(stale[n], sc.who)
				}
			}
		}
		if slices.Contains(sc.own, "coordinator_model") && sc.safety.CoordinatorModel != "" {
			name, _, err := models.Resolve(reg, installed, sc.safety.CoordinatorAgent, "coordinator agent")
			if c, ok := catOf(name); err == nil && ok && c.Known {
				if err := c.Check(sc.safety.CoordinatorModel, sc.safety.Models); err != nil {
					warn("%s: coordinator_model %s: %s; the coordinator runs %s's default", sc.who, sc.safety.CoordinatorModel, errMsg(err), name)
				}
			}
		}
	}
	if len(installed) > 1 {
		all, _ := cfg.AllProjects()
		if all.ThreadAgent == "" || all.CoordinatorAgent == "" {
			warn("several agents are installed (%s) and the settings name none for all projects: the dashboard asks once when a project opens; until then tm thread start needs --agent", strings.Join(installed, ", "))
		}
	}
	var names []string
	for n := range stale {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		warn("model %s is in the models of %s, but no installed agent offers it now; --model %s is refused", n, strings.Join(stale[n], ", "), n)
	}
	return out
}

// errMsg is a models refusal's message without its code.
func errMsg(err error) string {
	if me, ok := err.(*models.Error); ok {
		return me.Msg
	}
	return err.Error()
}
