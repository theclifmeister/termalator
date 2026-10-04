package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/theclifmeister/termalator/internal/home"
)

func write(t *testing.T, body string) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv(home.Env, dir)
	if body != "" {
		os.WriteFile(filepath.Join(dir, "config.toml"), []byte(body), 0o600)
	}
}

func TestDefaultsWithoutFile(t *testing.T) {
	write(t, "")
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if s, _ := c.Safety("demo"); s != Defaults {
		t.Fatalf("got %+v", s)
	}
}

func TestProjectSettings(t *testing.T) {
	write(t, `
detach_key = "ctrl-\\"   # another package's setting: ignored here

[projects.demo]
start_threads = "auto"
yolo = true

[projects.other]
coordinator_approves = false
`)
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if s, _ := c.Safety("demo"); s != (Safety{StartThreads: "auto", Yolo: true, CoordinatorApproves: true}) {
		t.Fatalf("demo %+v", s)
	}
	if s, _ := c.Safety("other"); s != (Safety{StartThreads: "propose", CoordinatorApproves: false}) {
		t.Fatalf("other %+v", s)
	}
}

func TestBadSettings(t *testing.T) {
	for body, want := range map[string]string{
		"[projects.demo]\nstart_threads = \"sometimes\"\n": "start_threads must be",
		"[projects.demo]\nyoloo = true\n":                  "unknown setting projects.demo.yoloo",
		"[projects.demo]\nyolo = \"yes\"\n":                "yolo",
		"not toml":                                         "config.toml",
	} {
		write(t, body)
		if _, err := Load(); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: err %v, want %q", body, err, want)
		}
	}
}
