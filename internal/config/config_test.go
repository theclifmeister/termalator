package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/theclifmeister/terminatr/internal/home"
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
	if s, _ := c.Safety("demo"); !reflect.DeepEqual(s, Defaults) {
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
auto_clear = true
fast_forward_checkout = false
`)
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if s, _ := c.Safety("demo"); !reflect.DeepEqual(s, Safety{StartThreads: "auto", Yolo: true, CoordinatorApproves: true, ParallelThreads: 10, AutoClose: "merged", AutoCloseDays: 7, PRFollowup: true, CompleteTasks: "merged", FastForwardCheckout: true, Merge: "coordinator", Guard: true, ArchiveTasksDays: 30, ArchiveThreadsDays: 30, ArchiveInboxDays: 30, ArchiveJournalDays: 30}) {
		t.Fatalf("demo %+v", s)
	}
	if s, _ := c.Safety("other"); !reflect.DeepEqual(s, Safety{StartThreads: "propose", ParallelThreads: 10, AutoClose: "off", AutoCloseDays: 7, CompleteTasks: "user", CoordinatorRemoteControl: true, AutoClear: true, Merge: "coordinator", Guard: true, ArchiveTasksDays: 30, ArchiveThreadsDays: 30, ArchiveInboxDays: 30, ArchiveJournalDays: 30}) {
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
		"[projects.demo]\narchive_inbox_days = 0\n":        "archive_inbox_days must be 1 to 365",
		"[defaults]\narchive_journal_days = 400\n":         "defaults.archive_journal_days must be 1 to 365",
		"[projects.demo]\ncomplete_tasks = \"later\"\n":    "complete_tasks must be",
		"[defaults]\nyoloo = true\n":                       "unknown setting defaults.yoloo",
		"[defaults]\narchived = true\n":                    "defaults can't set paused or archived",
		"[defaults]\npaused = false\n":                     "defaults can't set paused or archived",
		"[defaults]\nparallel_threads = 100\n":             "defaults.parallel_threads must be 1 to 99",
		"[defaults]\nauto_close = \"never\"\n":             "defaults.auto_close must be",
		"[projects.demo]\nmerge = \"anyone\"\n":            "merge must be",
		"[defaults]\nguard_off = [\"push\"]\n":             "unknown rule \"push\"",
		"[projects.demo]\nguard = \"no\"\n":                "guard",
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

// TestAllProjects: each setting is the project's own value, else the
// all-projects one ([defaults]), else the built-in default; a project
// without a table follows all projects in everything.
func TestAllProjects(t *testing.T) {
	write(t, `
[defaults]
start_threads = "auto"
parallel_threads = 4
auto_close = "off"
complete_tasks = "merged"
archive_threads_days = 14

[projects.demo]
parallel_threads = 2
complete_tasks = "user"
archive_journal_days = 90

[projects.old]
auto_resolve = true
`)
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	all := Safety{StartThreads: "auto", CoordinatorApproves: true, ParallelThreads: 4, AutoClose: "off", AutoCloseDays: 7, PRFollowup: true, CompleteTasks: "merged", FastForwardCheckout: true, Merge: "coordinator", Guard: true,
		ArchiveTasksDays: 30, ArchiveThreadsDays: 14, ArchiveInboxDays: 30, ArchiveJournalDays: 30}
	if s, _ := c.AllProjects(); !reflect.DeepEqual(s, all) {
		t.Fatalf("all projects %+v", s)
	}
	if s, _ := c.Safety("new"); !reflect.DeepEqual(s, all) {
		t.Fatalf("a new project %+v, want all projects", s)
	}
	want := all
	want.ParallelThreads, want.CompleteTasks, want.ArchiveJournalDays = 2, "user", 90
	if s, _ := c.Safety("demo"); !reflect.DeepEqual(s, want) {
		t.Fatalf("demo %+v", s)
	}
	// A project's older auto_resolve overrides the defaults' auto_close.
	if s, _ := c.Safety("old"); s.AutoClose != CloseMerged || s.ParallelThreads != 4 {
		t.Fatalf("old %+v", s)
	}
	if got := strings.Join(c.Own("demo"), " "); got != "parallel_threads complete_tasks archive_journal_days" {
		t.Fatalf("Own(demo) = %q", got)
	}
	if got := strings.Join(c.Own("old"), " "); got != "auto_close" {
		t.Fatalf("Own(old) = %q", got)
	}
	if got := c.Own("new"); got != nil {
		t.Fatalf("Own(new) = %q", got)
	}
}

// TestAllProjectsReleasedRemoved: "released" in [defaults] is read as
// "user" and named first by Removed.
func TestAllProjectsReleasedRemoved(t *testing.T) {
	write(t, "[defaults]\ncomplete_tasks = \"released\"\n\n[projects.a]\ncomplete_tasks = \"released\"\n")
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if s, _ := c.Safety("b"); s.CompleteTasks != CompleteUser {
		t.Fatalf("b %+v", s)
	}
	if got := strings.Join(c.Removed(), ","); got != "all projects,a" {
		t.Fatalf("Removed = %q", got)
	}
}

// TestMods: [mods] enabled is off unless set, band and pane on unless
// set false; its unknown keys are listed, not errors.
func TestMods(t *testing.T) {
	write(t, "")
	if c, _ := Load(); c.Mods || !c.ModsBand || !c.ModsPane {
		t.Fatalf("without a file: mods %v, band %v, pane %v", c.Mods, c.ModsBand, c.ModsPane)
	}
	write(t, "[mods]\nenabled = true\nsidebar = true\n")
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if !c.Mods || !c.ModsBand || !c.ModsPane || strings.Join(c.Unknown, ",") != "mods.sidebar" {
		t.Fatalf("mods %v, band %v, pane %v, unknown %v", c.Mods, c.ModsBand, c.ModsPane, c.Unknown)
	}
	write(t, "[mods]\nenabled = true\nband = false\n")
	if c, err := Load(); err != nil || !c.Mods || c.ModsBand || !c.ModsPane || len(c.Unknown) != 0 {
		t.Fatalf("band = false: %v, %+v", err, c)
	}
	write(t, "[mods]\nenabled = true\npane = false\n")
	if c, err := Load(); err != nil || !c.ModsBand || c.ModsPane || len(c.Unknown) != 0 {
		t.Fatalf("pane = false: %v, %+v", err, c)
	}
}

func TestModelsAllowList(t *testing.T) {
	write(t, `
[defaults]
models = ["opus", "sonnet"]

[projects.demo]
models = ["opus"]

[projects.other]
yolo = true
`)
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if s, _ := c.Safety("demo"); !reflect.DeepEqual(s.Models, []string{"opus"}) || !s.AllowsModel("opus") || s.AllowsModel("sonnet") {
		t.Fatalf("demo: %v", s.Models)
	}
	if s, _ := c.Safety("other"); !reflect.DeepEqual(s.Models, []string{"opus", "sonnet"}) || s.AllowsModel("haiku") {
		t.Fatalf("other: %v", s.Models)
	}
	if got := c.Own("demo"); !reflect.DeepEqual(got, []string{"models"}) {
		t.Fatalf("own: %v", got)
	}
	if s, _ := (*Config)(nil).AllProjects(); s.Models != nil || !s.AllowsModel("haiku") {
		t.Fatal("no setting allows every model")
	}
	for _, bad := range []string{`models = []`, `models = ["a b"]`, `models = ["opus", "opus"]`, `models = "opus"`} {
		write(t, "[defaults]\n"+bad+"\n")
		if _, err := Load(); err == nil {
			t.Errorf("%s accepted", bad)
		}
	}
}

// TestGuard: merge, guard and guard_off default to the coordinator
// merging and every rule on; a project's own value wins over [defaults].
func TestGuard(t *testing.T) {
	write(t, "[defaults]\nguard_off = [\"credentials\"]\n[projects.a]\nmerge = \"thread\"\nguard = false\n[projects.b]\nguard_off = []\n")
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if s, _ := c.Safety("a"); s.Merge != MergeThread || s.Guard || !reflect.DeepEqual(s.GuardOff, []string{"credentials"}) {
		t.Errorf("a %+v", s)
	}
	if s, _ := c.Safety("b"); s.Merge != MergeCoordinator || !s.Guard || len(s.GuardOff) != 0 {
		t.Errorf("b %+v", s)
	}
	if s, _ := c.Safety("c"); s.Merge != MergeCoordinator || !s.Guard || !reflect.DeepEqual(s.GuardOff, []string{"credentials"}) {
		t.Errorf("c %+v", s)
	}
}

// TestContextHint: [ui] context_hint is 40 unless set; 0 is never, and
// a percent past 100 is an error.
func TestContextHint(t *testing.T) {
	write(t, "")
	if c, _ := Load(); c.ContextHint != DefaultContextHint || DefaultContextHint != 40 {
		t.Fatalf("without a file: %d", c.ContextHint)
	}
	write(t, "[ui]\nicons = \"ascii\"\n")
	if c, err := Load(); err != nil || c.ContextHint != 40 || len(c.Unknown) != 0 {
		t.Fatalf("unset: %v %+v", err, c)
	}
	write(t, "[ui]\ncontext_hint = 55\n")
	if c, err := Load(); err != nil || c.ContextHint != 55 || len(c.Unknown) != 0 {
		t.Fatalf("55: %v %+v", err, c)
	}
	write(t, "[ui]\ncontext_hint = 0\n")
	if c, err := Load(); err != nil || c.ContextHint != 0 {
		t.Fatalf("0 means never: %v %+v", err, c)
	}
	write(t, "[ui]\ncontext_hint = 140\n")
	if _, err := Load(); err == nil {
		t.Fatal("140% loaded")
	}
}
