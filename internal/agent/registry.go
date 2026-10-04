package agent

import (
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

//go:embed manifests/*.toml
var builtin embed.FS

// goAgents holds agents that need Go code. A Go agent registers a
// constructor that receives its manifest and wraps FromManifest.
var goAgents = map[string]func(*Manifest) Agent{}

// RegisterGo registers Go code for the agent named name. Call it from an
// init function in internal/agent/<name>.
func RegisterGo(name string, wrap func(*Manifest) Agent) { goAgents[name] = wrap }

// Registry is the set of agents known to this binary and this user.
type Registry struct {
	agents map[string]Agent
	// Source records where each agent's manifest came from, for `tm agent list`.
	Source map[string]string
}

// Load reads the built-in manifests, then user manifests from userDir
// (~/.termalator/agents). A user manifest with a built-in's name replaces
// it. Broken user manifests are reported but don't stop the others.
func Load(userDir string) (*Registry, error) {
	r := &Registry{agents: map[string]Agent{}, Source: map[string]string{}}
	var errs []error

	names, _ := fs.Glob(builtin, "manifests/*.toml")
	for _, n := range names {
		data, err := builtin.ReadFile(n)
		if err != nil {
			return nil, err
		}
		if err := r.add(data, "builtin:"+n); err != nil {
			// A broken built-in manifest is a bug in this binary.
			return nil, fmt.Errorf("%s: %w", n, err)
		}
	}

	if userDir != "" {
		files, _ := filepath.Glob(filepath.Join(userDir, "*.toml"))
		for _, f := range files {
			data, err := os.ReadFile(f)
			if err == nil {
				err = r.add(data, f)
			}
			if err != nil {
				errs = append(errs, fmt.Errorf("%s: %w", f, err))
			}
		}
	}
	return r, errors.Join(errs...)
}

func (r *Registry) add(data []byte, source string) error {
	m, err := ParseManifest(data)
	if err != nil {
		return err
	}
	if base := strings.TrimSuffix(filepath.Base(source), ".toml"); base != m.Name {
		return fmt.Errorf("file name %q must match name = %q", base, m.Name)
	}
	a := FromManifest(m)
	if wrap, ok := goAgents[m.Name]; ok {
		a = wrap(m)
	}
	r.agents[m.Name] = a
	r.Source[m.Name] = source
	return nil
}

// Get returns the agent named name.
func (r *Registry) Get(name string) (Agent, bool) {
	a, ok := r.agents[name]
	return a, ok
}

// Identify returns the agent whose manifest claims this process.
func (r *Registry) Identify(p ProcessInfo) (Agent, bool) {
	for _, n := range r.Names() {
		if a := r.agents[n]; a.Identify(p) {
			return a, true
		}
	}
	return nil, false
}

// Names lists the known agents, sorted.
func (r *Registry) Names() []string {
	out := make([]string, 0, len(r.agents))
	for n := range r.agents {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}
