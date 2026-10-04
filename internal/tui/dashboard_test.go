package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/theclifmeister/termalator/internal/agent"
	"github.com/theclifmeister/termalator/internal/project"
	"github.com/theclifmeister/termalator/internal/proto"
	"github.com/theclifmeister/termalator/internal/tasks"
	"github.com/theclifmeister/termalator/internal/thread"
)

// fakeSource records the dashboard's actions.
type fakeSource struct {
	data    Data
	opened  []string
	started []string
}

func (f *fakeSource) Load() Data                             { return f.data }
func (f *fakeSource) Board(string) (*tasks.Board, error)     { return &tasks.Board{}, nil }
func (f *fakeSource) NewProject(name string) (string, error) { return name, nil }
func (f *fakeSource) StartShell(cwd string, c, r int) (string, error) {
	f.started = append(f.started, "shell "+cwd)
	return "s-9", nil
}
func (f *fakeSource) OpenProject(slug string, c, r int) (string, error) {
	f.opened = append(f.opened, slug)
	return "s-" + slug, nil
}

func testData() Data {
	now := time.Now()
	return Data{ServerOK: true,
		Sessions: []proto.SessionInfo{
			{ID: "s-1", Role: proto.RoleCoordinator, Project: "alpha", Agent: "claude", State: "blocked", Reason: "question", Created: now},
			{ID: "s-2", Role: proto.RoleThread, Project: "alpha", Thread: "t-0002", Agent: "claude", State: "blocked", Reason: "permission", Created: now},
			{ID: "s-5", Role: proto.RoleThread, Project: "beta", Thread: "t-0005", Agent: "claude", State: "working", Created: now},
			{ID: "s-3", Role: proto.RoleShell, Argv: []string{"/bin/zsh", "-l"}, State: "", Cwd: "/x", Created: now},
			{ID: "s-4", Role: proto.RoleShell, Agent: "claude", State: "blocked", Reason: "permission", Cwd: "/y", Created: now},
		},
		Projects: []ProjectData{
			{Slug: "alpha", Counts: map[string]int{"needs_you": 1, "in_motion": 1}},
			{Slug: "beta", Counts: map[string]int{}, Threads: []ThreadRow{
				{Record: &thread.Record{ID: "t-0005", Title: "Write docs", Task: "T4", State: thread.Running, Session: "s-5", Reports: 1},
					Status: &thread.Status{Percent: 60, PercentSource: "steps", StepsDone: 3, StepsTotal: 5, Current: "Draft §2", NeedsYou: "Which licence?",
						Todos: []agent.Todo{{Text: "Outline", Status: agent.TodoCompleted}, {Text: "Draft §2", Status: agent.TodoInProgress}}},
					Report:  &thread.Report{PR: "https://github.com/o/r/pull/7", Next: []string{"Merge the PR", "Delete the branch"}},
					TaskRec: &tasks.Task{ID: 4, Title: "Docs", Steps: []tasks.Step{{N: 1, Text: "Plan", Done: true}, {N: 2, Text: "Write"}}}},
				{Record: &thread.Record{ID: "t-0006", Title: "Old work", State: thread.Stopped}},
			}},
		},
	}
}

// screen is the dashboard as text, without its colours or the sidebar.
func screen(m *dash) string {
	lines := strings.Split(whole(m), "\n")
	for i, l := range lines {
		r := []rune(l)
		lines[i] = string(r[min(m.sideW(), len(r)):])
	}
	return strings.Join(lines, "\n")
}

