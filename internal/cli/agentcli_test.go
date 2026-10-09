package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	// The Go agents, registered as cmd/tm registers them.
	_ "github.com/theclifmeister/terminatr/internal/agent/claude"
	_ "github.com/theclifmeister/terminatr/internal/agent/codex"
)

// TestAgentCheckRender: --render writes a sample launch's files and
// prints the launch the agent's Go side makes (Codex's hooks included),
// which CI's agent canary hands the latest releases.
func TestAgentCheckRender(t *testing.T) {
	for _, name := range []string{"claude", "codex"} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			var out, errb bytes.Buffer
			e := &Env{Stdout: &out, Stderr: &errb, Getenv: func(string) string { return "" }}
			file := filepath.Join("..", "agent", "manifests", name+".toml")
			if code := agentCheck(e, []string{"--render", dir, "--tm-bin", "/x/tm", file}); code != ExitOK {
				t.Fatalf("exit %d: %s", code, errb.String())
			}
			var l struct{ Argv, Env []string }
			if err := json.Unmarshal(out.Bytes(), &l); err != nil || len(l.Argv) == 0 || l.Argv[0] != name {
				t.Fatalf("launch %v: %s", err, out.String())
			}
			if !slices.Contains(l.Env, "TERMINATR_BIN=/x/tm") {
				t.Errorf("env %v", l.Env)
			}
			switch name {
			case "claude":
				b, err := os.ReadFile(filepath.Join(dir, "claude-plugin", "hooks", "hooks.json"))
				if err != nil || !strings.Contains(string(b), `"/x/tm"`) {
					t.Errorf("hooks.json: %v %s", err, b)
				}
			case "codex":
				if !slices.ContainsFunc(l.Argv, func(a string) bool { return strings.HasPrefix(a, "hooks.state=") }) {
					t.Errorf("no hooks in %q", l.Argv)
				}
			}
		})
	}
}
