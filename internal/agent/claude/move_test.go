package claude

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/theclifmeister/terminatr/internal/agent"
)

func TestMoveDir(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	m, ok := load(t).(agent.Mover)
	if !ok {
		t.Fatal("the claude agent is no Mover")
	}
	home := t.TempDir()
	from, to := "/h/.terminatr/worktrees/demo/t-0001-x", "/h/.terminatr/worktrees/demo2/t-0001-x"
	if k := folderKey(from); k != "-h--terminatr-worktrees-demo-t-0001-x" {
		t.Fatalf("key %q", k)
	}
	// A folder Claude never ran in: nothing to do.
	if err := m.MoveDir(home, from, to); err != nil {
		t.Fatal(err)
	}
	projects := filepath.Join(home, ".claude", "projects")
	src, dst := filepath.Join(projects, folderKey(from)), filepath.Join(projects, folderKey(to))
	os.MkdirAll(filepath.Join(src, "memory"), 0o700)
	os.WriteFile(filepath.Join(src, "a.jsonl"), []byte("a"), 0o600)
	os.WriteFile(filepath.Join(src, "b.jsonl"), []byte("old b"), 0o600)
	os.WriteFile(filepath.Join(src, "memory", "m.md"), []byte("m"), 0o600)
	// The new folder has a conversation already: the two are merged, and
	// what it has stays.
	os.MkdirAll(dst, 0o700)
	os.WriteFile(filepath.Join(dst, "b.jsonl"), []byte("new b"), 0o600)
	os.WriteFile(filepath.Join(home, ".claude.json"), []byte(`{"numStartups": 3, "projects": {"`+from+`": {"hasTrustDialogAccepted": true, "allowedTools": ["x"]}}}`), 0o600)

	if err := m.MoveDir(home, from, to); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{"a.jsonl": "a", "b.jsonl": "new b", "memory/m.md": "m"} {
		if b, err := os.ReadFile(filepath.Join(dst, name)); err != nil || string(b) != want {
			t.Errorf("%s: %q %v", name, b, err)
		}
	}
	if _, err := os.Stat(filepath.Join(src, "a.jsonl")); !os.IsNotExist(err) {
		t.Errorf("a.jsonl left behind: %v", err)
	}
	var cfg struct {
		NumStartups int `json:"numStartups"`
		Projects    map[string]struct {
			Trusted bool     `json:"hasTrustDialogAccepted"`
			Tools   []string `json:"allowedTools"`
		} `json:"projects"`
	}
	b, _ := os.ReadFile(filepath.Join(home, ".claude.json"))
	if err := json.Unmarshal(b, &cfg); err != nil || cfg.NumStartups != 3 || !cfg.Projects[to].Trusted || len(cfg.Projects[to].Tools) != 1 || !cfg.Projects[from].Trusted {
		t.Fatalf(".claude.json: %v\n%s", err, b)
	}
}

// Claude names a folder by its real path; the old one is gone by then,
// but its parent's link still resolves.
func TestMoveDirRealPath(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	m := load(t).(agent.Mover)
	home := t.TempDir()
	real := t.TempDir()
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	real, _ = filepath.EvalSymlinks(real)
	os.MkdirAll(filepath.Join(real, "new"), 0o755)
	projects := filepath.Join(home, ".claude", "projects")
	os.MkdirAll(filepath.Join(projects, folderKey(filepath.Join(real, "old"))), 0o700)
	os.WriteFile(filepath.Join(projects, folderKey(filepath.Join(real, "old")), "s.jsonl"), []byte("s"), 0o600)
	os.WriteFile(filepath.Join(home, ".claude.json"), []byte(`{"projects": {"`+filepath.Join(real, "old")+`": {"hasTrustDialogAccepted": true}}}`), 0o600)
	if err := m.MoveDir(home, filepath.Join(link, "old"), filepath.Join(link, "new")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(projects, folderKey(filepath.Join(real, "new")), "s.jsonl")); err != nil {
		t.Fatalf("not moved: %v", err)
	}
	if b, _ := os.ReadFile(filepath.Join(home, ".claude.json")); !strings.Contains(string(b), `"`+filepath.Join(real, "new")+`"`) {
		t.Fatalf(".claude.json:\n%s", b)
	}
}
