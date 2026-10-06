package claude

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/theclifmeister/terminatr/internal/agent"
)

func modSpec(dir string, mods bool) agent.LaunchSpec {
	return agent.LaunchSpec{Role: agent.RoleThread, SessionID: "s-1", AgentSID: "abc", Cwd: dir,
		RuntimeDir: dir, TMBin: "/usr/local/bin/tm", Socket: "/tmp/tm.sock", Mods: mods}
}

// TestLaunchMods: with Mods the plugin folder also holds the mod, and
// hooks.json names it beside the command hooks, which stay.
func TestLaunchMods(t *testing.T) {
	a := load(t)
	if _, ok := a.(agent.Modder); !ok {
		t.Fatal("claude is not a Modder")
	}
	for _, mods := range []bool{false, true} {
		l, err := a.Launch(modSpec(t.TempDir(), mods))
		if err != nil {
			t.Fatal(err)
		}
		var hooks struct {
			Modules []string                   `json:"modules"`
			Hooks   map[string]json.RawMessage `json:"hooks"`
		}
		if err := json.Unmarshal(l.Files["claude-plugin/hooks/hooks.json"], &hooks); err != nil {
			t.Fatalf("mods %v: hooks.json: %v", mods, err)
		}
		var plugin map[string]string
		if err := json.Unmarshal(l.Files["claude-plugin/.claude-plugin/plugin.json"], &plugin); err != nil {
			t.Fatalf("mods %v: plugin.json: %v", mods, err)
		}
		if len(hooks.Hooks) < 16 || plugin["name"] != "terminatr" {
			t.Fatalf("mods %v: %d command hooks, plugin %v", mods, len(hooks.Hooks), plugin)
		}
		_, reg := l.Files["claude-plugin/hooks/register.ts"]
		_, types := l.Files["claude-plugin/types/index.d.ts"]
		_, feed := l.Files["claude-plugin/hooks/feed.ts"]
		if !mods {
			if len(hooks.Modules) != 0 || plugin["types"] != "" || reg || types || feed {
				t.Fatalf("mods off: modules %v, types %q, files %v %v %v", hooks.Modules, plugin["types"], reg, types, feed)
			}
			continue
		}
		if strings.Join(hooks.Modules, ",") != "./register.ts" || plugin["types"] != "./types/index.d.ts" || !reg || !types || !feed {
			t.Fatalf("mods on: modules %v, types %q, files %v %v %v", hooks.Modules, plugin["types"], reg, types, feed)
		}
		for p := range l.Files {
			if strings.Contains(p, "tests/") {
				t.Fatalf("a session got the mod's test %s", p)
			}
		}
	}
}

// writePlugin renders the plugin a session with mods gets into dir, with
// the mod's tests, and returns the plugin folder.
func writePlugin(t *testing.T, dir string) string {
	t.Helper()
	l, err := load(t).Launch(modSpec(dir, true))
	if err != nil {
		t.Fatal(err)
	}
	files := l.Files
	for p, b := range ModFiles(true) {
		files[filepath.Join(pluginDir, p)] = b
	}
	for p, b := range files {
		f := filepath.Join(dir, p)
		if err := os.MkdirAll(filepath.Dir(f), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(f, b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return filepath.Join(dir, pluginDir)
}

// TestModPlugin runs `claude plugin validate` and `claude plugin test`
// on the plugin exactly as a session gets it. It needs the Claude Code
// CLI, so it runs only with TERMINATR_CLAUDE_PLUGIN_CHECK=1 (CI's mods
// job); `claude` is then required.
func TestModPlugin(t *testing.T) {
	if os.Getenv("TERMINATR_CLAUDE_PLUGIN_CHECK") != "1" {
		t.Skip("set TERMINATR_CLAUDE_PLUGIN_CHECK=1 to run claude plugin validate/test")
	}
	claude, err := exec.LookPath("claude")
	if err != nil {
		t.Fatal(err)
	}
	dir := writePlugin(t, t.TempDir())
	for _, args := range [][]string{{"plugin", "validate", dir}, {"plugin", "test", dir}} {
		out, err := exec.Command(claude, args...).CombinedOutput()
		t.Logf("claude %s:\n%s", strings.Join(args, " "), out)
		if err != nil {
			t.Fatalf("claude %s: %v", strings.Join(args, " "), err)
		}
	}
}
