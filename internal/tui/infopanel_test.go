package tui

import (
	"io"
	"log"
	"strconv"
	"strings"
	"testing"
	"time"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"

	"github.com/theclifmeister/terminatr/internal/proto"
	"github.com/theclifmeister/terminatr/internal/server"
	"github.com/theclifmeister/terminatr/internal/tasks"
	"github.com/theclifmeister/terminatr/internal/thread"
	"github.com/theclifmeister/terminatr/internal/ticker"
	"github.com/theclifmeister/terminatr/internal/view"
)

// sampleInfo is a thread with a task, steps, a PR and a report.
func sampleInfo(now time.Time) *infoData {
	return &infoData{
		slug:    "demo",
		session: proto.SessionInfo{ID: "s-4", Role: proto.RoleThread, Project: "demo", Thread: "t-0002", State: "working"},
		rec: &thread.Record{ID: "t-0002", Title: "Info panel", Task: "T26", Model: "sonnet", Branch: "tm/demo/t-0002-info",
			Worktree: "/tmp/wt/t-0002", ReportAt: now.Add(-5 * time.Minute)},
		status: &thread.Status{Percent: 40, StepsDone: 2, StepsTotal: 5, Current: "Write the tests",
			NeedsYou: "Which key toggles it?", Updated: now.Add(-2 * time.Minute)},
		report: &thread.Report{PR: "https://github.com/o/r/pull/70", Next: []string{"Merge PR #70", "Release it"},
			Text: "PR: https://github.com/o/r/pull/70\n\n## Report\n\nLayout done.\nContent done.\nTests next.\nMore.\n\n## Next\n\nMerge PR #70\nRelease it\n"},
		task: &tasks.Task{ID: 26, Title: "Info panel on thread panes", Status: tasks.Started, Steps: []tasks.Step{
			{N: 1, Text: "Layout", Done: true}, {N: 2, Text: "Content", Done: true}, {N: 3, Text: "Toggle"}, {N: 4, Text: "Clicks"}}},
		pr:       ticker.PR{Number: 70, URL: "https://github.com/o/r/pull/70", State: "OPEN", Checks: "pending", MergeState: "BEHIND", Base: "main"},
		attached: []string{"chart.png", "plan.md"},
	}
}

// TestInfoLines: the panel shows the task and its steps (the one under
// way marked), the thread's state and what it waits on, the PR in the
// ticker's words with behind main, the last report's first lines (never
// its Next, which is the coordinator's), and where it works; a click on
// the task or the PR opens it.
func TestInfoLines(t *testing.T) {
	now := time.Now()
	lines, hits := infoLines(sampleInfo(now), 40, now)
	if len(hits) != len(lines) {
		t.Fatalf("%d hits for %d lines", len(hits), len(lines))
	}
	var plain []string
	for _, l := range lines {
		if w := ansi.StringWidth(l); w != 40 && l != "" {
			t.Errorf("line %q is %d cells", ansi.Strip(l), w)
		}
		plain = append(plain, strings.TrimRight(ansi.Strip(l), " "))
	}
	text := strings.Join(plain, "\n")
	for _, want := range []string{
		"T26 Info panel on thread panes", "started", "steps", "2/4",
		ic().todoDone + " Layout", ic().todoNow + " Toggle", ic().todoOpen + " Clicks",
		"thread    t-0002", "working", "model     sonnet", "now       ▸ Write the tests", "needs you Which key toggles it?",
		"PR        #70 open, checks pending,\n           behind main",
		"Last report · 5m ago", "Layout done.", "Tests next.",
		"attached  chart.png, plan.md",
		"branch    tm/demo/t-0002-info", "worktree  /tmp/wt/t-0002", "active    2m ago",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("no %q in\n%s", want, text)
		}
	}
	if strings.Contains(text, "library") {
		t.Errorf("an attachment's path in the panel:\n%s", text)
	}
	for _, next := range []string{"Merge PR #70", "Release it"} {
		if strings.Contains(text, next) {
			t.Errorf("the report's Next line %q shows:\n%s", next, text)
		}
	}
	if strings.Contains(text, "More.") {
		t.Errorf("more than the report's first three lines:\n%s", text)
	}
	if strings.Index(text, ic().todoNow+" Toggle") > strings.Index(text, ic().todoOpen+" Clicks") {
		t.Error("steps out of order")
	}
	got := map[hitKind][]string{}
	for i, h := range hits {
		got[h.kind] = append(got[h.kind], plain[i])
	}
	if len(got[hitTask]) == 0 || !strings.Contains(got[hitTask][0], "T26") {
		t.Errorf("task lines %q", got[hitTask])
	}
	if len(got[hitPR]) != 2 || !strings.Contains(got[hitPR][0], "#70") {
		t.Errorf("PR lines %q", got[hitPR])
	}

	// A failed check and a conflict, in the ticker's words; a PR only
	// known from the report; no task.
	d := sampleInfo(now)
	d.pr.Checks, d.pr.Failed, d.pr.Mergeable, d.pr.MergeState = "fail", 1, "CONFLICTING", "DIRTY"
	if l := d.prLine(); l != "#70 open, 1 check failed, conflicts" {
		t.Errorf("PR line %q", l)
	}
	d.pr, d.task = ticker.PR{}, nil
	lines, _ = infoLines(d, 40, now)
	text = ansi.Strip(strings.Join(lines, "\n"))
	if !strings.Contains(text, "PR        #70") || !strings.Contains(text, "no task") {
		t.Errorf("without the ticker's PR or a task:\n%s", text)
	}
	if lines, _ := infoLines(nil, 30, now); len(lines) != 1 {
		t.Errorf("no data: %q", lines)
	}
}

