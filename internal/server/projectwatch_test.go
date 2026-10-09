package server

import (
	"github.com/theclifmeister/terminatr/internal/project"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/theclifmeister/terminatr/internal/proto"
	"github.com/theclifmeister/terminatr/internal/tasks"
	"github.com/theclifmeister/terminatr/internal/thread"
)

// TestProjectWatchOf: what waits for the user comes first, in order:
// tasks in review (a PR ready to merge before one that isn't), threads'
// questions, red CI, blocked tasks; then the inbox, the threads with
// their state and PR, and the tasks on deck.
func TestProjectWatchOf(t *testing.T) {
	testPaths(t)
	p := newWatchProject(t)
	if _, err := p.Tasks().Add(human, []tasks.NewTask{
		{Title: "Review, no PR", Status: "review"},               // T1
		{Title: "Review, PR ready", Status: "review"},            // T2
		{Title: "Asks", Status: "started", Steps: []string{"a"}}, // T3
		{Title: "Red CI", Status: "started"},                     // T4
		{Title: "Stuck", Status: "blocked"},                      // T5
		{Title: "Next", Status: "ready"},                         // T6
		{Title: "Shipped", Status: "done"},                       // T7
		{Title: "Quiet", Status: "started"},                      // T8
		{Title: "Someday", Status: "open"},                       // T9, the backlog: not on deck
	}); err != nil {
		t.Fatal(err)
	}
	mk := func(task string) string {
		t.Helper()
		rec, err := thread.Create(p, thread.Record{Title: "work on " + task, Task: task})
		if err != nil {
			t.Fatal(err)
		}
		return rec.ID
	}
	t2, t3, t4, t8 := mk("T2"), mk("T3"), mk("T4"), mk("T8")
	if _, err := thread.UpdateStatus(p, t3, func(st *thread.Status) error {
		st.NeedsYou = "which port?"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := p.AskAccept(human, "T1"); err != nil {
		t.Fatal(err)
	}
	ts := filepath.Join(t.TempDir(), "ticker.json")
	os.WriteFile(ts, []byte(`{"threads": {
		"`+p.Slug+`/`+t2+`": {"pr_polled": "2026-10-07T12:00:00Z", "pr": {"number": 12, "url": "https://x/12", "state": "OPEN", "checks": "pass", "mergeable": "MERGEABLE"}},
		"`+p.Slug+`/`+t4+`": {"pr": {"number": 13, "url": "https://x/13", "state": "OPEN", "checks": "fail", "failed": 2}}},
		"projects": {"`+p.Slug+`": {"pr_polled": "2026-10-07T12:00:00Z", "synced": "2026-10-07T11:59:00Z", "gh_fails": 1}}}`), 0o600)
	sessions := []proto.SessionInfo{
		{ID: "s-8", Role: proto.RoleThread, Project: p.Slug, Thread: t8, State: "blocked", Reason: "question",
			Question: &proto.Question{Questions: []proto.QuestionItem{{Question: "done?", Answered: true}, {Question: "ship it?"}}}},
		{ID: "s-3", Role: proto.RoleThread, Project: p.Slug, Thread: t3, State: "idle"},
		{ID: "s-9", Role: proto.RoleThread, Project: "other", Thread: t4, State: "working"},
		// A coordinator whose nudge sits in a box with text in it; a
		// hold under QueueNotice is a box being typed into.
		{ID: "s-1", Role: proto.RoleCoordinator, Project: p.Slug, State: "idle", Queued: 1,
			QueueHeld: "prompt box not empty", QueueHeldSince: time.Now().Add(-3 * time.Minute)},
		{ID: "s-7", Role: proto.RoleThread, Project: p.Slug, State: "idle", Queued: 2,
			QueueHeld: "prompt box not empty", QueueHeldSince: time.Now()},
	}
	w := ProjectWatchOf(p, sessions, ts)

	type need struct{ why, task string }
	var got []need
	for _, n := range w.NeedsYou {
		got = append(got, need{n.Why, n.Task})
	}
	want := []need{{"queue", ""}, {"review", "T2"}, {"review", "T1"}, {"question", "T3"}, {"question", "T8"}, {"ci", "T4"}, {"blocked", "T5"}}
	if len(got) != len(want) {
		t.Fatalf("needs %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("needs %v, want %v", got, want)
		}
	}
	if n := w.NeedsYou[0]; n.Session != "s-1" || n.Thread != "" || n.Title != "1 queued prompt for the coordinator, held 3m0s: prompt box not empty" {
		t.Errorf("held queue %+v", n)
	}
	w.NeedsYou = w.NeedsYou[1:]
	if n := w.NeedsYou[0]; !n.Mergeable || n.PRNumber != 12 || n.PRURL != "https://x/12" || n.Thread != t2 || n.PR == "" {
		t.Errorf("T2 %+v", n)
	}
	if n := w.NeedsYou[1]; n.Asked != "accept" || n.Mergeable || n.Thread != "" {
		t.Errorf("T1 %+v", n)
	}
	if n := w.NeedsYou[2]; n.Question != "which port?" || n.Thread != t3 {
		t.Errorf("T3 %+v", n)
	}
	if n := w.NeedsYou[3]; n.Question != "ship it?" {
		t.Errorf("T8 %+v", n)
	}
	if n := w.NeedsYou[4]; n.PRNumber != 13 || n.Mergeable {
		t.Errorf("T4 %+v", n)
	}

	if len(w.Inbox) != 1 || w.Inbox[0].Kind != "accept" || w.Inbox[0].Subject != "T1" {
		t.Errorf("inbox %+v", w.Inbox)
	}
	if len(w.Ready) != 1 || w.Ready[0].Task != "T6" {
		t.Errorf("ready %+v", w.Ready)
	}
	if len(w.Threads) != 4 {
		t.Fatalf("threads %+v", w.Threads)
	}
	byID := map[string]proto.WatchThread{}
	for _, th := range w.Threads {
		byID[th.ID] = th
	}
	if th := byID[t4]; !th.PRBad || th.Session != "" || th.Task == nil || th.Task.ID != "T4" {
		t.Errorf("t4 %+v (another project's session must not count)", th)
	}
	if th := byID[t8]; th.Session != "s-8" || th.State != "blocked" || th.NeedsYou != "ship it?" {
		t.Errorf("t8 %+v", th)
	}
	if th := byID[t2]; !th.PRChecked.Equal(time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)) {
		t.Errorf("t2 PR checked %v", th.PRChecked)
	}
	if tk := w.Ticker; tk == nil || !tk.GHFailing || tk.PRPollSeconds != 120 || tk.Synced.IsZero() || tk.PRChecked.IsZero() {
		t.Errorf("ticker %+v", tk)
	}
	if th := byID[t3]; th.Task == nil || th.Task.StepsTotal != 1 || th.NeedsYou != "which port?" {
		t.Errorf("t3 %+v", th)
	}
}

// TestProjectWatchStream: project.watch answers the project, then sends
// a line when it changes behind the server's back; an unknown project is
// refused.
func TestProjectWatchStream(t *testing.T) {
	paths := testPaths(t)
	p := newWatchProject(t)
	t.Setenv(envWatchPoll, "50ms")
	startServer(t, paths)

	if _, _, err := WatchProject(paths, "nope"); err == nil {
		t.Fatal("watched an unknown project")
	}
	st, w, err := WatchProject(paths, p.Slug)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if w.Project != p.Slug || len(w.NeedsYou) != 0 || w.Threads == nil {
		t.Fatalf("first %+v", w)
	}
	got := make(chan proto.ProjectWatch, 4)
	go func() {
		for {
			w, err := st.Next()
			if err != nil {
				close(got)
				return
			}
			got <- w
		}
	}()
	if _, err := p.Tasks().Add(human, []tasks.NewTask{{Title: "Check it", Status: "review"}}); err != nil {
		t.Fatal(err)
	}
	select {
	case w := <-got:
		if len(w.NeedsYou) != 1 || w.NeedsYou[0].Task != "T1" {
			t.Fatalf("next %+v", w)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no line after the board changed")
	}
}

// TestProjectWatchNoPRHost: a project whose only repo has no remote has
// nothing to poll: its watch says so (never "failing", even with a
// failure kept from before), and a project with a GitHub origin doesn't.
func TestProjectWatchNoPRHost(t *testing.T) {
	testPaths(t)
	git := func(dir string, args ...string) {
		t.Helper()
		if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	repo := t.TempDir()
	git(repo, "init", "-q")
	p, err := project.New(project.Options{Slug: "demo", Repos: []string{repo}})
	if err != nil {
		t.Fatal(err)
	}
	st := filepath.Join(t.TempDir(), "ticker.json")
	if err := os.WriteFile(st, []byte(`{"projects": {"demo": {"gh_fails": 4, "synced": "2026-10-09T10:00:00Z"}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	w := ProjectWatchOf(p, nil, st)
	if w.Ticker == nil || !w.Ticker.NoPRHost || w.Ticker.GHFailing {
		t.Fatalf("no remote: ticker %+v", w.Ticker)
	}
	if rows := w.Ticker.Rows(time.Now()); len(rows) == 0 || rows[len(rows)-1] != (proto.TickerRow{Label: "PR host", Value: "none"}) {
		t.Errorf("rows %+v", rows)
	}
	git(repo, "remote", "add", "origin", "https://github.com/o/r.git")
	w = ProjectWatchOf(p, nil, st)
	if w.Ticker == nil || w.Ticker.NoPRHost || !w.Ticker.GHFailing {
		t.Fatalf("github origin: ticker %+v", w.Ticker)
	}
}
