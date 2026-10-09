package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/theclifmeister/terminatr/internal/agent"
	"github.com/theclifmeister/terminatr/internal/config"
	"github.com/theclifmeister/terminatr/internal/project"
	"github.com/theclifmeister/terminatr/internal/proto"
	"github.com/theclifmeister/terminatr/internal/tasks"
	"github.com/theclifmeister/terminatr/internal/thread"
	"github.com/theclifmeister/terminatr/internal/version"
)

// statusLine is statusBar's line, without its buttons.
func statusLine(s proto.SessionInfo, ts *thread.Status, pending bool, cols int, where string) string {
	line, _ := statusBar(s, ts, pending, cols, where)
	return line
}

// fakeSource records the dashboard's actions. Settings go to the real
// settings file under the test's TERMINATR_HOME, and Load reads them
// back once one was set.
type fakeSource struct {
	data     Data
	board    *tasks.Board
	opened   []string
	started  []string
	settings []string // table.key=value
	repos    []string // +path or -path
	// delegated are the tasks asked to be delegated: "slug T12"; asked
	// the other asks: "accept slug T12", "send-back slug T12 note".
	delegated  []string
	asked      []string
	adopted    []string // "slug s-3"
	answered   []string // slugs whose questions were opened
	reviews    map[int]Review
	agents     []string
	remote     []string // "slug on" or "slug off"
	lifecycle  []string // "slug verb"
	memory     project.Memory
	library    []thread.LibFile
	libFiles   map[string]string // "t-0001/name" -> content
	libRemoved []string          // "t-0001/name", "t-0001/" for all
}

func (f *fakeSource) SetRemote(slug string, on bool) (string, error) {
	state := map[bool]string{true: "on", false: "off"}[on]
	f.remote = append(f.remote, slug+" "+state)
	return "remote control " + state + " for " + slug, nil
}

func (f *fakeSource) Lifecycle(slug, verb string) (string, error) {
	f.lifecycle = append(f.lifecycle, slug+" "+verb)
	if verb == "pause" || verb == "resume" {
		if err := config.SetProject(slug, "paused", verb == "pause"); err != nil {
			return "", err
		}
	}
	return verb + " " + slug, nil
}