func TestReportHead(t *testing.T) {
	got := reportHead("PR: x\n## Report\n\none\n\ntwo\n## Next\nthree\n", 3)
	if strings.Join(got, "|") != "one|two" {
		t.Fatalf("report head %q", got)
	}
}

// infoClient is an attach client showing thread s-4 in a window cols
// wide, with the info panel's data loaded.
func infoClient(t *testing.T, cols int) *client {
	t.Helper()
	c, err := newClient(server.Paths{}, log.New(io.Discard, "", 0))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.close)
	c.vc = &ViewConn{}
	c.dashboard = true
	c.side, c.info = &sidebar{}, &infoPanel{data: sampleInfo(time.Now())}
	c.v = view.View{Name: view.Main, Mode: view.ModeLayout, Focus: "s-4", Panel: true, Cols: uint16(cols), Rows: 30}
	c.v.Normalize()
	c.setWindow(cols, 30)
	c.relayout()
	return c
}

// TestInfoPanelLayout: the attach client draws the panel at the window's
// right edge, the pane and the status bar between it and the sidebar;
// it hides when the window is narrow, when it is off and beside a
// session that isn't a thread's.
func TestInfoPanelLayout(t *testing.T) {
	for _, tc := range []struct{ cols, info int }{
		{200, view.InfoDefault},
		{view.SideDefault + view.SideRoom + 30, 30},
		{view.SideDefault + view.SideRoom + view.InfoMin - 1, 0},
		{80, 0},
	} {
		c := infoClient(t, tc.cols)
		if c.infoW != tc.info || c.paneCols != tc.cols-c.sideW-tc.info {
			t.Errorf("%d columns: panel %d, pane %d; want panel %d", tc.cols, c.infoW, c.paneCols, tc.info)
		}
		if tc.info == 0 {
			continue
		}
		c.infoLayout()
		b, _ := c.appendInfo(nil, false)
		if !strings.Contains(string(b), "\x1b[1;"+strconv.Itoa(tc.cols-tc.info+1)+"H") || !strings.Contains(ansi.Strip(string(b)), "T26") {
			t.Errorf("%d columns: the panel isn't drawn at the right edge: %q", tc.cols, ansi.Strip(string(b)))
		}
		if b, _ := c.appendInfo(nil, false); len(b) != 0 {
			t.Errorf("%d columns: unchanged panel drawn again", tc.cols)
		}
	}
	c := infoClient(t, 200)
	c.v.Info.Off = true
	c.relayout()
	if c.infoW != 0 {
		t.Error("off, the panel shows")
	}
	c.v.Info.Off, c.v.Panel = false, false
	c.relayout()
	if c.infoW != 0 {
		t.Error("beside a plain session, the panel shows")
	}
	// prefix+| beside a plain session says why nothing happens.
	c.infoToggle()
	if !strings.Contains(c.flash, "beside a thread's or a coordinator's pane") {
		t.Errorf("toggle beside a plain session: %q", c.flash)
	}
}

