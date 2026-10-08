package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
)

// TestLaunchRepoValues: launch templates see the repo root and the
// worktree's own git dir, and read the brief as a TOML-quoted value
// (Codex's -c developer_instructions=…, T97).
func TestLaunchRepoValues(t *testing.T) {
	brief := filepath.Join(t.TempDir(), "brief.md")
	text := "# T1 \"quoted\"\n\tC:\\path\x01\x7f é\n"
	os.WriteFile(brief, []byte(text), 0o600)
	m, err := ParseManifest([]byte(`manifest_version = 1
name = "a"
[launch]
command = "a"
args = ["-c", 'projects={ {{- toml .RepoRoot}}={trust_level="trusted"} }', "--write", "{{.GitDir}}",
  "-c", "developer_instructions={{toml (file .BriefPath)}}", "{{json (file .BriefPath)}}"]
`))
	if err != nil {
		t.Fatal(err)
	}
	spec := threadSpec()
	spec.Cwd, spec.RepoRoot, spec.GitDir, spec.BriefPath = "/r/wt", "/r/repo", "/r/repo/.git/worktrees/wt", brief
	l, err := FromManifest(m).Launch(spec)
	if err != nil {
		t.Fatal(err)
	}
	if got := l.Argv[2]; got != `projects={"/r/repo"={trust_level="trusted"} }` {
		t.Errorf("trust arg %q", got)
	}
	if l.Argv[4] != spec.GitDir {
		t.Errorf("git dir arg %q", l.Argv[4])
	}
	var v struct {
		DeveloperInstructions string `toml:"developer_instructions"`
	}
	if _, err := toml.Decode(l.Argv[6], &v); err != nil || v.DeveloperInstructions != text {
		t.Errorf("developer_instructions %q -> %q %v", l.Argv[6], v.DeveloperInstructions, err)
	}
	if want := "\"# T1 \\\"quoted\\\"\\n\\tC:\\\\path\\u0001\x7f é\\n\""; l.Argv[7] != want {
		t.Errorf("json brief %s, want %s", l.Argv[7], want)
	}

	spec.BriefPath = filepath.Join(t.TempDir(), "missing.md")
	if _, err := FromManifest(m).Launch(spec); err == nil {
		t.Error("a missing file must fail the launch")
	}
	spec.BriefPath = "brief.md"
	if _, err := FromManifest(m).Launch(spec); err == nil || !strings.Contains(err.Error(), "absolute") {
		t.Errorf("a relative path: %v", err)
	}
	big := filepath.Join(t.TempDir(), "big.md")
	os.WriteFile(big, make([]byte, maxFileText+1), 0o600)
	spec.BriefPath = big
	if _, err := FromManifest(m).Launch(spec); err == nil || !strings.Contains(err.Error(), "larger") {
		t.Errorf("a file over the bound: %v", err)
	}
}

// TestTOMLString: every string comes back from a TOML decoder as it went
// in (invalid UTF-8 as U+FFFD).
func TestTOMLString(t *testing.T) {
	for _, s := range []string{"", "plain", `a "b" \c`, "\b\t\n\f\r\x00\x1f\x7f", "ünï 🙂", "''' \"\"\" ${x} {{y}}"} {
		var v struct{ K string }
		if _, err := toml.Decode("K = "+tomlString(s), &v); err != nil || v.K != s {
			t.Errorf("%q -> %s -> %q %v", s, tomlString(s), v.K, err)
		}
	}
	var v struct{ K string }
	if _, err := toml.Decode("K = "+tomlString("a\xffb"), &v); err != nil || v.K != "a�b" {
		t.Errorf("invalid UTF-8 -> %q %v", v.K, err)
	}
}
