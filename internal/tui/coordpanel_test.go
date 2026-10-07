package tui

import (
	"strings"
	"testing"
	"time"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"

	"github.com/theclifmeister/terminatr/internal/proto"
	"github.com/theclifmeister/terminatr/internal/view"
)

// sampleWatch is a project as project.watch sends it: a held queue, a
// task in review with a mergeable PR, a thread's question, the inbox
// with a repeated item, threads running and stopped, the context past
// its threshold and more tasks on deck than the panel lists.
func sampleWatch() *proto.ProjectWatch {
	return &proto.ProjectWatch{
		Project: "demo",
		NeedsYou: []proto.WatchNeed{
			{Why: proto.WhyQueue, Session: "s-9", Title: "1 queued prompt for the coordinator, held 3m0s: prompt box not empty"},
			{Why: proto.WhyReview, Task: "T63", Title: "Dashboard pane", Status: "review", Thread: "t-0059",
				PR: "#121 open, checks passed", PRURL: "https://github.com/o/r/pull/121", PRNumber: 121, Mergeable: true, Asked: "accept"},
			{Why: proto.WhyQuestion, Task: "T64", Title: "Mod tools", Status: "started", Thread: "t-0060", Question: "Which key?"},
		},
		Inbox: []proto.WatchItem{
			{ID: "1", Kind: "idle", Count: 3, Task: "T64", What: "idle", Title: "Mod tools"},
			{ID: "2", Kind: "pr-checks-failed", Count: 1, Task: "T65", What: "checks failed", Title: "Feed"},
		},
		Threads: []proto.WatchThread{
			{ID: "t-0059", Title: "Dashboard pane", Session: "s-5", State: "idle", Task: &proto.WatchTask{ID: "T63", Title: "Dashboard pane", StepsDone: 5, StepsTotal: 5},
				PR: "#121 open, checks passed", PRURL: "https://github.com/o/r/pull/121"},
			{ID: "t-0060", Title: "Mod tools", Session: "s-6", State: "blocked", Reason: "question",
				Task: &proto.WatchTask{ID: "T64", Title: "Mod tools", StepsDone: 2, StepsTotal: 4, Current: "Write the tests"}},
			{ID: "t-0061", Title: "Old work"},
		},
		Ready: []proto.WatchTodo{
			{Task: "T70", Title: "One", Status: "ready"}, {Task: "T71", Title: "Two", Status: "open", Asked: "delegate"},
			{Task: "T72", Title: "Three", Status: "open"}, {Task: "T73", Title: "Four", Status: "open"},
			{Task: "T74", Title: "Five", Status: "open"}, {Task: "T75", Title: "Six", Status: "open"},
		},
		Context: &proto.WatchContext{Tokens: 84_000, Window: 200_000, Percent: 42, Threshold: 40, Hint: true},
	}
}

func coordInfo() *infoData {
	return &infoData{slug: "demo", session: proto.SessionInfo{ID: "s-1", Role: proto.RoleCoordinator, Project: "demo"}, watch: sampleWatch()}
}

