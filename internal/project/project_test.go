package project

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/theclifmeister/termilator/internal/caller"
	"github.com/theclifmeister/termilator/internal/home"
	"github.com/theclifmeister/termilator/internal/tasks"
)

var (
	human = caller.Caller{Kind: caller.Human}
	coord = caller.Caller{Kind: caller.Coordinator, Project: "demo-app"}
)

// setup points TERMILATOR_HOME at a temp dir and fixes the clock.
func setup(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv(home.Env, dir)
	clock := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	now = func() time.Time { clock = clock.Add(time.Second); return clock }
	t.Cleanup(func() { now = time.Now })
	return dir
}

func TestSlugify(t *testing.T) {
	for in, want := range map[string]string{
		"Demo App":         "demo-app",
		"  Foo -- Bar!!  ": "foo-bar",
		"Ünïcode 2":        "n-code-2",
		"***":              "",
	} {
		if got := Slugify(in); got != want {
			t.Errorf("Slugify(%q) = %q, want %q", in, got, want)
		}
	}
	if ValidSlug("../x") || ValidSlug("") || !ValidSlug("a-1") {
		t.Fatal("ValidSlug")
	}
}

func TestNewLayout(t *testing.T) {
	root := setup(t)
	repo := t.TempDir()
	p, err := New(Options{Name: "Demo App", Goal: "Ship it", Repos: []string{repo}})
	if err != nil {
		t.Fatal(err)
	}
	if p.Dir != filepath.Join(root, "projects", "demo-app") {
		t.Fatalf("dir %s", p.Dir)
	}
	for _, f := range []string{"PROJECT.md", "AGENTS.md", "CONTEXT.md", "MEMORY.md", "TASKS.md", "JOURNAL.md", "memory", "tasks", "inbox/done", "threads", "uploads"} {
		if _, err := os.Stat(p.Path(f)); err != nil {
			t.Errorf("missing %s", f)
		}
	}
	if target, err := os.Readlink(p.Path("CLAUDE.md")); err != nil || target != "AGENTS.md" {
		t.Fatalf("CLAUDE.md link: %q %v", target, err)
	}
	role, _ := os.ReadFile(p.Path("AGENTS.md"))
	if !strings.Contains(string(role), "tm skill coordinator") || !strings.Contains(string(role), "coordinator of the termilator project \"Demo App\"") {
		t.Fatalf("role file:\n%s", role)
	}

	q, err := Open("demo-app")
	if err != nil {
		t.Fatal(err)
	}
	if q.Meta.Name != "Demo App" || q.Meta.Goal != "Ship it" || len(q.Meta.Repos) != 1 || q.Meta.Repos[0] != repo {
		t.Fatalf("meta %+v", q.Meta)
	}
	if !strings.HasPrefix(q.Instructions, "# Instructions") {
		t.Fatalf("instructions %q", q.Instructions)
	}
	if _, err := New(Options{Name: "demo app"}); err == nil || !strings.Contains(err.Error(), "project-exists") {
		t.Fatalf("second new: %v", err)
	}
	if _, err := New(Options{Name: "x", Repos: []string{filepath.Join(repo, "nope")}}); err == nil || !strings.Contains(err.Error(), "invalid-repo") {
		t.Fatalf("bad repo: %v", err)
	}
	if _, err := Open("nope"); err == nil || !strings.Contains(err.Error(), "unknown-project") {
		t.Fatalf("open unknown: %v", err)
	}
}

func TestList(t *testing.T) {
	setup(t)
	if l, err := List(); err != nil || len(l) != 0 {
		t.Fatalf("empty list: %v %v", l, err)
	}
	New(Options{Name: "beta"})
	p, _ := New(Options{Name: "alpha"})
	p.Tasks().Add(human, []tasks.NewTask{{Title: "a", Status: "review"}, {Title: "b"}})
	l, err := List()
	if err != nil || len(l) != 2 || l[0].Slug != "alpha" {
		t.Fatalf("list %+v %v", l, err)
	}
	if l[0].Counts["needs_you"] != 1 || l[0].Counts["on_deck"] != 1 {
		t.Fatalf("counts %+v", l[0].Counts)
	}
}

func TestResolve(t *testing.T) {
	root := setup(t)
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	none := env(nil)
	sub := filepath.Join(root, "projects", "demo", "threads")
	wt := filepath.Join(root, "worktrees", "demo", "t-0001-fix")
	os.MkdirAll(sub, 0o755)
	os.MkdirAll(wt, 0o755)
	cases := []struct {
		flag string
		get  func(string) string
		cwd  string
		want string
	}{
		{"x", env(map[string]string{caller.EnvProject: "y"}), sub, "x"},
		{"", env(map[string]string{caller.EnvProject: "y"}), sub, "y"},
		{"", none, sub, "demo"},
		{"", none, wt, "demo"},
		{"", none, root, ""},
		{"", none, t.TempDir(), ""},
	}
	for _, c := range cases {
		got, err := Resolve(c.flag, c.get, c.cwd)
		if err != nil || got != c.want {
			t.Errorf("Resolve(%q, %q) = %q, %v; want %q", c.flag, c.cwd, got, err, c.want)
		}
	}
}

