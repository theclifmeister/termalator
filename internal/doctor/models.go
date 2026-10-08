package doctor

import (
	"fmt"
	"slices"
	"strings"

	"github.com/theclifmeister/terminatr/internal/agent"
	"github.com/theclifmeister/terminatr/internal/config"
)

// Models checks the agents' models catalogs (docs/SPEC.md §8.2, §11.2):
// each agent's models as threads get them, the manifest's or the user's
// in config.toml, and warns about what the settings name that the
// catalogs no longer have: models for an agent tm doesn't know, a
// default_model the catalog leaves out (no model is passed then), and
// allowed models (Thread models) no agent lists any more.
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
	var out, warns []Check
	var parts, known []string
	for _, name := range reg.Names() {
		a, _ := reg.Get(name)
		models := agent.Models(a, cfg)
		s := cfg.Agent(name)
		if len(models) > 0 || s.HasModels {
			src := "as released"
			if s.HasModels {
				src = "your list"
			}
			parts = append(parts, fmt.Sprintf("%s %d (%s)", name, len(models), src))
		}
		for _, m := range models {
			known = append(known, m.Name)
		}
		if s.HasDefault && s.DefaultModel != "" && agent.DefaultOf(models) == "" {
			warns = append(warns, Check{Group: g, Name: "models", Status: Warn,
				Detail: fmt.Sprintf("%s's default model %s is stale: its models don't list it, so threads run the agent's own default; pick another in Settings > Models", name, s.DefaultModel)})
		}
	}
	for _, name := range cfg.AgentNames() {
		if _, ok := reg.Get(name); !ok {
			warns = append(warns, Check{Group: g, Name: "models", Status: Warn,
				Detail: fmt.Sprintf("config.toml has models for agent %s, which tm doesn't know (tm agent list)", name)})
		}
	}
	stale := map[string][]string{} // model -> who allows it
	var order []string
	note := func(who string, allow []string) {
		for _, n := range allow {
			if slices.Contains(known, n) {
				continue
			}
			if _, ok := stale[n]; !ok {
				order = append(order, n)
			}
			stale[n] = append(stale[n], who)
		}
	}
	if all, err := cfg.AllProjects(); err == nil {
		note(config.AllProjectsName, all.Models)
	}
	for _, slug := range cfg.ProjectSlugs() {
		if slices.Contains(cfg.Own(slug), "models") {
			if s, err := cfg.Safety(slug); err == nil {
				note(slug, s.Models)
			}
		}
	}
	for _, n := range order {
		warns = append(warns, Check{Group: g, Name: "models", Status: Warn,
			Detail: fmt.Sprintf("allowed model %s is stale: no agent lists it (%s); Settings > Thread models shows it, enter leaves it out", n, strings.Join(stale[n], ", "))})
	}
	if len(parts) > 0 {
		out = append(out, Check{Group: g, Name: "models", Status: OK, Detail: strings.Join(parts, ", ")})
	}
	return append(out, warns...)
}