// TestCoordLines: beside a coordinator the panel shows what /tm shows,
// in its order: the summary and the context use with the /clear hint,
// what needs the user (a held queue, review, a question), the inbox with
// its repeats collapsed, the threads with their state and steps, and at
// most five tasks on deck; a click on a task opens it, on a running
// thread shows its pane, on a PR opens it.
func TestCoordLines(t *testing.T) {
	lines, hits := infoLines(coordInfo(), 50, time.Now())
	if len(hits) != len(lines) {
		t.Fatalf("%d hits for %d lines", len(hits), len(lines))
	}
	var plain []string
	for _, l := range lines {
		plain = append(plain, strings.TrimRight(ansi.Strip(l), " "))
	}
	text := strings.Join(plain, "\n")
	order := []string{
		"demo", "3 need you · 2 in inbox · 3 threads", "context 84k / 200k · 42%", "consider /clear",
		"NEEDS YOU", "s-9 ▲ prompts held 1 queued prompt", "tm agent explain s-9", "T63 ◆ in review Dashboard pane", "#121 open, checks passed",
		"asked the coordinator: accept", "T64 ▲ asks you Mod tools", "“Which key?”",
		"INBOX", "idle ×3 T64 idle Mod tools", "checks failed T65 checks failed Feed",
		"THREADS", "t-0059 T63", "idle 5/5", "t-0060 T64", "2/4", "▸ Write the tests", "t-0061", "stopped",
		"ON DECK", "T70 ○ ready One", "T71 ○ open Two · asked the coordinator:", "delegate", "T74 ○ open Five", "and 1 more",
	}
	at := 0
	for _, want := range order {
		i := strings.Index(text[at:], want)
		if i < 0 {
			t.Fatalf("panel lacks %q after offset %d:\n%s", want, at, text)
		}
		at += i
	}
	if strings.Contains(text, "T75") {
		t.Errorf("more than five tasks on deck:\n%s", text)
	}
	// What a click on each line does.
	find := func(s string) infoHit {
		for i, l := range plain {
			if strings.Contains(l, s) {
				return hits[i]
			}
		}
		t.Fatalf("no line %q", s)
		return infoHit{}
	}
	for _, tc := range []struct {
		line string
		want infoHit
	}{
		{"T63 ◆ in review", infoHit{kind: hitTask, task: 63}},
		{"“Which key?”", infoHit{kind: hitTask, task: 64}},
		{"idle ×3", infoHit{kind: hitTask, task: 64}},
		{"t-0059 T63", infoHit{kind: hitSession, session: "s-5"}},
		{"▸ Write the tests", infoHit{kind: hitSession, session: "s-6"}},
		{"t-0061", infoHit{}},
		{"T70 ○ ready", infoHit{kind: hitTask, task: 70}},
		{"NEEDS YOU", infoHit{}},
		{"tm agent explain", infoHit{}},
	} {
		if got := find(tc.line); got != tc.want {
			t.Errorf("click on %q: %+v, want %+v", tc.line, got, tc.want)
		}
	}
	prs := 0
	for _, h := range hits {
		if h.kind == hitPR && h.url == "https://github.com/o/r/pull/121" {
			prs++
		}
	}
	if prs != 2 {
		t.Errorf("%d PR lines, want the need's and the thread's", prs)
	}

	// Nothing waiting, no context reported yet.
	w := &proto.ProjectWatch{Project: "demo"}
	lines, _ = coordLines(w, 40, time.Now())
	text = ansi.Strip(strings.Join(lines, "\n"))
	if !strings.Contains(text, "Nothing waits for you.") || strings.Contains(text, "context") || strings.Contains(text, "Inbox") {
		t.Errorf("an empty project:\n%s", text)
	}
	// Below the threshold: the use, no hint.
	w.Context = &proto.WatchContext{Tokens: 1_200_000, Window: 2_000_000, Percent: 12, Threshold: 40}
	lines, _ = coordLines(w, 40, time.Now())
	if text = ansi.Strip(strings.Join(lines, "\n")); !strings.Contains(text, "context 1.2M / 2.0M · 12%") || strings.Contains(text, "/clear") {
		t.Errorf("below the threshold:\n%s", text)
	}
}

// TestCoordLinesNarrow: at every width, from the panel's least to the
// odd single column, no line is wider than the panel and every line has
// its hit.
func TestCoordLinesNarrow(t *testing.T) {
	for w := 1; w <= 60; w++ {
		lines, hits := coordLines(sampleWatch(), w, time.Now())
		if len(hits) != len(lines) {
			t.Fatalf("width %d: %d hits for %d lines", w, len(hits), len(lines))
		}
		for _, l := range lines {
			if lw := ansi.StringWidth(l); lw > w {
				t.Fatalf("width %d: a line %d wide: %q", w, lw, ansi.Strip(l))
			}
		}
	}
	// At the panel's least width the needs' titles wrap, not vanish.
	lines, _ := coordLines(sampleWatch(), view.InfoMin-1, time.Now())
	if text := ansi.Strip(strings.Join(lines, "\n")); !strings.Contains(text, "Dashboard") {
		t.Errorf("narrow panel lost the need's title:\n%s", text)
	}
}

// coordClient is an attach client showing a coordinator's pane with its
// panel, in a window cols wide.
func coordClient(t *testing.T, cols int) *client {
	c := infoClient(t, cols)
	c.info.data = coordInfo()
	c.v.Focus = "s-1"
	c.relayout()
	return c
}