// whole is the whole window as text: the sidebar and the dashboard.
func whole(m *dash) string { return ansi.Strip(m.render()) }

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
	m := newDash(DashOptions{Source: src, Width: 120 + sideDefault, Height: 30})
	m.layout.Details = false // one column: the rows at full width
	m.setData(src.data)
	out := screen(m)
	needs := strings.Index(out, "NEEDS YOU")
	projs := strings.Index(out, "PROJECTS")
	sess := strings.Index(out, "SESSIONS")
	if needs < 0 || !(needs < projs && projs < sess) {
		t.Fatalf("sections out of order:\n%s", out)
	}
	if strings.Contains(out, "s-5 ") {
		t.Errorf("thread session listed besides its thread row:\n%s", out)
	}
	// NEEDS YOU: the blocked coordinator and the user's own blocked
	// session; nothing of the threads (a blocked one, a report, a
	// question), which are their coordinator's.
	for _, want := range []string{
		"NEEDS YOU 2 ─",
		"! alpha        coordinator                    ▲ blocked   question",
		"! s-4          claude                         ▲ blocked   permission",
		"  alpha        coordinator                    ▲ blocked",
		"    s-2          t-0002",
		"  beta         coordinator                    —           enter starts the coordinator",
		"  s-3          /bin/zsh -l                    ● running",
		"                 t-0005 Write docs              ● working   ▰▰▰▱▱  T4  60% 3/5 ▸ Draft §2  report waiting  PR #7",
		"                 t-0006 Old work                · stopped",
		"PROJECTS 2 ─",
		"● server ok · 5 sessions",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in\n%s", want, out)
		}
	}
	needsYou := out[needs:projs]
	for _, not := range []string{"t-0002", "t-0005", "report", "Which licence?"} {
		if strings.Contains(needsYou, not) {
			t.Errorf("NEEDS YOU shows %q:\n%s", not, needsYou)
		}
	}
	// The first selectable row is the blocked coordinator.
	if r, _ := m.selected(); r.session != "s-1" {
		t.Errorf("selected %+v", r)
	}
}

