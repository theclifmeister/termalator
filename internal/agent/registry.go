package agent

import (
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
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
// (~/.terminatr/agents). A user manifest with a built-in's name replaces
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

// Builtin returns the text of a built-in manifest, e.g. as the starting
// point of a user manifest (docs/SPEC.md §8.7) or a test's copy.
func Builtin(name string) ([]byte, bool) {
	b, err := builtin.ReadFile("manifests/" + name + ".toml")
	return b, err == nil
}

// RoleFiles is the union of the agents' role_files, sorted: the extra
// names the coordinator's role file is linked under. reg nil means the
// built-in manifests.
func RoleFiles(reg *Registry) []string {
	if reg == nil {
		reg, _ = Load("")
	}
	var out []string
	if reg == nil {
		return nil
	}
	for _, n := range reg.Names() {
		if m := ManifestOf(reg.agents[n]); m != nil {
			for _, f := range m.RoleFiles {
				if !slices.Contains(out, f) {
					out = append(out, f)
				}
			}
		}
	}
	sort.Strings(out)
	return out
}

// GuardSecrets is every known agent's [guard] secrets, resolved: the
// built-in manifests' and reg's (nil: the built-ins only), so a user
// manifest replacing a built-in one doesn't drop what it protected.
func GuardSecrets(reg *Registry, home string, getenv func(string) string) []string {
	var out []string
	add := func(m *Manifest) {
		for _, p := range GuardPaths(m.Guard.Secrets, home, getenv) {
			if !slices.Contains(out, p) {
				out = append(out, p)
			}
		}
	}
	names, _ := fs.Glob(builtin, "manifests/*.toml")
	for _, n := range names {
		data, _ := builtin.ReadFile(n)
		if m, err := ParseManifest(data); err == nil {
			add(m)
		}
	}
	if reg != nil {
		for _, n := range reg.Names() {
			if m := ManifestOf(reg.agents[n]); m != nil {
				add(m)
			}
		}
	}
	return out
}

// GuardWritable is the [guard] writable folders of the agent named
// name, resolved: reg's manifest, else the built-in one.
func GuardWritable(reg *Registry, name, home string, getenv func(string) string) []string {
	var m *Manifest
	if reg != nil {
		if a, ok := reg.Get(name); ok {
			m = ManifestOf(a)
		}
	} else if data, ok := Builtin(name); ok {
		m, _ = ParseManifest(data)
	}
	if m == nil {
		return nil
	}
	return GuardPaths(m.Guard.Writable, home, getenv)
}