// TestCoordPanelLayout: beside a coordinator the panel follows the
// thread panel's width rules: its width where there is room, cut to
// leave the pane its columns, hidden below that.
func TestCoordPanelLayout(t *testing.T) {
	for _, tc := range []struct{ cols, info int }{
		{200, view.InfoDefault},
		{view.SideDefault + view.SideRoom + 30, 30},
		{view.SideDefault + view.SideRoom + view.InfoMin, view.InfoMin},
		{view.SideDefault + view.SideRoom + view.InfoMin - 1, 0},
		{80, 0},
	} {
		c := coordClient(t, tc.cols)
		if c.infoW != tc.info {
			t.Errorf("%d columns: panel %d, want %d", tc.cols, c.infoW, tc.info)
		}
		if tc.info == 0 {
			continue
		}
		c.infoLayout()
		b, _ := c.appendInfo(nil, false)
		if s := ansi.Strip(string(b)); !strings.Contains(s, "NEEDS YOU") {
			t.Errorf("%d columns: the coordinator's panel isn't drawn: %q", tc.cols, s)
		}
	}
	// prefix+| toggles it, as beside a thread (the view, which this test
	// has no server for, does the rest).
	c := coordClient(t, 200)
	c.infoToggle()
	if strings.Contains(c.flash, "info panel shows beside") {
		t.Errorf("toggle beside a coordinator: %q", c.flash)
	}
}

// TestCoordPanelMouse: a click gives the panel the keyboard; on a
// running thread's row it shows that thread's pane (a bare console hands
// over to a full tm), on a task it opens the task view.
func TestCoordPanelMouse(t *testing.T) {
	c := coordClient(t, 200)
	c.rows = 60
	c.mu.Lock()
	c.infoLayout()
	hits := c.info.hits
	c.mu.Unlock()
	row := func(h infoHit) int {
		for i, x := range hits {
			if x == h {
				return i
			}
		}
		t.Fatalf("no line for %+v", h)
		return 0
	}
	x := c.infoX() + 3
	c.mouse(uv.MouseClickEvent{X: x, Y: 0, Button: uv.MouseLeft})
	if c.kb != areaInfo {
		t.Fatal("a click didn't give the panel the keyboard")
	}
	// enter has no one task to open beside a coordinator.
	c.key(uv.Key{Code: uv.KeyEnter})
	if !strings.Contains(c.flash, "click a task") {
		t.Errorf("enter beside a coordinator: %q", c.flash)
	}
	c.bare = true
	c.mouse(uv.MouseClickEvent{X: x, Y: row(infoHit{kind: hitSession, session: "s-6"}), Button: uv.MouseLeft})
	select {
	case <-c.end:
	case <-time.After(time.Second):
		t.Fatal("a click on a thread didn't leave for its pane")
	}
	if g := c.goTo; g == nil || g.Project != "demo" || g.Session != "s-6" {
		t.Fatalf("thread click: %+v", c.goTo)
	}

	c = coordClient(t, 200)
	c.rows = 60
	c.mu.Lock()
	c.infoLayout()
	hits = c.info.hits
	c.mu.Unlock()
	c.mouse(uv.MouseClickEvent{X: x, Y: row(infoHit{kind: hitTask, task: 70}), Button: uv.MouseLeft})
	select {
	case <-c.end:
	case <-time.After(time.Second):
		t.Fatal("a click on a task didn't leave for the task view")
	}
	if o := c.result.Over; o == nil || o.Key != "t" || o.Task != 70 || o.Project != "demo" {
		t.Fatalf("task click: %+v", c.result.Over)
	}
}

// TestCoordLinesTicker: the ticker's timers show as one line under the
// summary (red while gh fails), and a thread's PR says when it was last
// checked.
func TestCoordLinesTicker(t *testing.T) {
	now := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	w := sampleWatch()
	w.Ticker = &proto.WatchTicker{PRChecked: now.Add(-40 * time.Second), Synced: now.Add(-time.Minute), PRPollSeconds: 120}
	w.Threads[0].PRChecked = now.Add(-30 * time.Second)
	lines, _ := coordLines(w, 80, now)
	text := ansi.Strip(strings.Join(lines, "\n"))
	for _, want := range []string{"ticker · PRs checked 40s ago, next 1m20s · synced 1m ago · PR host ok", "#121 open, checks passed · checked 30s ago"} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q:\n%s", want, text)
		}
	}
	w.Ticker.GHFailing = true
	lines, _ = coordLines(w, 80, now)
	if text = ansi.Strip(strings.Join(lines, "\n")); !strings.Contains(text, "PR host failing") || strings.Contains(text, "PR host ok") {
		t.Errorf("PR host failing:\n%s", text)
	}
}
