package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/theclifmeister/termilator/internal/agent"
	"github.com/theclifmeister/termilator/internal/proto"
	"github.com/theclifmeister/termilator/internal/tasks"
	"github.com/theclifmeister/termilator/internal/thread"
)

// TestProgressSameEverywhere: the dashboard row, the details panel and
// the attach status bar and the sidebar show one thread's progress alike (docs/SPEC.md
// §7.3), here 2 of 4 steps plus 1 of 3 todos. The status bar once
// counted the session's todos alone ("33% 1/3").
func TestProgressSameEverywhere(t *testing.T) {
	now := time.Now()
	todos := []agent.Todo{{Text: "a", Status: agent.TodoCompleted}, {Text: "b", Status: agent.TodoInProgress}, {Text: "c"}}
	steps := []tasks.Step{{N: 1, Text: "A", Done: true}, {N: 2, Text: "B", Done: true}, {N: 3, Text: "C"}, {N: 4, Text: "D"}}
	st := &thread.Status{Todos: todos, SelfPercent: -1}
	st.Derive(steps, now)
	info := proto.SessionInfo{ID: "s-1", Role: proto.RoleThread, Project: "alpha", Thread: "t-0001", Agent: "claude",
		State: "working", TodosDone: 1, TodosTotal: 3, Current: "b", Created: now}
	data := Data{ServerOK: true, Sessions: []proto.SessionInfo{info}, Projects: []ProjectData{{Slug: "alpha", Counts: map[string]int{},
		Threads: []ThreadRow{{Record: &thread.Record{ID: "t-0001", Title: "Fix", State: thread.Running, Session: "s-1"}, Status: st}}}}}

	const want = "55% 2/4"
	var r row
	for _, x := range buildRows(data, "alpha") {
		if x.thread != nil {
			r = x
		}
	}
	if r.thread == nil || !strings.Contains(r.rest, want) || r.pct != 55 {
		t.Errorf("dashboard row: %q, bar %d%%", r.rest, r.pct)
	}
	if tree := buildTree(data.Projects, data.Sessions, treeIn{current: "alpha"}); len(tree) != 3 || tree[2].pct != 55 {
		t.Errorf("sidebar: %+v", tree)
	}
	m := newDash(DashOptions{Source: &fakeSource{data: data}, Width: 140 + sideDefault, Height: 30})
	if panel := ansi.Strip(strings.Join(m.details(r, 50), "\n")); !strings.Contains(panel, want) {
		t.Errorf("details panel:\n%s", panel)
	}
	if bar := statusLine(info, st, false, 120, ""); !strings.Contains(bar, want) || strings.Contains(bar, "33%") {
		t.Errorf("status bar: %q", bar)
	}
}