func TestDashboardKeys(t *testing.T) {
	src := &fakeSource{data: testData()}
	m := newDash(DashOptions{Source: src, Cwd: "/work", Width: 100, Height: 30})
	m.setData(src.data)

	// enter on the blocked coordinator attaches to it.
	run(m, press(m, "enter"))
	if m.result.Attach != "s-1" {
		t.Fatalf("attach %q", m.result.Attach)
	}

	// The keys that acted on threads and tasks are gone: c, d, a, 1-9
	// do nothing on any row.
	for _, sel := range []string{"n:s-1", "th:beta:t-0005", "p:alpha"} {
		m.sel, m.result = sel, DashResult{}
		for _, k := range []string{"c", "d", "a", "1", "2"} {
			if cmd := press(m, k); cmd != nil || m.top() != nil {
				t.Fatalf("%s on %s did something: overlay %T", k, sel, m.top())
			}
		}
	}
	if len(src.started)+len(src.opened) != 0 {
		t.Fatalf("started %v opened %v", src.started, src.opened)
	}

	// enter on a thread watches its session.
	m.sel = "th:beta:t-0005"
	run(m, press(m, "enter"))
	if m.result.Attach != "s-5" {
		t.Fatalf("watch %q", m.result.Attach)
	}

	// s starts a shell where the dashboard was started.
	m.sel, m.result = "p:alpha", DashResult{}
	run(m, press(m, "s"))
	if got := strings.Join(src.started, ","); got != "shell /work" || m.result.Attach != "s-9" {
		t.Fatalf("started %q attach %q", got, m.result.Attach)
	}

	// ] from alpha opens beta's coordinator; [ from beta wraps to alpha.
	m.current = "alpha"
	run(m, press(m, "]"))
	m.current = "beta"
	run(m, press(m, "["))
	if got := strings.Join(src.opened, ","); got != "beta,alpha" {
		t.Fatalf("opened %q", got)
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
// task's steps and its report's Next lines, to read: the coordinator
// acts on them. i opens the project's inbox.
func TestDashboardThreadRow(t *testing.T) {
	src := &fakeSource{data: testData()}
	src.data.Projects[1].Items = []project.Item{{ID: "x", Kind: "report", Subject: "t-0005", Summary: "t-0005 handed in report 1"}}
	m := newDash(DashOptions{Source: src, Width: 100, Height: 40})
	m.setData(src.data)
	m.sel = "th:beta:t-0005"
	out := screen(m)
	for _, want := range []string{"✓ Outline", "◐ Draft §2", "T4 steps:", "✓ 1 Plan", "○ 2 Write",
		"report 1 (new) next:", "1 Merge the PR", "2 Delete the branch"} {
		if !strings.Contains(out, want) {
			t.Errorf("thread detail lacks %q:\n%s", want, out)
		}
	}
	for _, not := range []string{"a acks", "1-9"} {
		if strings.Contains(out, not) {
			t.Errorf("thread detail offers %q:\n%s", not, out)
		}
	}
	press(m, "i")
	if _, ok := m.top().(*inboxView); !ok || !strings.Contains(screen(m), "t-0005 handed in report 1") {
		t.Fatalf("inbox view:\n%s", screen(m))
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
	got := statusLine(info, nil, false, 80, "")
	want := "\x1b[7m s-4 · termalator coordinator · working 40% 2/5 ▸ Write §8"
	if !strings.HasPrefix(got, want) || !strings.HasSuffix(got, `prefix+d dashboard `+"\x1b[27m") {
		t.Fatalf("status line %q", got)
	}
	if w := len([]rune(strings.TrimSuffix(strings.TrimPrefix(got, "\x1b[7m"), "\x1b[27m"))); w != 80 {
		t.Fatalf("status line is %d cells, want 80", w)
	}
	// Split panes: where the focused one is.
	if got := statusLine(info, nil, false, 120, "pane 2/3"); !strings.Contains(got, "▸ Write §8 · pane 2/3 ") {
		t.Fatalf("status line with panes %q", got)
	}
	// After the prefix: the commands.
	got = statusLine(info, nil, true, 160, "")
	if !strings.Contains(got, `d dashboard · p ] [ projects · i t , ? · % " split`) || !strings.Contains(got, `prefix again sends it`) {
		t.Fatalf("pending status line %q", got)
	}
}

// TestDashboardOverlays: views open on top of the list and close back
// to the one below; the help lists every labelled action.
func TestDashboardOverlays(t *testing.T) {
	src := &fakeSource{data: testData()}
	m := newDash(DashOptions{Source: src, Width: 100, Height: 30})
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
	if !b.open || !strings.Contains(screen(m), "alpha T1") {
		t.Fatalf("enter on the board:\n%s", screen(m))
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
	out := screen(m)
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

// TestDashboardSplit: a wide window shows the selected row's details
// beside the list; < > | change the layout and ui.json keeps it; the
// mouse selects rows and drags the divider.
func TestDashboardSplit(t *testing.T) {
	src := &fakeSource{data: testData()}
	ui := filepath.Join(t.TempDir(), "ui.json")
	m := newDash(DashOptions{Source: src, Width: 140 + sideDefault, Height: 40, UIFile: ui})
	m.setData(src.data)

	m.sel = "th:beta:t-0005"
	out := screen(m)
	for _, want := range []string{"│ t-0005 Write docs", "● working", "task      T4", "progress  ▰▰▰▱▱ 60% 3/5",
		"PR        https://github.com/o/r/pull/7", "report    1, new · for the coordinator", "✓ Outline", "1 Merge the PR",
		"enter watches it; the coordinator acts on it"} {
		if !strings.Contains(out, want) {
			t.Errorf("details lack %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "        todos:") {
		t.Errorf("details shown under the row as well as beside it:\n%s", out)
	}
	m.sel = "p:beta"
	if out := screen(m); !strings.Contains(out, "no coordinator; enter starts it") {
		t.Errorf("project details:\n%s", out)
	}

	// < narrows the list and saves the layout; | hides the panel.
	_, before := m.split()
	press(m, "<")
	if _, after := m.split(); after >= before {
		t.Fatalf("< kept the list at %d columns", after)
	}
	press(m, "|")
	if split, _ := m.split(); split {
		t.Fatal("| left the panel shown")
	}
	if l := LoadLayout(ui); l.Details || l.Split != defaultSplit-splitStep {
		t.Fatalf("ui.json holds %+v", l)
	}
	press(m, "|")

	// A click selects the row under it; dragging the divider resizes.
	sw := m.sideW()
	m.Update(tea.MouseClickMsg{X: sw + 3, Y: 3, Button: tea.MouseLeft}) // NEEDS YOU's second row
	if m.sel != "n:s-4" {
		t.Fatalf("click selected %q", m.sel)
	}
	_, lw := m.split()
	m.Update(tea.MouseClickMsg{X: sw + lw, Y: 5, Button: tea.MouseLeft})
	m.Update(tea.MouseMotionMsg{X: sw + 70, Y: 5, Button: tea.MouseLeft})
	m.Update(tea.MouseReleaseMsg{X: sw + 70, Y: 5, Button: tea.MouseLeft})
	if _, lw := m.split(); lw != 70 || LoadLayout(ui).Split != 0.5 {
		t.Fatalf("drag: list %d wide, ui.json %+v", lw, LoadLayout(ui))
	}

	// Too narrow a window: one column, and < says why.
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	press(m, "<")
	if !strings.Contains(m.msg, "120 columns") {
		t.Fatalf("msg %q", m.msg)
	}
}

func TestLayoutFile(t *testing.T) {
	dir := t.TempDir()
	if l := LoadLayout(filepath.Join(dir, "none.json")); l != DefaultLayout {
		t.Fatalf("missing file: %+v", l)
	}
	bad := filepath.Join(dir, "bad.json")
	os.WriteFile(bad, []byte("{"), 0o644)
	if l := LoadLayout(bad); l != DefaultLayout {
		t.Fatalf("bad file: %+v", l)
	}
	far := filepath.Join(dir, "far.json")
	os.WriteFile(far, []byte(`{"details":true,"split":5}`), 0o644)
	if l := LoadLayout(far); l.Split != maxSplit {
		t.Fatalf("split out of range: %+v", l)
	}
}

// TestDashboardPrefix: the prefix works on the dashboard as in a
// session, and a key typed after it in a session runs once back here.
func TestDashboardPrefix(t *testing.T) {
	src := &fakeSource{data: testData()}
	m := newDash(DashOptions{Source: src, Width: 100, Height: 30})
	m.setData(src.data)
	m.sel = "p:alpha"
	prefix := tea.KeyPressMsg{Code: 'b', Mod: tea.ModCtrl}
	m.Update(prefix)
	if !m.prefixed || !strings.Contains(screen(m), `prefix ▸ any dashboard key`) {
		t.Fatalf("prefix not shown:\n%s", screen(m))
	}
	press(m, "t")
	if _, ok := m.top().(*boardView); !ok || m.prefixed {
		t.Fatalf("prefix t: overlay %T", m.top())
	}
	m.pop()
	m.Update(prefix)
	press(m, "d") // already on the dashboard: nothing
	if m.top() != nil || m.prefixed {
		t.Fatal("prefix d on the dashboard did something")
	}

	// Back from a session after prefix p: the switcher opens.
	m = newDash(DashOptions{Source: src, Width: 100, Height: 30, State: DashState{Then: "p"}})
	m.setData(src.data)
	if _, ok := m.top().(*switchView); !ok {
		t.Fatalf("then p: overlay %T", m.top())
	}
	m.setData(src.data)
	m.pop()
	if m.top() != nil {
		t.Fatal("then ran twice")
	}
}

// TestDashboardFooter: the footer lists the keys that apply to the
// selected row.
func TestDashboardFooter(t *testing.T) {
	src := &fakeSource{data: testData()}
	m := newDash(DashOptions{Source: src, Width: 100, Height: 30})
	m.setData(src.data)
	for sel, want := range map[string]string{
		"n:s-1":          "enter attach · t tasks · i inbox · p projects · , settings · ? help · q quit",
		"th:beta:t-0005": "enter watch · t tasks · i inbox · p projects",
		"th:beta:t-0006": "t tasks · i inbox · p projects",
		"p:beta":         "enter open · t tasks",
	} {
		m.sel = sel
		if got := m.footKeys(); !strings.HasPrefix(got, want) {
			t.Errorf("%s: footer %q, want %q…", sel, got, want)
		}
	}
	m.data.Projects = nil
	m.setData(Data{ServerOK: true})
	if got := m.footKeys(); !strings.HasPrefix(got, "n new project · , settings") {
		t.Errorf("no projects: footer %q", got)
	}
}

// TestDashboardPopups: views draw as boxes over the dimmed list, and the
// settings show the project's safety settings from config.toml.
func TestDashboardPopups(t *testing.T) {
	home := t.TempDir()
	t.Setenv("TERMALATOR_HOME", home)
	os.WriteFile(filepath.Join(home, "config.toml"), []byte("[projects.alpha]\nyolo = true\n"), 0o600)
	src := &fakeSource{data: testData()}
	src.data.Projects[0].Items = []project.Item{{ID: "x", Kind: "report", Summary: "t-0002 handed in report 1"}}
	m := newDash(DashOptions{Source: src, Width: 100, Height: 30})
	m.setData(src.data)
	m.sel = "p:alpha"

	press(m, "i")
	out := screen(m)
	for _, want := range []string{"╭─ alpha inbox ─", "│ ", "t-0002 handed in report 1", "╰─", "r refresh · esc back",
		"PROJECTS 2"} { // the list stays in view behind the box
		if !strings.Contains(out, want) {
			t.Errorf("inbox popup lacks %q:\n%s", want, out)
		}
	}
	for _, l := range strings.Split(out, "\n") {
		if w := ansi.StringWidth(l); w > 100 {
			t.Errorf("line %d cells wide: %q", w, l)
		}
	}
	m.pop()

	press(m, ",")
	out = screen(m)
	for _, want := range []string{"settings · alpha", "[projects.alpha]", "yolo                   true",
		"start_threads          propose  default", "prefix                 ctrl+b  default", "e edit config.toml"} {
		if !strings.Contains(out, want) {
			t.Errorf("settings lack %q:\n%s", want, out)
		}
	}

	// A tiny window: the box takes the body, and nothing overflows.
	m.Update(tea.WindowSizeMsg{Width: 30, Height: 8})
	for _, l := range strings.Split(screen(m), "\n") {
		if w := ansi.StringWidth(l); w > 30 {
			t.Errorf("tiny window: line %d cells wide: %q", w, l)
		}
	}
}

// TestPromptWraps: a long prompt text wraps in the popup instead of
// losing its start.
func TestPromptWraps(t *testing.T) {
	long := "/var/folders/0g/abcdefghijklmnopqrstuvwxyz/T/TestSmokeMakeRun3197026846/003/work"
	lines := wrapInput("claude session in directory: ", long+"█", 40)
	got := ansi.Strip(strings.Join(lines, ""))
	if got != "claude session in directory: "+long+"█" || len(lines) != 3 {
		t.Fatalf("wrapped %q", lines)
	}
	for _, l := range lines {
		if w := ansi.StringWidth(l); w > 40 {
			t.Fatalf("line %d cells wide: %q", w, l)
		}
	}
	src := &fakeSource{data: testData()}
	m := newDash(DashOptions{Source: src, Width: 100, Height: 30})
	m.setData(src.data)
	press(m, "n")
	m.top().(*inputView).text = long
	if out := screen(m); !strings.Contains(out, "new project name: /var/folders") {
		t.Fatalf("prompt:\n%s", out)
	}
}
