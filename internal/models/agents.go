package models

import (
	"context"
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/theclifmeister/terminatr/internal/agent"
	"github.com/theclifmeister/terminatr/internal/config"
	"github.com/theclifmeister/terminatr/internal/home"
	"github.com/theclifmeister/terminatr/internal/plat/shell"
)

// Error is a refusal with a code, as tm thread start reports it.
type Error struct{ Code, Msg string }

func (e *Error) Error() string { return e.Code + ": " + e.Msg }

// LookPath finds the agent's command on env's PATH (nil: tm's own).
func LookPath(m *agent.Manifest, env []string) (string, error) {
	if env == nil {
		env = os.Environ()
	}
	path := ""
	for _, kv := range env {
		if v, ok := strings.CutPrefix(kv, "PATH="); ok {
			path = v
		}
	}
	return shell.LookPathIn(m.Launch.Command, path)
}

// Installed lists, in the registry's order, the agents whose command is
// on env's PATH (nil: tm's own).
func Installed(reg *agent.Registry, env []string) []string {
	var out []string
	if reg == nil {
		return nil
	}
	for _, name := range reg.Names() {
		a, _ := reg.Get(name)
		if m := agent.ManifestOf(a); m != nil {
			if _, err := LookPath(m, env); err == nil {
				out = append(out, name)
			}
		}
	}
	return out
}

// The ways an agent can't be resolved.
const (
	CodeNoAgent      = "no-agent"
	CodeNotInstalled = "agent-not-installed"
	CodeNotChosen    = "agent-not-chosen"
	CodeUnknownAgent = "unknown-agent"
)

// Resolve is the agent a project's thread or coordinator runs: setting
// when the user named one (refused when it isn't installed: never
// replaced by another), else the one installed agent, auto. With none
// installed, or several and no setting, it is refused: the TUI asks
// the user which one to save. what names the setting ("thread agent",
// "coordinator agent").
func Resolve(reg *agent.Registry, installed []string, setting, what string) (name string, auto bool, err error) {
	if setting != "" {
		if _, ok := reg.Get(setting); !ok {
			return "", false, &Error{CodeUnknownAgent, fmt.Sprintf("the %s is %q, which tm doesn't know (tm agent list): pick another in the project popup", what, setting)}
		}
		if !slices.Contains(installed, setting) {
			return "", false, NotInstalled(reg, setting, installed)
		}
		return setting, false, nil
	}
	switch len(installed) {
	case 0:
		return "", false, NoAgent(reg)
	case 1:
		return installed[0], true, nil
	}
	return "", false, &Error{CodeNotChosen, fmt.Sprintf("several agents are installed (%s) and the project names no %s: pick one in the project popup (enter on the project asks), or pass --agent", strings.Join(installed, ", "), what)}
}

// NoAgent is the refusal when no agent is installed.
func NoAgent(reg *agent.Registry) error {
	var known []string
	if reg != nil {
		for _, n := range reg.Names() {
			a, _ := reg.Get(n)
			if m := agent.ManifestOf(a); m != nil {
				known = append(known, fmt.Sprintf("%s (%s)", m.Display, m.Launch.Command))
			}
		}
	}
	return &Error{CodeNoAgent, fmt.Sprintf("no agent is installed, so tm can't run coordinators or threads: install one of %s, or add a manifest (tm agent list); tm doctor says more", strings.Join(known, ", "))}
}

// NotInstalled is the refusal for an agent whose command isn't on PATH.
func NotInstalled(reg *agent.Registry, name string, installed []string) error {
	cmd := name
	if a, ok := reg.Get(name); ok {
		if m := agent.ManifestOf(a); m != nil {
			cmd = m.Launch.Command
		}
	}
	other := "none"
	if len(installed) > 0 {
		other = strings.Join(installed, ", ")
	}
	hint := "install it"
	if len(installed) > 0 {
		hint = "pick another in the project popup, or pass --agent " + installed[0]
	}
	return &Error{CodeNotInstalled, fmt.Sprintf("%s isn't installed (%s not found on PATH); installed: %s. %s", name, cmd, other, hint)}
}

// Registry loads the agents tm knows: the built-in manifests and the
// user's (a broken user manifest is skipped; nil only when the home
// can't be found).
func Registry() *agent.Registry {
	dir, err := home.AgentsDir()
	if err != nil {
		return nil
	}
	reg, _ := agent.Load(dir)
	return reg
}

// CatalogOf is the named agent's catalog, given the installed agents.
func CatalogOf(reg *agent.Registry, cfg *config.Config, name string, installed []string) (Catalog, bool) {
	a, ok := reg.Get(name)
	m := agent.ManifestOf(a)
	if !ok || m == nil {
		return Catalog{}, false
	}
	return Get(m, cfg, slices.Contains(installed, name)), true
}

// Catalogs are the installed agents' catalogs, in the registry's order.
func Catalogs(reg *agent.Registry, cfg *config.Config, installed []string) []Catalog {
	var out []Catalog
	for _, name := range installed {
		if c, ok := CatalogOf(reg, cfg, name, installed); ok {
			out = append(out, c)
		}
	}
	return out
}

// Ensure asks an installed agent that was never asked, so a first
// thread start knows its models; a known answer is left to the
// server's refreshes and tm doctor.
func Ensure(ctx context.Context, reg *agent.Registry, name string, env []string) {
	if _, ok := Load(name); ok {
		return
	}
	a, ok := reg.Get(name)
	if m := agent.ManifestOf(a); ok && m != nil && m.ListModels != nil {
		_, _ = Refresh(ctx, m, env)
	}
}
