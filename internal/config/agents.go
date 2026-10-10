package config

import (
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"
)

// An agent's model settings in config.toml (docs/SPEC.md §8.2, Models):
//
//	[agents.claude]
//	hide = ["claude-opus-4-6"]
//	add = ["arn:aws:bedrock:…"]
//	default_model = "opus"
//
// tm ships no model list: the models are what the installed agent says
// (internal/models). These settings lay the user's over it: hide leaves
// listed models out, add offers models the agent accepts but doesn't
// list (a Bedrock inference profile, a custom provider's), and
// default_model is the model a launch passes when none is chosen ("" or
// unset: none, so the agent runs its own default). The Settings popup's
// Models page writes them. The older models = [{ name, about }, …]
// list (a full catalog) is still read: its names count as added, its
// about lines are not used, and the next save from the popup replaces
// it. Which agents exist and what they list are checked by
// internal/models, since config doesn't read the manifests.

// AgentModel is one entry of the older models list.
type AgentModel struct {
	Name  string `toml:"name" json:"name"`
	About string `toml:"about" json:"about"`
}

// AgentSettings is one agent's [agents.<name>] table.
type AgentSettings struct {
	Hide         []string
	Add          []string
	DefaultModel string
	// Legacy is the older models list, HasLegacy when the file has one.
	Legacy    []AgentModel
	HasLegacy bool
}

type rawAgent struct {
	Hide         *[]string     `toml:"hide"`
	Add          *[]string     `toml:"add"`
	DefaultModel *string       `toml:"default_model"`
	Models       *[]AgentModel `toml:"models"`
}

func (r rawAgent) settings() AgentSettings {
	var s AgentSettings
	if r.Hide != nil {
		s.Hide = append([]string{}, (*r.Hide)...)
	}
	if r.Add != nil {
		s.Add = append([]string{}, (*r.Add)...)
	}
	if r.DefaultModel != nil {
		s.DefaultModel = *r.DefaultModel
	}
	if r.Models != nil {
		s.Legacy, s.HasLegacy = append([]AgentModel{}, (*r.Models)...), true
	}
	return s
}

// Added is the models the user added: add, then the older list's names.
func (s AgentSettings) Added() []string {
	out := append([]string{}, s.Add...)
	for _, m := range s.Legacy {
		if !slices.Contains(out, m.Name) {
			out = append(out, m.Name)
		}
	}
	return out
}

// Empty reports whether the table sets nothing.
func (s AgentSettings) Empty() bool {
	return len(s.Hide) == 0 && len(s.Add) == 0 && s.DefaultModel == "" && !s.HasLegacy
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

// checkNames checks a list of model names: each one word, none twice.
func checkNames(key string, names []string) error {
	seen := map[string]bool{}
	for _, n := range names {
		if err := CheckModelName(n); err != nil {
			return fmt.Errorf("%s: %w", key, err)
		}
		if seen[n] {
			return fmt.Errorf("%s lists %q twice", key, n)
		}
		seen[n] = true
	}
	return nil
}

// check checks an [agents.<name>] table's values. Whether the agent
// lists them is internal/models' to say.
func (s AgentSettings) check() error {
	if err := checkNames("hide", s.Hide); err != nil {
		return err
	}
	if err := checkNames("add", s.Add); err != nil {
		return err
	}
	for _, m := range s.Legacy {
		if err := CheckModelName(m.Name); err != nil {
			return fmt.Errorf("models: %w", err)
		}
	}
	if s.DefaultModel != "" {
		if err := CheckModelName(s.DefaultModel); err != nil {
			return fmt.Errorf("default_model: %w", err)
		}
	}
	return nil
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

// SetAgentModels writes agent name's model settings as s says, in one
// write: hide, add and default_model are set when s has them and
// removed when not, and the older models list is removed (its names
// are in s.Add when the caller kept them), so AgentSettings{} resets
// the agent to what it lists itself.
func SetAgentModels(name string, s AgentSettings) error {
	if err := checkAgentTable(name); err != nil {
		return fmt.Errorf("the agent %w", err)
	}
	s.Legacy, s.HasLegacy = nil, false
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
		}{{"hide", len(s.Hide) > 0, append([]string{}, s.Hide...)}, {"add", len(s.Add) > 0, append([]string{}, s.Add...)},
			{"default_model", s.DefaultModel != "", s.DefaultModel}, {"models", false, nil}} {
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
