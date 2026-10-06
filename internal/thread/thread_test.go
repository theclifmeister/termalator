package thread

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/theclifmeister/termilator/internal/agent"
	"github.com/theclifmeister/termilator/internal/project"
	"github.com/theclifmeister/termilator/internal/tasks"
)

func steps(done ...bool) []tasks.Step {
	var out []tasks.Step
	for i, d := range done {
		out = append(out, tasks.Step{N: i + 1, Text: "step " + string(rune('A'+i)), Done: d})
	}
	return out
}

func todos(st ...agent.TodoStatus) []agent.Todo {
	var out []agent.Todo
	for i, s := range st {
		out = append(out, agent.Todo{Text: "todo " + string(rune('a'+i)), Status: s})
	}
	return out
}

const (
	P = agent.TodoPending
	I = agent.TodoInProgress
	C = agent.TodoCompleted
)

// TestDerive is the table of docs/SPEC.md §7.3.
func TestDerive(t *testing.T) {
	now := time.Now()
	cases := []struct {
		name    string
		steps   []tasks.Step
		todos   []agent.Todo
		self    int
		selfAt  time.Time
		done    bool
		pct     int
		src     string
		current string
	}{
		{"nothing", nil, nil, -1, time.Time{}, false, 0, "", ""},
		{"steps only", steps(true, false, false), nil, -1, time.Time{}, false, 30, "steps", "step B"},
		{"todos only", nil, todos(C, I, P, P), -1, time.Time{}, false, 25, "todos", "todo b"},
		{"steps and todos", steps(true, false, false), todos(C, C, I, P), -1, time.Time{}, false, 50, "steps+todos", "todo c"},
		{"all steps done caps at 95", steps(true, true), todos(C), -1, time.Time{}, false, 95, "steps+todos", ""},
		{"self", nil, nil, 42, now, false, 40, "self", ""},
		{"self expired", nil, nil, 42, now.Add(-6 * time.Minute), false, 0, "", ""},
		{"steps beat self", steps(false, false), nil, 80, now, false, 0, "steps", "step A"},
		{"done", steps(true, false), nil, -1, time.Time{}, true, 100, "done", "step B"},
	}
	for _, c := range cases {
		st := &Status{Todos: c.todos, SelfPercent: c.self, SelfAt: c.selfAt, Done: c.done}
		st.Derive(c.steps, now)
		if st.Percent != c.pct || st.PercentSource != c.src || st.Current != c.current {
			t.Errorf("%s: got %d%% %q ▸ %q, want %d%% %q ▸ %q", c.name, st.Percent, st.PercentSource, st.Current, c.pct, c.src, c.current)
		}
	}
}

func TestStatusRoundTrip(t *testing.T) {
	st := &Status{Percent: 45, PercentSource: "steps+todos", Current: "Fix", SelfPercent: -1, Activity: "Testing",
		Updated: time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC), Todos: todos(C, I, P)}
	b, err := st.Render()
	if err != nil {
		t.Fatal(err)
	}
	got, err := ParseStatus(b)
	if err != nil {
		t.Fatal(err)
	}
	if !SameTodos(got.Todos, st.Todos) || got.Percent != 45 || got.Activity != "Testing" || got.SelfPercent != -1 {
		t.Fatalf("round trip:\n%s\n%+v", b, got)
	}
	if !strings.Contains(string(b), "- [~] todo b  (in progress)") {
		t.Fatalf("render:\n%s", b)
	}
}

func TestValidate(t *testing.T) {
	good := "PR: https://github.com/o/r/pull/12\n\n## Report\nDone.\n\n## Next\n- Merge the PR\nRemove the worktree\n\n## Check\nRun tm, press t\n\n## Remember\n- lesson\n"
	r, err := Validate(good)
	if err != nil {
		t.Fatal(err)
	}
	if r.PR != "https://github.com/o/r/pull/12" || len(r.Next) != 2 || r.Next[0] != "Merge the PR" || len(r.Remember) != 1 ||
		len(r.Check) != 1 || r.Check[0] != "Run tm, press t" {
		t.Fatalf("%+v", r)
	}
	for _, c := range []struct{ text, want string }{
		{"", "empty report"},
		{"## Report\nx\n", "missing ## Next"},
		{"## Next\nx\n", "missing ## Report"},
		{"PR: 12\n## Report\n## Next\n", "bad PR line"},
		{"## Report\n## Next\n" + strings.Repeat("x", 101) + "\n", "line 3 over 100 chars"},
		{"## Report\n## Next\n## Next\n", "second ## Next"},
	} {
		if _, err := Validate(c.text); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("Validate(%q) = %v, want %q", c.text, err, c.want)
		}
	}
}

func FuzzValidateReport(f *testing.F) {
	f.Add("PR: https://github.com/o/r/pull/1\n## Report\nx\n## Next\ny\n")
	f.Add("## Report\n## Next\n## Remember\n- z\n")
	f.Add("\r\n## Next\r\n")
	f.Fuzz(func(t *testing.T, s string) {
		r, err := Validate(s)
		if err != nil {
			return
		}
		// A valid report validates again, unchanged, as stored.
		r2, err := Validate(r.Text)
		if err != nil || r2.PR != r.PR || len(r2.Next) != len(r.Next) {
			t.Fatalf("revalidate %q: %v", r.Text, err)
		}
		for _, n := range r.Next {
			if len([]rune(n)) > MaxNextLine {
				t.Fatalf("long next line %q", n)
			}
		}
	})
}