// TestInfoPanelMouse: a click in the panel gives it the keyboard, whose
// arrows scroll it and esc gives back; a click on the PR opens it in the
// browser, a press on its border starts a drag, and a click on the task
// opens the task view over the session.
func TestInfoPanelMouse(t *testing.T) {
	c := infoClient(t, 200)
	c.rows = 8 // shorter than the panel, so it scrolls
	c.mu.Lock()
	c.infoLayout()
	lines := c.info.hits
	c.mu.Unlock()
	row := func(h hitKind) int {
		for i, x := range lines {
			if x.kind == h {
				return i
			}
		}
		t.Fatalf("no line for %d", h)
		return 0
	}
	x := c.infoX() + 3
	// A click on a plain line: the keyboard.
	c.mouse(uv.MouseClickEvent{X: x, Y: 2, Button: uv.MouseLeft})
	if c.kb != areaInfo {
		t.Fatal("a click didn't give the panel the keyboard")
	}
	c.key(uv.Key{Code: uv.KeyDown})
	c.key(uv.Key{Code: uv.KeyDown})
	if c.info.top != 2 {
		t.Fatalf("down twice: top %d", c.info.top)
	}
	c.key(uv.Key{Code: uv.KeyUp})
	c.key(uv.Key{Code: 'x', Text: "x"}) // dropped: nothing reaches the pane
	if c.info.top != 1 || c.kb != areaInfo {
		t.Fatalf("up: top %d, focus %d", c.info.top, c.kb)
	}
	c.mouse(uv.MouseWheelEvent{X: x, Y: 2, Button: uv.MouseWheelDown})
	if c.info.top != 4 {
		t.Fatalf("wheel: top %d", c.info.top)
	}
	c.key(uv.Key{Code: uv.KeyEscape})
	if c.kb == areaInfo {
		t.Fatal("esc kept the keyboard in the panel")
	}
	// The PR: the browser.
	opened := ""
	defer func(f func(string) error) { openURL = f }(openURL)
	openURL = func(u string) error { opened = u; return nil }
	c.info.top = 0
	pr := row(hitPR)
	c.info.top = pr - 1
	c.mouse(uv.MouseClickEvent{X: x, Y: 1, Button: uv.MouseLeft})
	if opened != "https://github.com/o/r/pull/70" {
		t.Fatalf("PR click opened %q", opened)
	}
	// The border: a drag.
	c.mouse(uv.MouseClickEvent{X: c.infoX(), Y: 3, Button: uv.MouseLeft})
	if !c.info.drag {
		t.Fatal("a press on the border didn't start a drag")
	}
	c.info.drag = false
	// The task: the task view over the session.
	c.info.top = 0
	c.mouse(uv.MouseClickEvent{X: x, Y: row(hitTask), Button: uv.MouseLeft})
	select {
	case <-c.end:
	case <-time.After(time.Second):
		t.Fatal("a click on the task didn't leave for the task view")
	}
	if o := c.result.Over; o == nil || o.Key != "t" || o.Task != 26 || o.Project != "demo" {
		t.Fatalf("task click: %+v", c.result.Over)
	}
}

// TestInfoPanelKeyboardCycle: prefix+tab goes from the pane to the
// sidebar, the info panel and back to the pane.
func TestInfoPanelKeyboardCycle(t *testing.T) {
	c := infoClient(t, 200)
	c.sideW = 32
	c.nextArea()
	if c.kb != areaSide {
		t.Fatal("not in the sidebar")
	}
	c.nextArea()
	if c.kb != areaInfo {
		t.Fatal("not on to the info panel")
	}
	c.nextArea()
	if c.kb != areaMain {
		t.Fatal("not back to the pane")
	}
}

