package agent

import (
	"io/fs"
	"sort"
)

// BuiltinUnsetEnv returns the unset_env patterns of every built-in agent.
// The server drops them from every session it starts, shells included: a
// shell started from inside an agent would otherwise pass that agent's
// per-session variables on to a copy of the agent run in the shell.
func BuiltinUnsetEnv() []string {
	seen := map[string]bool{}
	names, _ := fs.Glob(builtin, "manifests/*.toml")
	for _, n := range names {
		data, err := builtin.ReadFile(n)
		if err != nil {
			continue
		}
		m, err := ParseManifest(data)
		if err != nil {
			continue
		}
		for _, p := range m.Launch.UnsetEnv {
			seen[p] = true
		}
	}
	out := make([]string, 0, len(seen))
	for p := range seen {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}
