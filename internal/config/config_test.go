package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/theclifmeister/termilator/internal/home"
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
auto_resolve = false
pr_followup = false
coordinator_remote_control = true
`)
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if s, _ := c.Safety("demo"); s != (Safety{StartThreads: "auto", Yolo: true, CoordinatorApproves: true, ParallelThreads: 10, AutoClose: "merged", AutoCloseDays: 7, PRFollowup: true}) {
		t.Fatalf("demo %+v", s)
	}
	if s, _ := c.Safety("other"); s != (Safety{StartThreads: "propose", ParallelThreads: 10, AutoClose: "off", AutoCloseDays: 7, CoordinatorRemoteControl: true}) {
		t.Fatalf("other %+v", s)
	}
}

// TestAutoCloseAndCap: auto_close wins over the older auto_resolve, and
// the number settings take their own values.
func TestAutoCloseAndCap(t *testing.T) {
	write(t, `
[projects.a]
auto_resolve = false
auto_close = "days"
auto_close_days = 3
parallel_threads = 4

[projects.b]
auto_resolve = true
`)
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if s, _ := c.Safety("a"); s.AutoClose != CloseDays || s.AutoCloseDays != 3 || s.ParallelThreads != 4 {
		t.Fatalf("a %+v", s)
	}
	if s, _ := c.Safety("b"); s.AutoClose != CloseMerged || s.ParallelThreads != 10 {
		t.Fatalf("b %+v", s)
	}
}

func TestBadSettings(t *testing.T) {
	for body, want := range map[string]string{
		"[projects.demo]\nstart_threads = \"sometimes\"\n": "start_threads must be",
		"[projects.demo]\nyoloo = true\n":                  "unknown setting projects.demo.yoloo",
		"[projects.demo]\nyolo = \"yes\"\n":                "yolo",
		"not toml":                                         "config.toml",
		"[projects.demo]\nparallel_threads = 0\n":          "parallel_threads must be 1 to 99",
		"[projects.demo]\nauto_close = \"never\"\n":        "auto_close must be",
		"[projects.demo]\nauto_close_days = 0\n":           "auto_close_days must be 1 to 365",
	} {
		write(t, body)
		if _, err := Load(); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: err %v, want %q", body, err, want)
		}
	}
}