func (f *fakeSource) Load() Data {
	if len(f.settings) > 0 || len(f.lifecycle) > 0 {
		if cfg, err := config.Load(); err == nil {
			for i := range f.data.Projects {
				s, _ := cfg.Safety(f.data.Projects[i].Slug)
				f.data.Projects[i].Safety = &s
				f.data.Projects[i].Own = cfg.Own(f.data.Projects[i].Slug)
			}
			all, _ := cfg.AllProjects()
			f.data.Defaults = &all
			f.data.Mods, f.data.ModsBand = cfg.Mods, cfg.ModsBand
		}
	}
	return f.data
}
func (f *fakeSource) Board(string) (*tasks.Board, error) {
	if f.board != nil {
		return f.board, nil
	}
	return &tasks.Board{}, nil
}
func (f *fakeSource) SetSetting(table, key string, value any) error {
	f.settings = append(f.settings, fmt.Sprintf("%s.%s=%v", table, key, value))
	if table == config.DefaultsTable {
		if value == nil {
			return config.UnsetDefaults(key)
		}
		return config.SetDefaults(key, value)
	}
	if slug, ok := strings.CutPrefix(table, "projects."); ok {
		if value == nil {
			return config.UnsetProject(slug, key)
		}
		return config.SetProject(slug, key, value)
	}
	return config.Set(table, key, value)
}
func (f *fakeSource) SetRepo(slug, path string, add bool) error {
	sign := map[bool]string{true: "+", false: "-"}[add]
	f.repos = append(f.repos, sign+path)
	for i := range f.data.Projects {
		if p := &f.data.Projects[i]; p.Slug == slug {
			if add {
				p.Repos = append(p.Repos, path)
			} else {
				p.Repos = slices.DeleteFunc(p.Repos, func(r string) bool { return r == path })
			}
		}
	}
	return nil
}
func (f *fakeSource) OpenQuestions(slug string) (proto.QuestionsOpenResult, error) {
	f.answered = append(f.answered, slug)
	return proto.QuestionsOpenResult{Session: "s-1", Via: "queued", Open: 2}, nil
}
func (f *fakeSource) AskAdopt(slug string, s proto.SessionInfo) (bool, error) {
	f.adopted = append(f.adopted, slug+" "+s.ID)
	return true, nil
}
func (f *fakeSource) Ask(slug string, id int, kind, note string) (bool, error) {
	ref := fmt.Sprintf("T%d", id)
	what := slug + " " + ref
	if note != "" {
		what += " " + note
	}
	switch kind {
	case project.KindDelegate:
		f.delegated = append(f.delegated, what)
	default:
		f.asked = append(f.asked, kind+" "+what)
	}
	for i := range f.data.Projects {
		if p := &f.data.Projects[i]; p.Slug == slug {
			if project.TaskAsked(p.Items, ref) != "" {
				return false, nil
			}
			p.Items = append(p.Items, project.Item{ID: "x-" + kind + "-" + ref, Kind: kind, Subject: ref})
		}
	}
	return true, nil
}
func (f *fakeSource) Library(string) ([]thread.LibFile, error) { return f.library, nil }
func (f *fakeSource) LibraryRead(_, id, name string, max int64) ([]byte, bool, error) {
	return []byte(f.libFiles[id+"/"+name]), false, nil
}
func (f *fakeSource) LibraryRemove(_, id, name string) (int, error) {
	f.libRemoved = append(f.libRemoved, id+"/"+name)
	var keep []thread.LibFile
	n := 0
	for _, l := range f.library {
		if l.Thread == id && (name == "" || l.Name == name) {
			n++
			continue
		}
		keep = append(keep, l)
	}
	f.library = keep
	return n, nil
}
func (f *fakeSource) Review(slug string, t *tasks.Task) Review { return f.reviews[t.ID] }
func (f *fakeSource) Memory(string) (project.Memory, error)    { return f.memory, nil }
func (f *fakeSource) Agents() []string                         { return f.agents }
func (f *fakeSource) ModsNote() string                         { return "Claude Code 2.1.289" }
func (f *fakeSource) NewProject(name string) (string, error)   { return name, nil }
func (f *fakeSource) OpenProject(slug string, c, r int) (string, error) {
	f.opened = append(f.opened, slug)
	return "s-" + slug, nil
}

