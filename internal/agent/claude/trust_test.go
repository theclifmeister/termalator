package claude

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/theclifmeister/terminatr/internal/agent"
)

func TestTrustDir(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	a := load(t)
	tr, ok := a.(agent.Truster)
	if !ok {
		t.Fatal("the claude agent is no Truster")
	}
	home := t.TempDir()
	real := filepath.Join(t.TempDir(), "wt")
	os.MkdirAll(real, 0o755)
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	real, _ = filepath.EvalSymlinks(real)
	cfg := filepath.Join(home, ".claude.json")

	// No config yet: it is created with the entry, private.
	if err := tr.TrustDir(home, link); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(cfg)
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("config %v %v", fi, err)
	}

	// Other keys, other projects and other fields of the entry stay.
	os.WriteFile(cfg, []byte(`{"numStartups": 12345678901234567890, "oauthAccount": {"x": 1},
  "projects": {"/other": {"hasTrustDialogAccepted": false, "allowedTools": ["a"]},
               "`+real+`": {"lastCost": 1.5}}}`), 0o600)
	os.Chmod(cfg, 0o640)
	if err := tr.TrustDir(home, link); err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	d := json.NewDecoder(mustOpen(t, cfg))
	d.UseNumber()
	if err := d.Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got["numStartups"].(json.Number).String() != "12345678901234567890" || got["oauthAccount"] == nil {
		t.Errorf("top-level keys changed: %v", got)
	}
	projects := got["projects"].(map[string]any)
	other := projects["/other"].(map[string]any)
	if other["hasTrustDialogAccepted"] != false || other["allowedTools"] == nil {
		t.Errorf("other project changed: %v", other)
	}
	for _, p := range []string{link, real} {
		e, _ := projects[p].(map[string]any)
		if e["hasTrustDialogAccepted"] != true {
			t.Errorf("%s not trusted: %v", p, projects)
		}
	}
	if e := projects[real].(map[string]any); e["lastCost"] == nil {
		t.Errorf("entry fields lost: %v", e)
	}
	if fi, _ := os.Stat(cfg); fi.Mode().Perm() != 0o640 {
		t.Errorf("mode %v", fi.Mode())
	}

	// Already trusted: the file isn't rewritten.
	before, _ := os.Stat(cfg)
	os.Chtimes(cfg, before.ModTime().Add(-1e9), before.ModTime().Add(-1e9))
	before, _ = os.Stat(cfg)
	if err := tr.TrustDir(home, link); err != nil {
		t.Fatal(err)
	}
	if after, _ := os.Stat(cfg); !after.ModTime().Equal(before.ModTime()) {
		t.Error("rewrote an already trusted config")
	}

	// A broken config is left alone.
	os.WriteFile(cfg, []byte("{broken"), 0o600)
	if err := tr.TrustDir(home, filepath.Join(home, "new")); err == nil {
		t.Error("no error on a broken config")
	}
	if b, _ := os.ReadFile(cfg); string(b) != "{broken" {
		t.Errorf("broken config rewritten: %q", b)
	}

	// CLAUDE_CONFIG_DIR moves the file; no lock is left behind.
	cd := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", cd)
	if err := tr.TrustDir(home, real); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(cd, ".claude.json")); err != nil {
		t.Error(err)
	}
	if _, err := os.Stat(filepath.Join(cd, ".claude.json.lock")); err == nil {
		t.Error("lock left behind")
	}
}

func mustOpen(t *testing.T, p string) *os.File {
	t.Helper()
	f, err := os.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	return f
}
