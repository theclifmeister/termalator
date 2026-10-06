package project

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/theclifmeister/terminatr/internal/caller"
	"github.com/theclifmeister/terminatr/internal/home"
	"github.com/theclifmeister/terminatr/internal/tasks"
)

var (
	human = caller.Caller{Kind: caller.Human}
	coord = caller.Caller{Kind: caller.Coordinator, Project: "demo-app"}
)

// setup points TERMINATR_HOME at a temp dir and fixes the clock.
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
	if !strings.Contains(string(role), "tm skill coordinator") || !strings.Contains(string(role), "coordinator of the terminatr project \"Demo App\"") {
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
	repo := t.TempDir()
	p, _ := New(Options{Name: "demo app", Goal: "Ship v1", Repos: []string{repo}})
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
	os.MkdirAll(p.Path("threads", "t-0002"), 0o755)
	os.WriteFile(p.Path("threads", "t-0002", "thread.toml"), []byte("title = \"Docs\"\n"), 0o644)
	os.WriteFile(p.Path("threads", "t-0002", "REPORT.md"), []byte("PR: https://github.com/o/r/pull/9\n## Report\nok\n\n## Next\nReview it\n"), 0o644)
	os.MkdirAll(p.Path("threads", "t-0003"), 0o755)
	os.WriteFile(p.Path("threads", "t-0003", "thread.toml"), []byte("title = \"Old\"\nstate = \"resolved\"\n"), 0o644)
	os.WriteFile(p.Path("threads", "t-0003", "REPORT.md"), []byte("## Report\nok\n\n## Next\nStale next line\n"), 0o644)
	os.MkdirAll(p.Path("threads", "archive"), 0o755)
	os.WriteFile(p.Path("threads", "archive", "t-0000.tar.gz"), nil, 0o644)
	prs := map[string]string{"t-0001": "#8 open, checks pass"}
	p.AddItem("report", "t-0001", "t-0001 reported", false)

	seen := Ticked{PRs: prs, Checkouts: map[string]string{p.Meta.Repos[0]: "local main is 3 behind origin (uncommitted changes)"},
		Queues: []HeldQueue{{Session: "s-28", Role: "coordinator", Queued: 1, Why: "prompt box not empty", Since: time.Date(2026, 10, 5, 19, 45, 49, 0, time.UTC)}}}
	sec1, err := p.Context(seen)
	if err != nil {
		t.Fatal(err)
	}
	sec2, _ := p.Context(seen)
	out := RenderContext(sec1)
	if out != RenderContext(sec2) {
		t.Fatal("context not deterministic")
	}
	for _, want := range []string{
		"Goal: Ship v1",
		"Repo: " + p.Meta.Repos[0] + " · local main is 3 behind origin (uncommitted changes)",
		"[… 80 more lines in CONTEXT.md]",
		"In motion (1)\n  T16  started  Fix login  0/2",
		"Done (15)",
		"Prompt queue: s-28 (coordinator) has 1 prompt(s) held since 2026-10-05 19:45 UTC (prompt box not empty)",
		"[… 5 done tasks not shown (tm task list)]",
		"T16 (t-0001)  Fix login  report: yes  PR: #8 open, checks pass\n    next: Merge the PR",
		"t-0002  Docs  report: yes  PR: https://github.com/o/r/pull/9\n    next: Review it",
		"report: t-0001 reported",
		"human task.add T16 Fix login",
		"2 resolved threads not shown (tm thread list --all)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("context lacks %q", want)
		}
	}
	if strings.Contains(out, "Stale next line") || strings.Contains(out, "archive") {
		t.Error("a resolved or archived thread is listed")
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

// TestArchiveInbox: handled items older than the age move into their
// creation month's tarball, which keeps what it held; fresh ones stay
// loose, and unhandled ones aren't touched.
func TestArchiveInbox(t *testing.T) {
	setup(t)
	p, err := New(Options{Name: "Prune"})
	if err != nil {
		t.Fatal(err)
	}
	old, _ := p.AddItem("report", "t-0001", "old", false)
	older, _ := p.AddItem("report", "t-0003", "older", false)
	fresh, _ := p.AddItem("report", "t-0002", "fresh", false)
	open, _ := p.AddItem("report", "t-0004", "open", false)
	for _, it := range []*Item{old, older, fresh} {
		p.DoneItem(it.ID)
	}
	at := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	past := at.Add(-31 * 24 * time.Hour)
	os.Chtimes(p.Path("inbox", "done", old.ID+".md"), past, past)
	if n, err := p.ArchiveInbox(at, 30*24*time.Hour); err != nil || n != 1 {
		t.Fatalf("bundled %d, %v", n, err)
	}
	os.Chtimes(p.Path("inbox", "done", older.ID+".md"), past, past)
	if n, err := p.ArchiveInbox(at, 30*24*time.Hour); err != nil || n != 1 {
		t.Fatalf("second run bundled %d, %v", n, err)
	}
	for _, f := range []string{"inbox/done/" + fresh.ID + ".md", "inbox/" + open.ID + ".md"} {
		if _, err := os.Stat(p.Path(f)); err != nil {
			t.Errorf("%s moved: %v", f, err)
		}
	}
	// The items' ids say they were created 2026-10-04: that month's file.
	names := map[string]string{}
	f, err := os.Open(p.Path("inbox", "done", "2026-10.tar.gz"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	zr, _ := gzip.NewReader(f)
	tr := tar.NewReader(zr)
	for {
		h, err := tr.Next()
		if err != nil {
			break
		}
		b, _ := io.ReadAll(tr)
		names[h.Name] = string(b)
	}
	if len(names) != 2 || !strings.Contains(names[old.ID+".md"], `summary = "old"`) || !strings.Contains(names[older.ID+".md"], `summary = "older"`) {
		t.Fatalf("tarball: %v", names)
	}
	if items, _ := p.Inbox(); len(items) != 1 || items[0].ID != open.ID {
		t.Fatalf("inbox: %+v", items)
	}
}

// TestArchiveJournal: journal lines older than the age go to their
// month's gzip file, appended; the heading and newer lines stay.
func TestArchiveJournal(t *testing.T) {
	setup(t)
	p, _ := New(Options{Name: "demo"})
	os.WriteFile(p.Path("JOURNAL.md"), []byte("# Journal\n\n"+
		"2026-08-30T10:00:00Z human a\n2026-09-02T10:00:00Z human b\n2026-09-20T10:00:00Z human c\n2026-10-03T10:00:00Z human d\n"), 0o644)
	at := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	if n, err := p.ArchiveJournal(at, 20*24*time.Hour); err != nil || n != 2 {
		t.Fatalf("moved %d, %v", n, err)
	}
	if n, err := p.ArchiveJournal(at, 10*24*time.Hour); err != nil || n != 1 {
		t.Fatalf("second run moved %d, %v", n, err)
	}
	if n, _ := p.ArchiveJournal(at, 10*24*time.Hour); n != 0 {
		t.Fatalf("third run moved %d", n)
	}
	got, _ := os.ReadFile(p.Path("JOURNAL.md"))
	if string(got) != "# Journal\n\n2026-10-03T10:00:00Z human d\n" {
		t.Fatalf("JOURNAL.md:\n%s", got)
	}
	read := func(name string) string {
		f, err := os.Open(p.Path("journal", name))
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		zr, err := gzip.NewReader(f)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(zr)
		return string(b)
	}
	if a, s := read("2026-08.md.gz"), read("2026-09.md.gz"); a != "2026-08-30T10:00:00Z human a\n" ||
		s != "2026-09-02T10:00:00Z human b\n2026-09-20T10:00:00Z human c\n" {
		t.Fatalf("archives: %q %q", a, s)
	}
	if lines, total, _ := p.JournalTail(5); total != 1 || lines[0] != "2026-10-03T10:00:00Z human d" {
		t.Fatalf("tail: %v %d", lines, total)
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
	if TaskAsked(items, "T3") != KindDelegate || TaskAsked(items, "T4") != "" {
		t.Fatal("TaskAsked")
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

func TestAskAcceptSendBack(t *testing.T) {
	setup(t)
	p, err := New(Options{Name: "Review"})
	if err != nil {
		t.Fatal(err)
	}
	if asked, err := p.AskAccept(human, "T5"); !asked || err != nil {
		t.Fatalf("accept: %v, %v", asked, err)
	}
	// One ask per task at a time: neither a second accept nor a
	// send-back is added while the accept waits.
	if asked, err := p.AskAccept(human, "T5"); asked || err != nil {
		t.Fatalf("second accept: %v, %v", asked, err)
	}
	if asked, err := p.AskSendBack(human, "T5", "too slow"); asked || err != nil {
		t.Fatalf("send-back over accept: %v, %v", asked, err)
	}
	if _, err := p.AskSendBack(human, "T6", " \t "); err == nil {
		t.Fatal("empty note accepted")
	}
	if _, err := p.AskSendBack(human, "T6", strings.Repeat("x", MaxSendBackNote+1)); err == nil {
		t.Fatal("long note accepted")
	}
	if asked, err := p.AskSendBack(human, "T6", "the bell\nis   cut off"); !asked || err != nil {
		t.Fatalf("send-back: %v, %v", asked, err)
	}
	items, _ := p.Inbox()
	if len(items) != 2 || items[0].Kind != KindAccept || items[0].Summary != "the user accepts T5" ||
		items[1].Kind != KindSendBack || items[1].Subject != "T6" || items[1].Summary != "the user sends T6 back: the bell is cut off" {
		t.Fatalf("items %+v", items)
	}
	if TaskAsked(items, "T5") != KindAccept || TaskAsked(items, "T6") != KindSendBack {
		t.Fatal("TaskAsked")
	}
	lines, _, _ := p.JournalTail(5)
	j := strings.Join(lines, "\n")
	for _, w := range []string{"human task.accept.ask T5", "human task.sendback.ask T6 the bell is cut off"} {
		if !strings.Contains(j, w) {
			t.Errorf("journal lacks %q: %q", w, j)
		}
	}
}

// TestUpkeep: context files over their budget are named in tm context's
// Upkeep section (and only then); done tasks leave the board after
// the given age, journaled as the caller's.
func TestUpkeep(t *testing.T) {
	setup(t)
	p, _ := New(Options{Name: "demo app"})
	render := func() string {
		secs, err := p.Context(Ticked{})
		if err != nil {
			t.Fatal(err)
		}
		return RenderContext(secs)
	}
	if over, err := p.Oversized(); err != nil || len(over) != 0 {
		t.Fatalf("fresh project: %v %v", over, err)
	}
	if strings.Contains(render(), "## Upkeep") {
		t.Fatal("an Upkeep section without anything over budget")
	}
	os.WriteFile(p.Path("CONTEXT.md"), []byte(strings.Repeat("x", BudgetContext+512)), 0o644)
	os.MkdirAll(p.Path("memory", "old"), 0o755)
	os.WriteFile(p.Path("memory", "old", "decisions.md"), []byte(strings.Repeat("y", BudgetMemory+1024)), 0o644)
	os.WriteFile(p.Path("memory", "small.md"), []byte("ok\n"), 0o644)
	ctx := render()
	for _, w := range []string{"## Upkeep", "CONTEXT.md is 6.5 KB, over its 6 KB budget: consolidate it",
		"memory/old/decisions.md is 7 KB, over its 6 KB budget"} {
		if !strings.Contains(ctx, w) {
			t.Errorf("context lacks %q:\n%s", w, ctx)
		}
	}
	if strings.Contains(ctx, "small.md") {
		t.Error("a file within budget named")
	}

	s := p.Tasks()
	s.Add(human, []tasks.NewTask{{Title: "Old"}, {Title: "Open"}})
	s.SetStatus(human, 1, tasks.Done, "")
	day := now()
	ticker := caller.Caller{Kind: caller.Ticker}
	const ArchiveDoneAfter = 30 * 24 * time.Hour
	if ids, err := p.ArchiveOldDone(ticker, day.Add(ArchiveDoneAfter-24*time.Hour), ArchiveDoneAfter); err != nil || len(ids) != 0 {
		t.Fatalf("archived too early: %v %v", ids, err)
	}
	if ids, err := p.ArchiveOldDone(ticker, day.Add(ArchiveDoneAfter+24*time.Hour), ArchiveDoneAfter); err != nil || len(ids) != 1 || ids[0] != 1 {
		t.Fatalf("archive: %v %v", ids, err)
	}
	if b, _ := s.Load(); b.Find(1) != nil || b.Find(2) == nil {
		t.Fatalf("board after archiving: %+v", b.Tasks)
	}
	lines, _, _ := p.JournalTail(5)
	if j := strings.Join(lines, "\n"); !strings.Contains(j, "ticker task.archive T1") {
		t.Errorf("journal:\n%s", j)
	}
}

// TestReadMemory: a project's memory is its CONTEXT.md, its MEMORY.md
// and its memory notes' titles (the first heading, else the file name),
// sorted; other files in memory/ are left out.
func TestReadMemory(t *testing.T) {
	setup(t)
	p, err := New(Options{Name: "Demo App"})
	if err != nil {
		t.Fatal(err)
	}
	for name, text := range map[string]string{
		"CONTEXT.md":          "# Context\n\nThe plan.\n",
		"MEMORY.md":           "# Memory\n\n- [Decisions](memory/decisions.md): host\n",
		"memory/decisions.md": "intro\n\n## Design decisions\n\n- one\n",
		"memory/lessons.md":   "no heading\n",
		"memory/notes.txt":    "# Not a note\n",
	} {
		if err := os.WriteFile(p.Path(name), []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	m, err := p.ReadMemory()
	if err != nil {
		t.Fatal(err)
	}
	if m.Context != "# Context\n\nThe plan.\n" || !strings.Contains(m.Index, "[Decisions]") {
		t.Fatalf("memory %+v", m)
	}
	if strings.Join(m.Notes, "|") != "Design decisions|lessons" {
		t.Fatalf("notes %q", m.Notes)
	}
	os.Remove(p.Path("CONTEXT.md"))
	if m, err := p.ReadMemory(); err != nil || m.Context != "" {
		t.Fatalf("without CONTEXT.md: %+v, %v", m, err)
	}
}

func TestInboxRows(t *testing.T) {
	items := []Item{
		{ID: "a", Kind: "report", Subject: "t-0001", Summary: "T1 Fix the login (t-0001) handed in report 1"},
		{ID: "b", Kind: "idle", Subject: "t-0001", Summary: "t-0001 is idle"},
		{ID: "c", Kind: "report", Subject: "t-0002", Summary: "t-0002 (Docs) handed in report 1"},
		{ID: "d", Kind: "report", Subject: "t-0001", Summary: "T1 Fix the login (t-0001) handed in report 2"},
	}
	rows := Rows(items)
	var got []string
	for _, r := range rows {
		got = append(got, fmt.Sprintf("%s %d %q|%q|%q", r.ID, r.Count, r.Task, r.What, r.Title))
	}
	want := []string{
		`b 1 ""|"t-0001 is idle"|""`,
		`c 1 ""|"t-0002 (Docs) handed in report 1"|""`,
		`d 2 "T1"|"handed in report 2"|"Fix the login"`,
	}
	if !slices.Equal(got, want) {
		t.Errorf("rows:\n%q\nwant\n%q", got, want)
	}
}

func TestDoneItems(t *testing.T) {
	t.Setenv("TERMINATR_HOME", t.TempDir())
	p, err := New(Options{Name: "Demo"})
	if err != nil {
		t.Fatal(err)
	}
	p.AddItem("report", "t-0001", "r1", false)
	p.AddItem("report", "t-0002", "other", false)
	p.AddItem("idle", "t-0001", "idle", false)
	if err := p.DoneItems("report", "t-0001"); err != nil {
		t.Fatal(err)
	}
	items, _ := p.Inbox()
	var got []string
	for _, it := range items {
		got = append(got, it.Summary)
	}
	slices.Sort(got)
	if !slices.Equal(got, []string{"idle", "other"}) {
		t.Fatalf("inbox: %+v", items)
	}
}
