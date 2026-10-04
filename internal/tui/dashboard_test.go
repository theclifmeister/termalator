package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/theclifmeister/termalator/internal/agent"
	"github.com/theclifmeister/termalator/internal/project"
	"github.com/theclifmeister/termalator/internal/proto"
	"github.com/theclifmeister/termalator/internal/tasks"
	"github.com/theclifmeister/termalator/internal/thread"
)

// fakeSource records the dashboard's actions.
type fakeSource struct {
	data    Data
	done    []string
	opened  []string
	started []string
	acked   []string
	next    []string
}

func (f *fakeSource) Load() Data                             { return f.data }
func (f *fakeSource) Board(string) (*tasks.Board, error)     { return &tasks.Board{}, nil }
func (f *fakeSource) NewProject(name string) (string, error) { return name, nil }
func (f *fakeSource) StartShell(cwd string, c, r int) (string, error) {
	f.started = append(f.started, "shell "+cwd)
	return "s-9", nil
}
func (f *fakeSource) StartAgent(cwd string, c, r int) (string, error) {
	f.started = append(f.started, "agent "+cwd)
	return "s-9", nil
}
func (f *fakeSource) OpenProject(slug string, c, r int) (string, error) {
	f.opened = append(f.opened, slug)
	return "s-" + slug, nil
}
func (f *fakeSource) MarkDone(slug string, id int) error {
	f.done = append(f.done, slug+":"+(&tasks.Task{ID: id}).Ref())
	return nil
}

func (f *fakeSource) Ack(slug, id string) error {
	f.acked = append(f.acked, slug+":"+id)
	return nil
}
func (f *fakeSource) PromptNext(slug, id string, n int) error {
	f.next = append(f.next, fmt.Sprintf("%s:%s:%d", slug, id, n))
	return nil
}

func testData() Data {
	now := time.Now()
	review := &tasks.Task{ID: 7, Title: "Pick a licence", Status: tasks.Review}
	started := &tasks.Task{ID: 3, Title: "Build it", Status: tasks.Started}
	return Data{ServerOK: true,
		Sessions: []proto.SessionInfo{
			{ID: "s-1", Role: proto.RoleCoordinator, Project: "alpha", Agent: "claude", State: "idle", Created: now},
			{ID: "s-2", Role: proto.RoleThread, Project: "alpha", Thread: "t-0002", Agent: "claude", State: "blocked", Reason: "permission", Created: now},
			{ID: "s-5", Role: proto.RoleThread, Project: "beta", Thread: "t-0005", Agent: "claude", State: "working", Created: now},
			{ID: "s-3", Role: proto.RoleShell, Argv: []string{"/bin/zsh", "-l"}, State: "", Cwd: "/x", Created: now},
		},
		Projects: []ProjectData{
			{Slug: "alpha", Counts: map[string]int{"needs_you": 1, "in_motion": 1}, NeedsYou: []*tasks.Task{review},
				Inbox: []InboxRow{{Item: project.Item{ID: "i1", Kind: project.KindConfirmDone, Subject: "T3", Summary: "coordinator asks to mark T3 done", NeedsUser: true}, Task: started}}},
			{Slug: "beta", Counts: map[string]int{}, Threads: []ThreadRow{
				{Record: &thread.Record{ID: "t-0005", Title: "Write docs", Task: "T4", State: thread.Running, Session: "s-5", Reports: 1},
					Status: &thread.Status{Percent: 60, PercentSource: "steps", StepsDone: 3, StepsTotal: 5, Current: "Draft §2",
						Todos: []agent.Todo{{Text: "Outline", Status: agent.TodoCompleted}, {Text: "Draft §2", Status: agent.TodoInProgress}}},
					Report:  &thread.Report{PR: "https://github.com/o/r/pull/7", Next: []string{"Merge the PR", "Delete the branch"}},
					TaskRec: &tasks.Task{ID: 4, Title: "Docs", Steps: []tasks.Step{{N: 1, Text: "Plan", Done: true}, {N: 2, Text: "Write"}}}},
				{Record: &thread.Record{ID: "t-0006", Title: "Old work", State: thread.Stopped}},
			}},
		},
	}
}

func press(m *dash, keys ...string) tea.Cmd {
	var cmd tea.Cmd
	for _, k := range keys {
		var msg tea.KeyPressMsg
		switch k {
		case "enter":
			msg = tea.KeyPressMsg{Code: tea.KeyEnter}
		case "down":
			msg = tea.KeyPressMsg{Code: tea.KeyDown}
		default:
			msg = tea.KeyPressMsg{Code: rune(k[0]), Text: k}
		}
		_, cmd = m.Update(msg)
	}
	return cmd
}

