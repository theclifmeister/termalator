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
complete_tasks = "merged"

[projects.other]
coordinator_approves = false
auto_resolve = false
pr_followup = false
coordinator_remote_control = true
fast_forward_checkout = false
`)
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if s, _ := c.Safety("demo"); s != (Safety{StartThreads: "auto", Yolo: true, CoordinatorApproves: true, ParallelThreads: 10, AutoClose: "merged", AutoCloseDays: 7, PRFollowup: true, CompleteTasks: "merged", FastForwardCheckout: true}) {
		t.Fatalf("demo %+v", s)
	}
	if s, _ := c.Safety("other"); s != (Safety{StartThreads: "propose", ParallelThreads: 10, AutoClose: "off", AutoCloseDays: 7, CompleteTasks: "user", CoordinatorRemoteControl: true}) {
		t.Fatalf("other %+v", s)
	}
}

// TestCompleteReleasedRemoved: the removed complete_tasks "released" is
// read as "user", so nothing completes silently, and Removed names the
// project for tm doctor.
func TestCompleteReleasedRemoved(t *testing.T) {
	write(t, "[projects.b]\ncomplete_tasks = \"released\"\n\n[projects.a]\ncomplete_tasks = \"released\"\n\n[projects.c]\ncomplete_tasks = \"merged\"\n")
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if s, _ := c.Safety("a"); s.CompleteTasks != CompleteUser {
		t.Fatalf("a %+v", s)
	}
	if got := strings.Join(c.Removed(), " "); got != "a b" {
		t.Fatalf("Removed = %q", got)
	}
	if err := SetProject("a", "complete_tasks", CompleteRemoved); err == nil {
		t.Fatal("wrote the removed value")
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
		"[projects.demo]\ncomplete_tasks = \"later\"\n":    "complete_tasks must be",
	} {
		write(t, body)
		if _, err := Load(); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: err %v, want %q", body, err, want)
		}
	}
}

// TestKeysAndUI: [keys] and [ui] are read here too, the older [keys]
// detach as the prefix, and their unknown keys are listed, not errors.
func TestKeysAndUI(t *testing.T) {
	write(t, "[keys]\ndetach = \"ctrl+a\"\nprefx = \"ctrl+q\"\n\n[ui]\nicon = \"nerd\"\nicons = \"ascii\"\n")
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.Prefix != "ctrl+a" || c.Icons != "ascii" || strings.Join(c.Unknown, ",") != "keys.prefx,ui.icon" {
		t.Fatalf("prefix %q icons %q unknown %v", c.Prefix, c.Icons, c.Unknown)
	}
	write(t, "[keys]\nprefix = \"ctrl+o\"\ndetach = \"ctrl+a\"\n")
	if c, _ = Load(); c.Prefix != "ctrl+o" {
		t.Fatalf("prefix %q, want prefix over detach", c.Prefix)
	}
	// A bad project setting fails Load, but the TUI still gets its keys.
	write(t, "[keys]\nprefix = \"ctrl+o\"\n[projects.demo]\nyoloo = true\n")
	if c, err = Load(); err == nil || c == nil || c.Prefix != "ctrl+o" {
		t.Fatalf("bad project: %v, %+v", err, c)
	}
}