// Catalogs are the built-in manifests' models (claude only, as the
// tests' agents) with the test's config.toml over them.
func (f *fakeSource) Catalogs() []Catalog {
	reg, _ := agent.Load("")
	a, _ := reg.Get("claude")
	cfg, _ := config.Load()
	return []Catalog{{Agent: "claude", Manifest: agent.ModelsOf(a), Settings: cfg.Agent("claude")}}
}
func (f *fakeSource) SetModels(name string, s config.AgentSettings) error {
	f.settings = append(f.settings, fmt.Sprintf("agents.%s=%v", name, s))
	return config.SetAgentModels(name, s)
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

// screen is the dashboard as text, without its colours or the sidebar;
// with a popup open the whole window, which the popup is centred on.
func screen(m *dash) string {
	lines := strings.Split(whole(m), "\n")
	if m.geo != nil {
		return strings.Join(lines, "\n")
	}
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

// TestDashboardRows: NEEDS YOU across projects, then the current
// project's own section (its coordinator, threads and task counts; the
// sidebar's tree lists the projects), then SESSIONS. A block's reason
// and a new report lead the rest of a row, so they are never cut off.
func TestDashboardRows(t *testing.T) {
	src := &fakeSource{data: testData()}
	m := newDash(DashOptions{Source: src, Width: 120 + sideDefault, Height: 30, State: DashState{Current: "beta"}})
	m.layout.Details = false // one column: the rows at full width
	m.setData(src.data)
	out := screen(m)
	needs := strings.Index(out, "NEEDS YOU")
	proj := strings.Index(out, " beta ─")
	sess := strings.Index(out, "SESSIONS")
	if needs < 0 || !(needs < proj && proj < sess) {
		t.Fatalf("sections out of order:\n%s", out)
	}
	if strings.Contains(out, "s-5 ") || strings.Contains(out, "PROJECTS") || strings.Contains(out[needs:], " alpha ─") {
		t.Errorf("a thread's session, a PROJECTS section or another project listed:\n%s", out)
	}
	// NEEDS YOU: the blocked coordinator and the user's own blocked
	// session; nothing of the threads (a blocked one, a report, a
	// question), which are their coordinator's.
	for _, want := range []string{
		"NEEDS YOU 2 ─",
		"  alpha         coordinator                               ▲ blocked   question",
		"  s-4           claude                                    ▲ blocked   permission",
		"  coordinator                               —           not running; enter starts it",
		"  T4 Write docs                             ● working   report new  ▰▰▰▱▱  60% 3/5 ▸ Draft §2  PR #7  t-0005",
		"  t-0006 Old work                           · stopped",
		"  tasks: 0 need you · 0 in motion · 0 on deck",
		"  s-3           /bin/zsh -l                               ▷ running",
		"● server ok · 5 sessions",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in\n%s", want, out)
		}
	}
	needsYou := out[needs:proj]
	for _, not := range []string{"t-0002", "t-0005", "report", "Which licence?"} {
		if strings.Contains(needsYou, not) {
			t.Errorf("NEEDS YOU shows %q:\n%s", not, needsYou)
		}
	}
	// The first selectable row is the blocked coordinator.
	if r, _ := m.selected(); r.session != "s-1" {
		t.Errorf("selected %+v", r)
	}
	// Without a current project, the first is listed.
	m = newDash(DashOptions{Source: src, Width: 120 + sideDefault, Height: 30})
	m.setData(src.data)
	if out := screen(m); !strings.Contains(out, " alpha ─") || strings.Contains(out, "t-0005") {
		t.Errorf("no current project:\n%s", out)
	}
}

// TestDashboardDoneThread: a done thread with a new report says so in
// full, its blocked reason or report leading the row even in a narrow
// list.
func TestDashboardDoneThread(t *testing.T) {
	d := testData()
	th := &d.Projects[1].Threads[1]
	th.Done, th.Reports, th.State = true, 1, thread.Running
	d.Sessions = append(d.Sessions, proto.SessionInfo{ID: "s-6", Role: proto.RoleThread, Project: "beta", Thread: "t-0006", Agent: "claude", State: "idle"})
	th.Session = "s-6"
	src := &fakeSource{data: d}
	m := newDash(DashOptions{Source: src, Width: 82 + sideDefault, Height: 30, State: DashState{Current: "beta"}})
	m.setData(src.data)
	if out := screen(m); !strings.Contains(out, "t-0006 Old work                 ✓ done      report new") {
		t.Fatalf("done thread:\n%s", out)
	}
}

func TestDashboardKeys(t *testing.T) {
	src := &fakeSource{data: testData()}
	m := newDash(DashOptions{Source: src, Cwd: "/work", Width: 100, Height: 30, State: DashState{Current: "beta"}})
	m.setData(src.data)

	// enter on the blocked coordinator attaches to it.
	run(m, press(m, "enter"))
	if m.result.Attach != "s-1" {
		t.Fatalf("attach %q", m.result.Attach)
	}

	// The keys that acted on threads and tasks are gone: c, d, 1-9 do
	// nothing on any row (a opens the project popup now).
	for _, sel := range []string{"n:s-1", "th:beta:t-0005", "p:alpha"} {
		m.sel, m.result = sel, DashResult{}
		for _, k := range []string{"c", "d", "1", "2"} {
			if cmd := press(m, k); cmd != nil || m.top() != nil {
				t.Fatalf("%s on %s did something: overlay %T", k, sel, m.top())
			}
		}
	}
	if len(src.started)+len(src.opened) != 0 {
		t.Fatalf("started %v opened %v", src.started, src.opened)
	}

	// enter on a thread attaches its session. (Attaching alpha's
	// coordinator made alpha current: back to beta.)
	m.current = "beta"
	m.rebuild()
	m.sel = "th:beta:t-0005"
	run(m, press(m, "enter"))
	if m.result.Attach != "s-5" {
		t.Fatalf("attach %q", m.result.Attach)
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
// task's steps and its report's state, to read: the coordinator acts on
// them, so the report's Next lines never show. i opens the project's
// inbox.
func TestDashboardThreadRow(t *testing.T) {
	src := &fakeSource{data: testData()}
	src.data.Projects[1].Items = []project.Item{{ID: "x", Kind: "report", Subject: "t-0005", Summary: "t-0005 handed in report 1"}}
	m := newDash(DashOptions{Source: src, Width: 100, Height: 40, State: DashState{Current: "beta"}})
	m.setData(src.data)
	m.sel = "th:beta:t-0005"
	out := screen(m)
	for _, want := range []string{"✓ Outline", "◐ Draft §2", "steps 1/2", "✓ 1 Plan", "○ 2 Write",
		"report new · for the coordinator"} {
		if !strings.Contains(out, want) {
			t.Errorf("thread detail lacks %q:\n%s", want, out)
		}
	}
	for _, not := range []string{"a acks", "1-9", "next:", "Merge the PR", "Delete the branch"} {
		if strings.Contains(out, not) {
			t.Errorf("thread detail offers %q:\n%s", not, out)
		}
	}
	press(m, "i")
	if pv, ok := m.top().(*projectView); !ok || pv.tab != tabInbox || !strings.Contains(screen(m), "t-0005 handed in report 1") {
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
	info := proto.SessionInfo{ID: "s-4", Role: proto.RoleCoordinator, Project: "terminatr", Agent: "claude",
		State: "working", TodosDone: 2, TodosTotal: 5, Current: "Write §8"}
	got := statusLine(info, nil, false, 100, "")
	want := "\x1b[7m terminatr coordinator · working 40% 2/5 ▸ Write §8 · s-4"
	if !strings.HasPrefix(got, want) || !strings.HasSuffix(got, ` ≡ menu · prefix+d dashboard `+"\x1b[27m") {
		t.Fatalf("status line %q", got)
	}
	if w := len([]rune(strings.TrimSuffix(strings.TrimPrefix(got, "\x1b[7m"), "\x1b[27m"))); w != 100 {
		t.Fatalf("status line is %d cells, want 100", w)
	}
	// No window buttons: split panes and zoom are gone.
	for _, b := range []string{"│", "─", "⤢", "×"} {
		if strings.Contains(got, b) {
			t.Fatalf("status line has %q: %q", b, got)
		}
	}
	// After the prefix: the commands.
	got = statusLine(info, nil, true, 160, "")
	if !strings.Contains(got, `d dashboard · q quit · a project · n new · p ] [ projects · i t , ? · { } b sidebar · | info · tab sidebar keys`) || !strings.Contains(got, `prefix again sends it`) {
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
	pv, ok := m.top().(*projectView)
	if !ok || pv.slug != "alpha" || pv.tab != tabTasks {
		t.Fatalf("t: overlay %T", m.top())
	}
	m.Update(boardMsg{slug: "alpha", board: &tasks.Board{Tasks: []*tasks.Task{{ID: 1, Title: "One", Status: tasks.Review}}}})
	press(m, "enter")
	tv, ok := m.top().(*taskView)
	if !ok || !strings.Contains(screen(m), "Task · alpha") {
		t.Fatalf("enter on the board:\n%s", screen(m))
	}
	press(m, "?") // the task takes its own keys; ? isn't one
	if m.top() != tv {
		t.Fatalf("? on a task opened %T", m.top())
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if m.top() != pv {
		t.Fatal("esc on a task returns to the popup")
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if m.top() != nil {
		t.Fatalf("esc on the popup returns to the list, have %T", m.top())
	}

	press(m, "?")
	out := ansi.Strip(strings.Join(m.top().(*helpView).box(m).body, "\n"))
	for _, a := range actions {
		if a.label != "" && !strings.Contains(out, " "+a.label+" ") && !strings.HasPrefix(out, a.label+" ") && !strings.Contains(out, "\n"+a.label+" ") {
			t.Errorf("help lacks %q:\n%s", a.label, out)
		}
	}
	press(m, "down") // the arrows scroll
	if m.top() == nil {
		t.Fatal("down closed the help")
	}
	press(m, "x")
	if m.top() == nil {
		t.Fatal("x closed the help")
	}
	keyPress(m, "esc")
	if m.top() != nil {
		t.Fatal("esc didn't close the help")
	}
}

// TestDashboardSplit: a wide window shows the selected row's details
// beside the list; < > | change the layout and ui.json keeps it; the
// mouse selects rows and drags the divider.
func TestDashboardSplit(t *testing.T) {
	src := &fakeSource{data: testData()}
	ui := filepath.Join(t.TempDir(), "ui.json")
	m := newDash(DashOptions{Source: src, Width: 140 + sideDefault, Height: 40, UIFile: ui, State: DashState{Current: "beta"}})
	m.setData(src.data)

	m.sel = "th:beta:t-0005"
	out := screen(m)
	for _, want := range []string{"│ T4 Write docs", "● working", "thread    t-0005", "progress  ▰▰▰▱▱ 60% 3/5",
		"PR        https://github.com/o/r/pull/7", "report    1, new · for the coordinator", "✓ Outline",
		"enter attach"} {
		if !strings.Contains(out, want) {
			t.Errorf("details lack %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "Merge the PR") {
		t.Errorf("details show the report's Next lines:\n%s", out)
	}
	if strings.Contains(out, "        todos") {
		t.Errorf("details shown under the row as well as beside it:\n%s", out)
	}
	m.sel = "p:beta"
	if out := screen(m); !strings.Contains(out, "not running; enter starts it") {
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
	if !m.prefixed || !strings.Contains(screen(m), `prefix ▸ d dashboard`) {
		t.Fatalf("prefix not shown:\n%s", screen(m))
	}
	press(m, "t")
	if _, ok := m.top().(*projectView); !ok || m.prefixed {
		t.Fatalf("prefix t: overlay %T", m.top())
	}
	m.pop()
	m.Update(prefix)
	press(m, "d") // already on the dashboard: nothing
	if m.top() != nil || m.prefixed {
		t.Fatal("prefix d on the dashboard did something")
	}

}

// TestDashboardOver: a popup over a session (prefix then a key in it)
// opens about the session's project, draws over its screen under a
// header naming it, and esc ends the dashboard to attach again; prefix d
// there turns it into the dashboard.
func TestDashboardOver(t *testing.T) {
	src := &fakeSource{data: testData()}
	scr := []string{"first row", "the session's second row", "its third row"}
	m := newDash(DashOptions{Source: src, Width: 100, Height: 30, State: DashState{Current: "beta"},
		Over: &Over{Key: "i", Project: "alpha", Session: "s-5", Title: "s-5 · beta t-0005", Screen: scr}})
	m.setData(src.data)
	if pv, ok := m.top().(*projectView); !ok || pv.slug != "alpha" || pv.tab != tabInbox {
		t.Fatalf("over: overlay %T %+v", m.top(), m.top())
	}
	out := screen(m)
	for _, want := range []string{"tm s-5 · beta t-0005", "the session's second row", "2 inbox"} {
		if !strings.Contains(out, want) {
			t.Errorf("over lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "NEEDS YOU") || strings.Contains(out, "first row") {
		t.Errorf("over shows the list, or the row under the header:\n%s", out)
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if m.result.Attach != "s-5" {
		t.Fatalf("esc over a session: result %+v", m.result)
	}

	// prefix d: the dashboard, with the popup closed.
	m = newDash(DashOptions{Source: src, Width: 100, Height: 30,
		Over: &Over{Key: "?", Session: "s-5", Screen: scr}})
	m.setData(src.data)
	if _, ok := m.top().(*helpView); !ok {
		t.Fatalf("over ?: overlay %T", m.top())
	}
	m.Update(tea.KeyPressMsg{Code: 'b', Mod: tea.ModCtrl})
	m.Update(tea.KeyPressMsg{Code: 'd', Text: "d"})
	if m.over != nil || m.top() != nil || m.result.Attach != "" {
		t.Fatalf("prefix d over a session: over %v top %T result %+v", m.over, m.top(), m.result)
	}
	if !strings.Contains(screen(m), "NEEDS YOU") {
		t.Fatalf("prefix d didn't show the dashboard:\n%s", screen(m))
	}
}

// TestDashboardFooter: the footer lists the ≡ menu, then the keys that
// apply to the selected row.
func TestDashboardFooter(t *testing.T) {
	src := &fakeSource{data: testData()}
	m := newDash(DashOptions{Source: src, Width: 100, Height: 30, State: DashState{Current: "beta"}})
	m.setData(src.data)
	for sel, want := range map[string]string{
		"n:s-1":          "≡ menu · enter attach · a project · t tasks · i inbox · , settings · ? help · prefix+q quit",
		"th:beta:t-0005": "≡ menu · enter attach · a project · t tasks · i inbox",
		"th:beta:t-0006": "≡ menu · a project · t tasks · i inbox",
		"p:beta":         "≡ menu · enter open · a project · t tasks",
	} {
		m.sel = sel
		if got := m.footKeys(); !strings.HasPrefix(got, want) {
			t.Errorf("%s: footer %q, want %q…", sel, got, want)
		}
	}
	m.data.Projects = nil
	m.setData(Data{ServerOK: true})
	if got := m.footKeys(); !strings.HasPrefix(got, "≡ menu · n new project · , settings") {
		t.Errorf("no projects: footer %q", got)
	}
}

// TestDashboardPopups: views draw as boxes over the dimmed list; the
// settings popup has the settings of every project.
func TestDashboardPopups(t *testing.T) {
	home := t.TempDir()
	t.Setenv("TERMINATR_HOME", home)
	src := &fakeSource{data: testData()}
	src.data.Projects[0].Items = []project.Item{{ID: "x", Kind: "report", Summary: "t-0002 handed in report 1"}}
	m := newDash(DashOptions{Source: src, Width: 100, Height: 30})
	m.setData(src.data)
	m.sel = "p:alpha"

	press(m, "i")
	out := screen(m)
	for _, want := range []string{"╭─ alpha ─", "2 inbox", "│ ", "t-0002 handed in report 1", "╰─", "esc close",
		"NEEDS YOU 2"} { // the list stays in view behind the box
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
	for _, want := range []string{"Settings", "Prefix key", "ctrl+b", "Details panel", "List width", "Sidebar"} {
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
	if out := screen(m); !strings.Contains(out, "Its name (becomes a lower-case slug") || !strings.Contains(out, "│ /var/folders") {
		t.Fatalf("prompt:\n%s", out)
	}
}

// TestPopupMargins: beside a popup, over the details panel and the list
// alike, no margin too narrow to read shows cut-off letters (a stray
// "O" of OTHER SESSIONS, "b…" of the details): it is cleared, or the box
// takes the width.
func TestPopupMargins(t *testing.T) {
	for _, w := range []int{60, 100, 140, 180} {
		for _, key := range []string{"?", "a", ","} {
			src := &fakeSource{data: testData()}
			m := newDash(DashOptions{Source: src, Width: w + sideDefault, Height: 40, State: DashState{Current: "beta"}})
			m.setData(src.data)
			m.sel = "th:beta:t-0005"
			keyPress(m, key)
			lines := strings.Split(whole(m), "\n")
			g := m.geo
			if g == nil {
				t.Fatalf("%d %s: no popup", w, key)
			}
			for y := g.y; y < g.y+g.h; y++ {
				r := []rune(lines[y])
				left, right := string(r[:g.x]), string(r[min(g.x+g.w, len(r)):])
				if g.x-m.sideW() < sliver && strings.TrimSpace(left[min(m.sideW(), len(left)):]) != "" || m.winW-g.x-g.w < sliver && strings.TrimSpace(right) != "" {
					t.Fatalf("%d %s: row %d shows %q | %q beside the box:\n%s", w, key, y, left, right, screen(m))
				}
			}
		}
	}
}

// TestDashboardNeedsYouTasks: a project's tasks in review or blocked are
// rows of NEEDS YOU under the project, the sidebar hints at them and the
// count says where to look; enter shows the task in the project popup's
// Tasks tab, and acts on nothing.
func TestDashboardNeedsYouTasks(t *testing.T) {
	d := testData()
	board := &tasks.Board{Tasks: []*tasks.Task{
		{ID: 1, Title: "Write the README", Status: tasks.Started},
		{ID: 3, Title: "Remove the prefix caption", Status: tasks.Review, Thread: "t-0002",
			Steps: []tasks.Step{{N: 1, Text: "Drop it", Done: true}, {N: 2, Text: "Goldens", Done: true}}},
		{ID: 5, Title: "Pick a licence", Status: tasks.Blocked},
	}}
	alpha := &d.Projects[0]
	alpha.NeedsYou = []*tasks.Task{board.Tasks[1], board.Tasks[2]}
	alpha.Counts = map[string]int{"needs_you": 2, "in_motion": 1}
	src := &fakeSource{data: d, board: board}
	m := newDash(DashOptions{Source: src, Width: 120 + sideDefault, Height: 30, State: DashState{Current: "alpha"}})
	m.layout.Details = false
	m.setData(src.data)
	out := screen(m)
	needs := strings.Index(out, "NEEDS YOU 4 ─")
	if needs < 0 {
		t.Fatalf("no NEEDS YOU 4:\n%s", out)
	}
	at := needs
	for _, want := range []string{
		"  alpha         coordinator ",
		"  alpha         T3 Remove the prefix caption              ◆ review    ▰▰▰▰▰  2/2  t-0002",
		"  alpha         T5 Pick a licence                         ▲ blocked",
		"  s-4 ",
		"tasks: 2 need you (t lists them) · 1 in motion · 0 on deck",
	} {
		i := strings.Index(out[at:], want)
		if i < 0 {
			t.Fatalf("missing %q after the previous row in\n%s", want, out)
		}
		at += i
	}
	if side := whole(m); !strings.Contains(side, " ■ alpha                   0 ⚑ │") {
		t.Errorf("no sidebar hint for alpha:\n%s", side)
	}

	m.sel = "nt:alpha:5"
	if foot := m.footKeys(); !strings.Contains(foot, "enter show") {
		t.Errorf("footer %q", foot)
	}
	cmd := press(m, "enter")
	pv, ok := m.top().(*projectView)
	if !ok || pv.slug != "alpha" || pv.tab != tabTasks {
		t.Fatalf("enter opened %T %+v", m.top(), m.top())
	}
	m.Update(cmd()) // the board
	if pv.sel[tabTasks] != 1 || pv.tasks()[1].ID != 5 {
		t.Errorf("selected task %d, want T5", pv.sel[tabTasks])
	}
	if out := screen(m); !strings.Contains(out, "T5     Pick a licence") {
		t.Errorf("Tasks tab:\n%s", out)
	}
	if len(src.opened) != 0 || m.result.Attach != "" {
		t.Errorf("enter on a task acted: opened %v, attach %q", src.opened, m.result.Attach)
	}
}

// An inbox row leads with the kind, then the task, what happened and last
// the title; items of one kind for one thread are one row, counted.
func TestInboxLinesRows(t *testing.T) {
	items := []project.Item{
		{ID: "a", Kind: "report", Subject: "t-0001", Created: time.Now(), Summary: "T1 Fix the login (t-0001) handed in report 1"},
		{ID: "b", Kind: "report", Subject: "t-0001", Created: time.Now(), Summary: "T1 Fix the login (t-0001) handed in report 2"},
	}
	lines, _, hits := inboxLines(items, 0, 80)
	if len(lines) != 1 || len(hits) != 1 {
		t.Fatalf("lines %q hits %v", lines, hits)
	}
	got := ansi.Strip(lines[0])
	if !strings.HasPrefix(got, "report") || !strings.Contains(got, "×2 T1 handed in report 2 Fix the login") {
		t.Errorf("row %q", got)
	}
	if narrow, _, _ := inboxLines(items, -1, 50); strings.Contains(ansi.Strip(narrow[0]), "login") || !strings.Contains(ansi.Strip(narrow[0]), "report 2") {
		t.Errorf("narrow row %q", ansi.Strip(narrow[0]))
	}
}

func TestVersionHint(t *testing.T) {
	own := ServerInfo{Version: version.Version, Build: version.BuildID()}
	if h := versionHint(own); h != "" {
		t.Errorf("same build: %q", h)
	}
	if h := versionHint(ServerInfo{}); h != "" {
		t.Errorf("old server: %q", h)
	}
	newer := own
	newer.Latest, newer.Upgrade = "v99.0.0", "brew upgrade terminatr"
	if h := versionHint(newer); !strings.Contains(h, "v99.0.0") || !strings.Contains(h, "brew upgrade terminatr") {
		t.Errorf("newer release: %q", h)
	}
	other := ServerInfo{Version: "v0.1.0", Build: "v0.1.0+x+y", Latest: "v99.0.0", Upgrade: "tm update"}
	if h := versionHint(other); !strings.Contains(h, "tm server restart") {
		t.Errorf("other build: %q", h)
	}
	if v := serverVersion(other); !strings.Contains(v, "v0.1.0") || !strings.Contains(v, "(tm "+version.Version+")") {
		t.Errorf("header: %q", v)
	}
	if v := serverVersion(own); strings.Contains(v, "(tm") {
		t.Errorf("header for the same version: %q", v)
	}
}