// TestInfoPanelClickToFocus: with T25's click-to-focus, a click gives its
// area the keyboard: the panel, then a sidebar row (which takes it from
// the panel), then the panel again (which takes it from the sidebar).
func TestInfoPanelClickToFocus(t *testing.T) {
	c := infoClient(t, 200)
	c.side.projects = []ProjectData{{Slug: "demo"}}
	c.mouse(uv.MouseClickEvent{X: c.infoX() + 3, Y: 2, Button: uv.MouseLeft})
	if c.kb != areaInfo {
		t.Fatal("a click in the panel didn't give it the keyboard")
	}
	c.mu.Lock()
	rows := c.sideTree()
	c.mu.Unlock()
	if len(rows) == 0 {
		t.Fatal("no sidebar rows")
	}
	c.mouse(uv.MouseClickEvent{X: 3, Y: 1, Button: uv.MouseLeft})
	if c.kb != areaSide {
		t.Fatalf("a click on a sidebar row: focus %d", c.kb)
	}
	c.mouse(uv.MouseClickEvent{X: c.infoX() + 3, Y: 2, Button: uv.MouseLeft})
	if c.kb != areaInfo {
		t.Fatal("a click in the panel didn't take the keyboard from the sidebar")
	}
}

// TestOneFocusModel: the dashboard and a session share one keyboard
// model: cycle steps through the areas shown, both ways; in a session,
// tab in the sidebar and in the info panel moves on as prefix+tab does.
func TestOneFocusModel(t *testing.T) {
	areas := []area{areaMain, areaSide, areaInfo}
	if cycle(areas, areaMain, false) != areaSide || cycle(areas, areaInfo, false) != areaMain || cycle(areas, areaMain, true) != areaInfo {
		t.Fatal("cycle")
	}
	if cycle(areas[:2], areaInfo, false) != areaSide { // an area gone counts as the first
		t.Fatal("cycle from an area that no longer shows")
	}
	c := infoClient(t, 200)
	tab := uv.Key{Code: uv.KeyTab}
	c.nextArea()
	if c.kb != areaSide {
		t.Fatalf("prefix+tab: %d", c.kb)
	}
	c.key(tab)
	if c.kb != areaInfo {
		t.Fatalf("tab in the sidebar: %d", c.kb)
	}
	c.key(tab)
	if c.kb != areaMain {
		t.Fatalf("tab in the info panel: %d", c.kb)
	}
	// Without the panel, the sidebar's tab goes back to the pane.
	c.v.Info.Off = true
	c.relayout()
	c.nextArea()
	c.key(tab)
	if c.kb != areaMain {
		t.Fatalf("tab in the sidebar, no panel: %d", c.kb)
	}
}

// TestInfoLinesQuestion: an open question menu shows in the panel with
// its options by number, and the thread's state says "question open".
func TestInfoLinesQuestion(t *testing.T) {
	now := time.Now()
	d := sampleInfo(now)
	d.session.State, d.session.Reason = "blocked", "question"
	d.session.Question = &proto.Question{Since: now.Add(-3 * time.Minute), Questions: []proto.QuestionItem{{
		Question: "Which color?", Header: "Color", Options: []proto.QuestionOption{{Label: "Blue", Description: "calm"}, {Label: "Red"}},
	}}}
	lines, _ := infoLines(d, 60, now)
	var plain []string
	for _, l := range lines {
		plain = append(plain, strings.TrimRight(ansi.Strip(l), " "))
	}
	text := strings.Join(plain, "\n")
	for _, want := range []string{"blocked question open", "Question open", "1. [Color] Which color?", "1. Blue — calm", "2. Red", "3. (the user's own words)"} {
		if !strings.Contains(text, want) {
			t.Errorf("panel lacks %q:\n%s", want, text)
		}
	}
	d.session.Question = nil
	lines, _ = infoLines(d, 60, now)
	if strings.Contains(ansi.Strip(strings.Join(lines, "\n")), "Question open") {
		t.Error("a question shows without one open")
	}
}

// TestQuestionOpenRow: a thread row's lead says "question open" while
// the server holds the menu, and "question" without it.
func TestQuestionOpenRow(t *testing.T) {
	s := proto.SessionInfo{ID: "s-4", State: "blocked", Reason: "question"}
	tr := &ThreadRow{Record: &thread.Record{ID: "t-0002", Session: "s-4"}}
	_, reason, _ := threadState(tr, map[string]proto.SessionInfo{"s-4": s})
	if reason != "question" {
		t.Errorf("reason = %q", reason)
	}
	s.Question = &proto.Question{Questions: []proto.QuestionItem{{Question: "Q?"}}}
	_, reason, _ = threadState(tr, map[string]proto.SessionInfo{"s-4": s})
	if reason != "question open" {
		t.Errorf("reason = %q", reason)
	}
}