func FuzzStatus(f *testing.F) {
	st := &Status{Percent: 10, SelfPercent: -1, Todos: todos(C, I, P)}
	b, _ := st.Render()
	f.Add(b)
	f.Add([]byte("+++\npercent = 5\n+++\n- [x] a\n- [?] b\n"))
	f.Fuzz(func(t *testing.T, data []byte) {
		st, err := ParseStatus(data)
		if err != nil {
			return
		}
		out, err := st.Render()
		if err != nil {
			return
		}
		st2, err := ParseStatus(out)
		if err != nil {
			t.Fatalf("re-parse: %v\n%s", err, out)
		}
		if !SameTodos(st.Todos, st2.Todos) {
			t.Fatalf("todos changed:\n%+v\n%+v", st.Todos, st2.Todos)
		}
	})
}

// TestFiles: create a thread, write task text and brief, store reports.
func TestFiles(t *testing.T) {
	t.Setenv("TERMILATOR_HOME", t.TempDir())
	p, err := project.New(project.Options{Name: "demo"})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	r, err := Create(p, Record{Title: "Fix it", Agent: "claude", State: Running, Created: now, LastPrompt: now})
	if err != nil || r.ID != "t-0001" {
		t.Fatalf("%+v %v", r, err)
	}
	r2, _ := Create(p, Record{Title: "Other"})
	if r2.ID != "t-0002" {
		t.Fatalf("second id %s", r2.ID)
	}
	if err := WriteTaskText(p, r.ID, TaskText(r, nil)); err != nil {
		t.Fatal(err)
	}
	AppendFollowUp(p, r.ID, "also this", now)
	WriteTaskText(p, r.ID, "# changed\n")
	b, _ := os.ReadFile(Path(p, r.ID, "task.md"))
	if !strings.HasPrefix(string(b), "# changed\n") || !strings.Contains(string(b), "also this") {
		t.Fatalf("task.md:\n%s", b)
	}
	brief, err := WriteBrief(p, r, true)
	if err != nil {
		t.Fatal(err)
	}
	b, _ = os.ReadFile(brief)
	for _, w := range []string{p.Path("TASKS.md"), p.Path("uploads") + "/ (files the user uploaded", "tm skill thread", "previous attempt", "also this"} {
		if !strings.Contains(string(b), w) {
			t.Errorf("brief lacks %q", w)
		}
	}
	report := "## Report\nok\n## Next\nMerge\n"
	for i := 1; i <= 2; i++ {
		if n, err := StoreReport(p, r.ID, report, nil, time.Now()); err != nil || n != i {
			t.Fatalf("report %d: %d %v", i, n, err)
		}
	}
	if _, err := os.Stat(Path(p, r.ID, "reports", "1.md")); err != nil {
		t.Fatal("earlier report not kept")
	}
	if a := Attachments(p, r.ID); len(a) != 0 {
		t.Fatalf("attachments before any: %v", a)
	}
	src := t.TempDir()
	for _, n := range []string{"plan.md", "chart.png"} {
		os.WriteFile(filepath.Join(src, n), []byte(n), 0o644)
	}
	if _, err := StoreReport(p, r.ID, report, []string{filepath.Join(src, "plan.md"), filepath.Join(src, "chart.png")}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if a := Attachments(p, r.ID); strings.Join(a, ",") != "chart.png,plan.md" {
		t.Fatalf("attachments %v", a)
	}
	if _, err := StoreReport(p, r.ID, "## Report\n", nil, time.Now()); err == nil {
		t.Fatal("invalid report stored")
	}
	got, _ := Load(p, r.ID)
	if got.Reports != 3 || got.ReportState() != "new" {
		t.Fatalf("record %+v", got)
	}
	if ctx := ResetContext(p, r.ID); !strings.Contains(ctx, "brief.md") || !strings.Contains(ctx, "Report: new") {
		t.Fatalf("reset context:\n%s", ctx)
	}
}

// TestReportPRs: every report's PR, oldest first, each once; reports
// past the tenth sort by number.
func TestReportPRs(t *testing.T) {
	t.Setenv("TERMILATOR_HOME", t.TempDir())
	p, err := project.New(project.Options{Name: "demo"})
	if err != nil {
		t.Fatal(err)
	}
	r, err := Create(p, Record{Title: "Two PRs", Agent: "claude", State: Running})
	if err != nil {
		t.Fatal(err)
	}
	if prs := ReportPRs(p, r.ID); len(prs) != 0 {
		t.Fatalf("PRs before any report: %q", prs)
	}
	report := "## Report\nok\n## Next\nMerge\n"
	pr := func(n int) string { return fmt.Sprintf("PR: https://github.com/o/r/pull/%d\n%s", n, report) }
	reps := []string{pr(7), report}
	for range 9 {
		reps = append(reps, pr(7))
	}
	reps = append(reps, pr(9), pr(8), report)
	for i, rep := range reps {
		if _, err := StoreReport(p, r.ID, rep, nil, time.Now()); err != nil {
			t.Fatalf("report %d: %v", i, err)
		}
	}
	want := "https://github.com/o/r/pull/7 https://github.com/o/r/pull/9 https://github.com/o/r/pull/8"
	if prs := ReportPRs(p, r.ID); strings.Join(prs, " ") != want {
		t.Fatalf("ReportPRs = %q, want %s", prs, want)
	}
}
