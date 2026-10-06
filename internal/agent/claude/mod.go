package claude

import (
	"embed"
	"io/fs"
	"path"
	"strings"

	"github.com/theclifmeister/terminatr/internal/agent"
)

// ModsMinVersion is the Claude Code build the mod was tested on
// (docs/SPEC.md §8.6, Mods). The mods API is early access, so an older
// build gets the command hooks alone. CI's claude-mod job pins the
// Claude Code it checks the mod with (CLAUDE_CODE_VERSION in ci.yml):
// bump the two together.
const ModsMinVersion = "2.1.289"

// mod is terminatr's mod: the hooks module, its type contract, and its
// tests for `claude plugin test` (ModFiles). plugin.json and
// hooks/hooks.json come from the manifest, rendered with .Mods.
//
//go:embed mod/hooks/*.ts mod/hooks/*.tsx mod/types/*.d.ts mod/tests/*.ts mod/tests/*.tsx
var mod embed.FS

// pluginDir is where the manifest's launch.files put the plugin, inside
// the runtime dir.
const pluginDir = "claude-plugin"

// ModsMinVersion makes the Claude agent an agent.Modder.
func (a *Agent) ModsMinVersion() string { return ModsMinVersion }

// Launch is the manifest's launch, plus the mod's files with spec.Mods.
func (a *Agent) Launch(spec agent.LaunchSpec) (agent.Launch, error) {
	l, err := a.Agent.Launch(spec)
	if err != nil || !spec.Mods {
		return l, err
	}
	for p, b := range ModFiles(false) {
		l.Files[path.Join(pluginDir, p)] = b
	}
	return l, nil
}

// ModFiles returns the mod's files by their path inside the plugin
// folder (hooks/register.ts, types/index.d.ts), with its tests or
// without: a session gets none, the CI check runs them.
func ModFiles(tests bool) map[string][]byte {
	out := map[string][]byte{}
	fs.WalkDir(mod, "mod", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel := strings.TrimPrefix(p, "mod/")
		if !tests && strings.HasPrefix(rel, "tests/") {
			return nil
		}
		b, err := mod.ReadFile(p)
		if err != nil {
			return err
		}
		out[rel] = b
		return nil
	})
	return out
}