func TestDoneApproved(t *testing.T) {
	setup(t)
	p, _ := New(Options{Name: "demo app"})
	s := p.Tasks()
	s.Add(coord, []tasks.NewTask{{Title: "Ship"}, {Title: "Docs"}})
	_, err := s.SetStatus(coord, 1, tasks.Done, "")
	if err == nil || !strings.Contains(err.Error(), "human-only") {
		t.Fatalf("err %v", err)
	}
	if items, _ := p.Inbox(); len(items) != 0 {
		t.Fatalf("a refused done raised items: %+v", items)
	}
	if _, err := s.SetDoneApproved(coord, 1, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetStatus(human, 2, tasks.Done, ""); err != nil {
		t.Fatal(err)
	}
	lines, _, _ := p.JournalTail(20)
	j := strings.Join(lines, "\n")
	for _, want := range []string{"human project.new demo-app", "coordinator task.add T1 Ship", "coordinator task.status T1 done (approved by the user)", "human task.status T2 done"} {
		if !strings.Contains(j, want) {
			t.Errorf("journal lacks %q:\n%s", want, j)
		}
	}
}

func TestContextDeterministicAndCapped(t *testing.T) {
	setup(t)
	p, _ := New(Options{Name: "demo app", Goal: "Ship v1"})
	s := p.Tasks()
	var items []tasks.NewTask
	for i := 0; i < 15; i++ {
		items = append(items, tasks.NewTask{Title: "done " + string(rune('a'+i)), Status: "done"})
	}
	items = append(items, tasks.NewTask{Title: "Fix login", Status: "started", Steps: []string{"a", "b"}})
	s.Add(human, items)
	var ctx strings.Builder
	for i := 0; i < 200; i++ {
		ctx.WriteString("line\n")
	}
	os.WriteFile(p.Path("CONTEXT.md"), []byte(ctx.String()), 0o644)
	os.MkdirAll(p.Path("threads", "t-0001"), 0o755)
	os.WriteFile(p.Path("threads", "t-0001", "thread.toml"), []byte("task = \"T16\"\ntitle = \"Fix login\"\n"), 0o644)
	os.WriteFile(p.Path("threads", "t-0001", "REPORT.md"), []byte("## Report\nok\n\n## Next\nMerge the PR\n"), 0o644)
	p.AddItem("thread-done", "t-0001", "t-0001 reported", false)

	sec1, err := p.Context()
	if err != nil {
		t.Fatal(err)
	}
	sec2, _ := p.Context()
	out := RenderContext(sec1)
	if out != RenderContext(sec2) {
		t.Fatal("context not deterministic")
	}
	for _, want := range []string{
		"Goal: Ship v1",
		"[… 80 more lines in CONTEXT.md]",
		"In motion (1)\n  T16  started  Fix login  0/2",
		"Done (15)",
		"[… 5 done tasks not shown (tm task list)]",
		"t-0001  T16  Fix login  report: yes\n    next: Merge the PR",
		"thread-done: t-0001 reported",
		"human task.add T16 Fix login",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("context lacks %q", want)
		}
	}
	if strings.Contains(out, "done a ") {
		t.Error("oldest done task not capped")
	}
	if t.Failed() {
		t.Log(out)
	}
}

// FuzzParseItem: an inbox file, whatever it holds, parses or fails
// without panicking, and a parsed item's fields are single lines.
func FuzzParseItem(f *testing.F) {
	f.Add([]byte("+++\nid = \"x\"\nkind = \"report\"\nsubject = \"t-0001\"\nsummary = \"t-0001 handed in report 1\"\nneeds_user = false\n+++\n"))
	f.Add([]byte("+++\nsummary = \"a\\nb\\rc\"\n+++\nbody"))
	f.Add([]byte("no front matter"))
	f.Fuzz(func(t *testing.T, data []byte) {
		it, err := ParseItem("id", data)
		if err != nil {
			return
		}
		for _, s := range []string{it.Kind, it.Subject, it.Summary} {
			if strings.ContainsAny(s, "\n\r") {
				t.Fatalf("multi-line field %q", s)
			}
		}
	})
}

func TestPruneDone(t *testing.T) {
	t.Setenv("TERMILATOR_HOME", t.TempDir())
	p, err := New(Options{Name: "Prune"})
	if err != nil {
		t.Fatal(err)
	}
	old, _ := p.AddItem("report", "t-0001", "old", false)
	fresh, _ := p.AddItem("report", "t-0002", "fresh", false)
	p.DoneItem(old.ID)
	p.DoneItem(fresh.ID)
	past := time.Now().Add(-31 * 24 * time.Hour)
	os.Chtimes(p.Path("inbox", "done", old.ID+".md"), past, past)
	if n, err := p.PruneDone(30 * 24 * time.Hour); err != nil || n != 1 {
		t.Fatalf("pruned %d, %v", n, err)
	}
	if _, err := os.Stat(p.Path("inbox", "done", fresh.ID+".md")); err != nil {
		t.Fatal("fresh item pruned")
	}
}

func TestAskDelegate(t *testing.T) {
	setup(t)
	p, err := New(Options{Name: "Delegate"})
	if err != nil {
		t.Fatal(err)
	}
	if asked, err := p.AskDelegate(human, "T3"); !asked || err != nil {
		t.Fatalf("first ask: %v, %v", asked, err)
	}
	// A second ask while the first is unhandled adds nothing.
	if asked, err := p.AskDelegate(human, "T3"); asked || err != nil {
		t.Fatalf("second ask: %v, %v", asked, err)
	}
	items, _ := p.Inbox()
	if len(items) != 1 || items[0].Kind != KindDelegate || items[0].Subject != "T3" || items[0].Summary != "the user asks to delegate T3" {
		t.Fatalf("items %+v", items)
	}
	if !DelegateAsked(items, "T3") || DelegateAsked(items, "T4") {
		t.Fatal("DelegateAsked")
	}
	lines, _, _ := p.JournalTail(5)
	if j := strings.Join(lines, "\n"); !strings.Contains(j, "human task.delegate.ask T3") {
		t.Fatalf("journal %q", j)
	}
	// Once handled, the user can ask again.
	p.DoneItem(items[0].ID)
	if asked, err := p.AskDelegate(human, "T3"); !asked || err != nil {
		t.Fatalf("ask after done: %v, %v", asked, err)
	}
}