// run executes a command and feeds its message back, as the program would.
func run(m *dash, cmd tea.Cmd) {
	if cmd == nil {
		return
	}
	if msg := cmd(); msg != nil {
		if _, ok := msg.(actionMsg); ok {
			m.Update(msg)
		}
	}
}

func TestDashboardRows(t *testing.T) {
	src := &fakeSource{data: testData()}
	m := newDash(DashOptions{Source: src, AgentName: "claude", Width: 120, Height: 30})
	m.setData(src.data)
	out := m.render()
	needs := strings.Index(out, "NEEDS YOU")
	projs := strings.Index(out, "PROJECTS")
	sess := strings.Index(out, "SESSIONS")
	if needs < 0 || !(needs < projs && projs < sess) {
		t.Fatalf("sections out of order:\n%s", out)
	}
	if strings.Contains(out, "s-5 ") {
		t.Errorf("thread session listed besides its thread row:\n%s", out)
	}
	for _, want := range []string{
		"! alpha        t-0002                         blocked   permission",
		"? alpha        T7 Pick a licence              review",
		"? alpha        T3 Build it                    confirm   d marks it done",
		"  alpha        coordinator                    idle",
		"    s-2          t-0002",
		"  beta         coordinator                    —         enter starts the coordinator",
		"  s-3          /bin/zsh -l                    running",
		"                 t-0005 Write docs              working   T4  60% 3/5 ▸ Draft §2  report waiting  PR #7",
		"                 t-0006 Old work                stopped",
		"? beta         t-0005 Write docs              report    unacknowledged report · a acks",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in\n%s", want, out)
		}
	}
	// The first selectable row is the blocked session.
	if r, _ := m.selected(); r.session != "s-2" {
		t.Errorf("selected %+v", r)
	}
}

func TestDashboardKeys(t *testing.T) {
	src := &fakeSource{data: testData()}
	m := newDash(DashOptions{Source: src, AgentName: "claude", Cwd: "/work", Width: 100, Height: 30})
	m.setData(src.data)

	// enter on the blocked session attaches to it.
	run(m, press(m, "enter"))
	if m.result.Attach != "s-2" {
		t.Fatalf("attach %q", m.result.Attach)
	}

	// d on a task in review, and on a confirmation of a started task.
	m.result = DashResult{}
	run(m, press(m, "down", "d"))
	run(m, press(m, "down", "d"))
	if got := strings.Join(src.done, ","); got != "alpha:T7,alpha:T3" {
		t.Fatalf("done %q", got)
	}
	// d on a started task in the task view is refused.
	if cmd := m.markDone("alpha", &tasks.Task{ID: 3, Status: tasks.Started}, false); cmd != nil || !strings.Contains(m.msg, "only a task in review") {
		t.Fatalf("started task marked done: %q", m.msg)
	}

	// ] from alpha opens beta's coordinator; [ from beta wraps to alpha.
	m.current = "alpha"
	run(m, press(m, "]"))
	m.current = "beta"
	run(m, press(m, "["))
	if got := strings.Join(src.opened, ","); got != "beta,alpha" {
		t.Fatalf("opened %q", got)
	}

	// c asks for a directory, relative to the dashboard's.
	m.result = DashResult{}
	press(m, "c")
	in, ok := m.top().(*inputView)
	if !ok || in.text != "/work" {
		t.Fatalf("c: overlay %T", m.top())
	}
	in.text = "sub"
	run(m, press(m, "enter"))
	if got := src.started[len(src.started)-1]; got != "agent /work/sub" || m.result.Attach != "s-9" {
		t.Fatalf("started %q attach %q", got, m.result.Attach)
	}
}

func TestDashboardBell(t *testing.T) {
	src := &fakeSource{data: testData()}
	src.data.Alerts = 3
	m := newDash(DashOptions{Source: src, Width: 100, Height: 30})
	if hasBell(m.setData(src.data)) {
		t.Fatal("bell for alerts from before the dashboard opened")
	}
	if hasBell(m.setData(src.data)) {
		t.Fatal("bell without a new alert")
	}
	d := testData()
	d.Alerts = 4
	if !hasBell(m.setData(d)) {
		t.Fatal("no bell for a new alert")
	}
}

