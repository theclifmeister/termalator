package config

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// An agent's models catalog in config.toml (docs/SPEC.md §8.2, §11.2):
//
//	[agents.claude]
//	models = [{ name = "opus", about = "most capable" }, { name = "sonnet", about = "balanced" }]
//	default_model = "opus"
//
// models replaces the manifest's [[models]] as a whole, so a model can be
// added, changed or removed without a release; unset, the manifest's
// list is the catalog. default_model is the model a launch passes when
// none is chosen ("" for none: the agent runs the user's own default);
// unset, the manifest's default, while the catalog still lists it. The
// Settings popup's Models page writes both. Which agents exist and
// whether default_model is in the catalog are checked by the agent
// layer (agent.Models), since config doesn't read the manifests.

// AgentModel is one model of an agent's catalog: a name the agent's
// model_args accept, and one line on when it fits.
type AgentModel struct {
	Name  string `toml:"name" json:"name"`
	About string `toml:"about" json:"about"`
}

// AgentSettings is one agent's [agents.<name>] table.
type AgentSettings struct {
	// Models is the catalog, with HasModels; unset (HasModels false), the
	// manifest's [[models]].
	Models    []AgentModel
	HasModels bool
	// DefaultModel, with HasDefault, is default_model: "" is none.
	DefaultModel string
	HasDefault   bool
}

type rawAgent struct {
	Models       *[]AgentModel `toml:"models"`
	DefaultModel *string       `toml:"default_model"`
}

func (r rawAgent) settings() AgentSettings {
	var s AgentSettings
	if r.Models != nil {
		s.Models, s.HasModels = append([]AgentModel{}, (*r.Models)...), true
	}
	if r.DefaultModel != nil {
		s.DefaultModel, s.HasDefault = *r.DefaultModel, true
	}
	return s
}

// MaxModelAbout is the most characters of a model's about line.
const MaxModelAbout = 120

var arrayHeaderRE = regexp.MustCompile(`^\s*\[\[\s*([^\[\]#]+?)\s*\]\]\s*(#.*)?$`)

var modelNameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/@\[\]-]{0,63}$`)

// CheckModelName checks a model's name: one word of letters, digits and
// ._:/@[]-, as an agent's model_args pass it.
func CheckModelName(name string) error {
	if !modelNameRE.MatchString(name) {
		return fmt.Errorf("model name %q is not one word of letters, digits and ._:/@[]- (at most 64)", name)
	}
	return nil
}

// CheckModelAbout checks a model's about: one line of 1 to MaxModelAbout
// characters.
func CheckModelAbout(about string) error {
	if strings.TrimSpace(about) == "" || strings.ContainsAny(about, "\r\n") || len([]rune(about)) > MaxModelAbout {
		return fmt.Errorf("a model's about must be one line of 1 to %d characters", MaxModelAbout)
	}
	return nil
}

// CheckCatalog checks a models catalog: each name and about, no name
// twice. An empty catalog is valid: the agent then offers no models.
func CheckCatalog(models []AgentModel) error {
	seen := map[string]bool{}
	for _, m := range models {
		if err := CheckModelName(m.Name); err != nil {
			return err
		}
		if err := CheckModelAbout(m.About); err != nil {
			return fmt.Errorf("%s: %w", m.Name, err)
		}
		if seen[m.Name] {
			return fmt.Errorf("lists %q twice", m.Name)
		}
		seen[m.Name] = true
	}
	return nil
}

// check checks an [agents.<name>] table's values; a default_model the
// table's own models leave out is an error, one the manifest's would is
// the agent layer's.
func (s AgentSettings) check() error {
	if s.HasModels {
		if err := CheckCatalog(s.Models); err != nil {
			return fmt.Errorf("models %w", err)
		}
	}
	if !s.HasDefault || s.DefaultModel == "" {
		return nil
	}
	if err := CheckModelName(s.DefaultModel); err != nil {
		return fmt.Errorf("default_model: %w", err)
	}
	if s.HasModels && !s.Lists(s.DefaultModel) {
		return fmt.Errorf("default_model %q isn't one of its models", s.DefaultModel)
	}
	return nil
}

// Lists reports whether the table's own models name name.
func (s AgentSettings) Lists(name string) bool {
	for _, m := range s.Models {
		if m.Name == name {
			return true
		}
	}
	return false
}

// Agent returns the [agents.<name>] settings; the zero value when the
// file has none (the manifest's catalog and default).
func (c *Config) Agent(name string) AgentSettings {
	if c == nil {
		return AgentSettings{}
	}
	return c.agents[name].settings()
}

// AgentNames lists, sorted, the agents config.toml has a table for.
func (c *Config) AgentNames() []string {
	if c == nil {
		return nil
	}
	var out []string
	for n := range c.agents {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// checkAgentTable checks an agent's name as a table name: one word
// without dots.
func checkAgentTable(name string) error {
	if err := CheckAgent(name); err != nil {
		return err
	}
	if strings.ContainsAny(name, ".[]'#=") {
		return fmt.Errorf("must be one agent's name, not %q", name)
	}
	return nil
}

// AgentTable is the table of agent name's settings.
func AgentTable(name string) string { return "agents." + name }

// SetAgentModels writes agent name's models catalog and default as s
// says, in one write: each key s has is set, each it hasn't is removed,
// so the manifest's applies again (AgentSettings{} resets both). s is
// checked first: a default_model its models leave out is refused.
func SetAgentModels(name string, s AgentSettings) error {
	if err := checkAgentTable(name); err != nil {
		return fmt.Errorf("the agent %w", err)
	}
	if err := s.check(); err != nil {
		return err
	}
	table := AgentTable(name)
	return edit(func(data []byte) ([]byte, error) {
		// [[agents.<name>.models]] tables are the user's to edit by hand:
		// the line editor would add a second definition.
		for _, l := range strings.SplitAfter(string(data), "\n") {
			if m := arrayHeaderRE.FindStringSubmatch(strings.TrimRight(l, "\r\n")); m != nil {
				if h := splitTable(m[1]); len(h) >= 2 && h[0] == "agents" && h[1] == name {
					return nil, ErrForm
				}
			}
		}
		for _, k := range []struct {
			key string
			has bool
			val any
		}{{"models", s.HasModels, append([]AgentModel{}, s.Models...)}, {"default_model", s.HasDefault, s.DefaultModel}} {
			if k.has {
				out, err := Edit(data, table, k.key, k.val)
				if err != nil {
					return nil, err
				}
				data = out
				continue
			}
			out := Remove(data, table, k.key)
			if string(out) == string(data) && sets(data, table, k.key) {
				return nil, ErrForm
			}
			data = out
		}
		return data, nil
	})
}