// TestDashboardThreadRow: a selected thread row shows its todos, its
// task's steps and its report's Next lines; a acks the report and a
// digit sends a Next line; i opens the project's inbox.
func TestDashboardThreadRow(t *testing.T) {
	src := &fakeSource{data: testData()}
	src.data.Projects[1].Items = []project.Item{{ID: "x", Kind: "report", Subject: "t-0005", Summary: "t-0005 handed in report 1"}}
	m := newDash(DashOptions{Source: src, Width: 100, Height: 40})
	m.setData(src.data)
	m.sel = "th:beta:t-0005"
	out := m.render()
	for _, want := range []string{"[x] Outline", "[~] Draft §2", "T4 steps:", "[x] 1 Plan", "[ ] 2 Write",
		"report 1 (new) next:  a acks it", "1 Merge the PR", "2 Delete the branch"} {
		if !strings.Contains(out, want) {
			t.Errorf("thread detail lacks %q:\n%s", want, out)
		}
	}
	run(m, press(m, "a"))
	run(m, press(m, "2"))
	if strings.Join(src.acked, ",") != "beta:t-0005" || strings.Join(src.next, ",") != "beta:t-0005:2" {
		t.Fatalf("acked %v next %v", src.acked, src.next)
	}
	press(m, "3")
	if !strings.Contains(m.msg, "no ## Next line 3") {
		t.Fatalf("msg %q", m.msg)
	}
	press(m, "i")
	if _, ok := m.top().(*inboxView); !ok || !strings.Contains(m.render(), "t-0005 handed in report 1") {
		t.Fatalf("inbox view:\n%s", m.render())
	}
}

func hasBell(cmd tea.Cmd) bool {
	msg := cmd()
	batch, ok := msg.(tea.BatchMsg)
	if !ok {
		return false
	}
	for _, c := range batch {
		if raw, ok := c().(tea.RawMsg); ok && raw.Msg == "\a" {
			return true
		}
	}
	return false
}

func TestStatusLine(t *testing.T) {
	info := proto.SessionInfo{ID: "s-4", Role: proto.RoleCoordinator, Project: "termalator", Agent: "claude",
		State: "working", TodosDone: 2, TodosTotal: 5, Current: "Write §8"}
	got := statusLine(info, chord{'\\'}, 80)
	want := "\x1b[7m s-4 · termalator coordinator · working 40% 2/5 ▸ Write §8"
	if !strings.HasPrefix(got, want) || !strings.HasSuffix(got, `ctrl+\ dashboard `+"\x1b[27m") {
		t.Fatalf("status line %q", got)
	}
	if w := len([]rune(strings.TrimSuffix(strings.TrimPrefix(got, "\x1b[7m"), "\x1b[27m"))); w != 80 {
		t.Fatalf("status line is %d cells, want 80", w)
	}
}

// TestDashboardOverlays: views open on top of the list and close back
// to the one below; the help lists every labelled action.
func TestDashboardOverlays(t *testing.T) {
	src := &fakeSource{data: testData()}
	m := newDash(DashOptions{Source: src, AgentName: "claude", Width: 100, Height: 30})
	m.setData(src.data)
	m.sel = "p:alpha"

	m.Update(boardMsg{}) // no board open: ignored
	m.Update(boardMsg{slug: "alpha", board: &tasks.Board{}})
	press(m, "t")
	b, ok := m.top().(*boardView)
	if !ok || b.slug != "alpha" {
		t.Fatalf("t: overlay %T", m.top())
	}
	m.Update(boardMsg{slug: "alpha", board: &tasks.Board{Tasks: []*tasks.Task{{ID: 1, Title: "One", Status: tasks.Review}}}})
	press(m, "enter")
	if !b.open || !strings.Contains(m.render(), "alpha T1") {
		t.Fatalf("enter on the board:\n%s", m.render())
	}
	press(m, "?") // the board takes its own keys; ? isn't one
	if m.top() != b {
		t.Fatalf("? on the board opened %T", m.top())
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if b.open || m.top() != b {
		t.Fatal("esc on a task returns to the board")
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if m.top() != nil {
		t.Fatalf("esc on the board returns to the list, have %T", m.top())
	}

	press(m, "?")
	out := m.render()
	for _, a := range actions {
		if a.label != "" && !strings.Contains(out, " "+a.label+" ") {
			t.Errorf("help lacks %q:\n%s", a.label, out)
		}
	}
	press(m, "x")
	if m.top() != nil {
		t.Fatal("any key closes the help")
	}

	// A failed board load closes the board and says why.
	press(m, "t")
	m.Update(boardMsg{slug: "alpha", err: fmt.Errorf("no TASKS.md")})
	if m.top() != nil || m.msg != "no TASKS.md" {
		t.Fatalf("board error: overlay %T msg %q", m.top(), m.msg)
	}
}
