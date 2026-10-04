package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/theclifmeister/termalator/internal/proto"
	"github.com/theclifmeister/termalator/internal/tasks"
)

// The dashboard (docs/SPEC.md §4): NEEDS YOU across projects, then each
// project with its coordinator and threads, then the other sessions. It
// is a client with no state of its own: it polls the server and the
// project files every second. Attaching leaves the dashboard; the caller
// runs the attach view and opens the dashboard again afterwards.

// refresh is how often the dashboard polls.
const refresh = time.Second

// DashState is what survives an attach: the selected row, the project
// last attached to (for ] and [), and a message for the footer.
type DashState struct {
	Selected string
	Current  string
	Message  string
}

// DashOptions configure Dashboard.
type DashOptions struct {
	Source Source
	In     *os.File
	Out    *os.File
	// Cwd is where s and c start sessions by default.
	Cwd string
	// AgentName labels the c key ("claude").
	AgentName string
	State     DashState
	// Width and Height size the first frame before the terminal reports
	// its size (tests).
	Width, Height int
}

// DashResult says why the dashboard ended: Attach names a session to
// attach to; empty means the user quit.
type DashResult struct {
	Attach string
	State  DashState
}

// Dashboard runs the dashboard until the user quits or picks a session.
func Dashboard(opts DashOptions) (DashResult, error) {
	m := newDash(opts)
	popts := []tea.ProgramOption{tea.WithInput(opts.In), tea.WithOutput(opts.Out)}
	if opts.Width > 0 && opts.Height > 0 {
		popts = append(popts, tea.WithWindowSize(opts.Width, opts.Height))
	}
	final, err := tea.NewProgram(m, popts...).Run()
	if err != nil {
		return DashResult{}, err
	}
	d := final.(*dash)
	d.result.State = DashState{Selected: d.sel, Current: d.current}
	return d.result, nil
}

type mode int

const (
	modeList mode = iota
	modeHelp
	modeInput
	modeTasks
	modeTask
	modeSwitch
)

// row is one dashboard line.
type row struct {
	key     string // selection identity, stable across refreshes
	text    string
	head    bool // a section header
	session string
	project string
	task    *tasks.Task
	confirm bool // a done confirmation: d completes it whatever the status
}

func (r row) selectable() bool { return r.key != "" }

type dash struct {
	src       Source
	cwd       string
	agentName string
	w, h      int

	data    Data
	loaded  bool
	rows    []row
	sel     string // key of the selected row
	current string // project last attached to
	msg     string
	mode    mode
	busy    bool // an action is running

	// input mode
	label, text string
	submit      func(string) tea.Cmd

	// task view
	boardSlug string
	board     *tasks.Board
	taskList  []*tasks.Task
	taskSel   int

	// project switcher
	swSel int

	blocked map[string]bool
	seen    bool // the first poll arrived (no bell for old blocks)

	result DashResult
}

func newDash(o DashOptions) *dash {
	w, h := o.Width, o.Height
	if w <= 0 || h <= 0 {
		w, h = 80, 24
	}
	return &dash{src: o.Source, cwd: o.Cwd, agentName: o.AgentName, w: w, h: h,
		sel: o.State.Selected, current: o.State.Current, msg: o.State.Message, blocked: map[string]bool{}}
}

type dataMsg Data
type tickMsg struct{}
type boardMsg struct {
	slug  string
	board *tasks.Board
	err   error
}

// actionMsg is the outcome of a key's action: attach to a session, or a
// message for the footer.
type actionMsg struct {
	attach  string
	current string
	sel     string
	msg     string
	err     error
}

func (m *dash) load() tea.Cmd {
	src := m.src
	return func() tea.Msg { return dataMsg(src.Load()) }
}

func (m *dash) loadBoard(slug string) tea.Cmd {
	src := m.src
	return func() tea.Msg {
		b, err := src.Board(slug)
		return boardMsg{slug: slug, board: b, err: err}
	}
}

func (m *dash) Init() tea.Cmd { return m.load() }

func (m *dash) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.w, m.h = msg.Width, msg.Height
		return m, nil
	case dataMsg:
		return m, m.setData(Data(msg))
	case tickMsg:
		return m, m.load()
	case boardMsg:
		if msg.slug != m.boardSlug {
			return m, nil
		}
		if msg.err != nil {
			m.msg = msg.err.Error()
			m.mode = modeList
			return m, nil
		}
		m.setBoard(msg.board)
		return m, nil
	case actionMsg:
		m.busy = false
		if msg.err != nil {
			m.msg = msg.err.Error()
			return m, m.load()
		}
		if msg.sel != "" {
			m.sel = msg.sel
		}
		m.msg = msg.msg
		if msg.attach != "" {
			m.result.Attach = msg.attach
			if msg.current != "" {
				m.current = msg.current
			}
			return m, tea.Quit
		}
		cmds := []tea.Cmd{m.load()}
		if m.mode == modeTasks || m.mode == modeTask {
			cmds = append(cmds, m.loadBoard(m.boardSlug))
		}
		return m, tea.Batch(cmds...)
	case tea.KeyPressMsg:
		return m, m.key(msg)
	}
	return m, nil
}

// setData takes a poll's result, rebuilds the rows and rings the bell
// when a session became blocked.
func (m *dash) setData(d Data) tea.Cmd {
	m.data, m.loaded = d, true
	m.rows = buildRows(d)
	if m.selIndex() < 0 {
		m.sel = ""
		for _, r := range m.rows {
			if r.selectable() {
				m.sel = r.key
				break
			}
		}
	}
	ring := false
	now := map[string]bool{}
	for _, s := range d.Sessions {
		b := s.State == "blocked"
		now[s.ID] = b
		if b && !m.blocked[s.ID] && m.seen {
			ring = true
		}
	}
	m.blocked, m.seen = now, true
	next := tea.Tick(refresh, func(time.Time) tea.Msg { return tickMsg{} })
	if ring {
		return tea.Batch(next, tea.Raw("\a"))
	}
	return next
}

func (m *dash) selIndex() int {
	for i, r := range m.rows {
		if r.key != "" && r.key == m.sel {
			return i
		}
	}
	return -1
}

func (m *dash) selected() (row, bool) {
	if i := m.selIndex(); i >= 0 {
		return m.rows[i], true
	}
	return row{}, false
}

func (m *dash) move(d int) {
	i := m.selIndex()
	for j := i + d; j >= 0 && j < len(m.rows); j += d {
		if m.rows[j].selectable() {
			m.sel = m.rows[j].key
			return
		}
	}
}

// paneSize is the size new sessions get: the window less the status bar.
func (m *dash) paneSize() (int, int) { return m.w, max(m.h-1, 1) }

func (m *dash) key(k tea.KeyPressMsg) tea.Cmd {
	key := k.String()
	if key == "ctrl+c" {
		return tea.Quit
	}
	switch m.mode {
	case modeHelp:
		m.mode = modeList
		return nil
	case modeInput:
		return m.inputKey(k)
	case modeTasks, modeTask:
		return m.taskKey(key)
	case modeSwitch:
		return m.switchKey(key)
	}
	if m.busy {
		return nil
	}
	switch key {
	case "q":
		return tea.Quit
	case "up", "k":
		m.move(-1)
	case "down", "j":
		m.move(1)
	case "r":
		return m.load()
	case "?":
		m.mode = modeHelp
	case "enter":
		r, ok := m.selected()
		switch {
		case !ok:
		case r.task != nil:
			m.openTasks(r.project)
			m.mode = modeTask
			m.boardSel(r.task.ID)
			return m.loadBoard(r.project)
		case r.session != "":
			return m.act(func() actionMsg { return actionMsg{attach: r.session, current: r.project} })
		case r.project != "":
			return m.openProject(r.project)
		}
	case "s":
		cols, rows := m.paneSize()
		cwd := m.cwd
		return m.act(func() actionMsg {
			id, err := m.src.StartShell(cwd, cols, rows)
			return actionMsg{attach: id, sel: "s:" + id, err: err}
		})
	case "c":
		m.prompt(m.agentName+" session in directory: ", m.cwd, func(dir string) tea.Cmd {
			dir = expandDir(dir, m.cwd)
			cols, rows := m.paneSize()
			return m.act(func() actionMsg {
				id, err := m.src.StartAgent(dir, cols, rows)
				return actionMsg{attach: id, sel: "s:" + id, err: err}
			})
		})
	case "n":
		m.prompt("new project name: ", "", func(name string) tea.Cmd {
			return m.act(func() actionMsg {
				slug, err := m.src.NewProject(name)
				return actionMsg{sel: "p:" + slug, msg: "created project " + slug + "; enter starts its coordinator", err: err}
			})
		})
	case "t":
		slug := m.projectHere()
		if slug == "" {
			m.msg = "no project; n creates one"
			return nil
		}
		m.openTasks(slug)
		return m.loadBoard(slug)
	case "d":
		r, ok := m.selected()
		if !ok || r.task == nil {
			m.msg = "d marks a task in review done; select one (t shows the tasks)"
			return nil
		}
		return m.markDone(r.project, r.task, r.confirm)
	case "p":
		if len(m.data.Projects) == 0 {
			m.msg = "no projects; n creates one"
			return nil
		}
		m.mode, m.swSel = modeSwitch, 0
		for i, p := range m.data.Projects {
			if p.Slug == m.projectHere() {
				m.swSel = i
			}
		}
	case "]", "[":
		return m.cycleProject(key == "]")
	}
	return nil
}

// act runs an action in the background; one at a time.
func (m *dash) act(fn func() actionMsg) tea.Cmd {
	m.busy = true
	return func() tea.Msg { return fn() }
}

func (m *dash) openProject(slug string) tea.Cmd {
	cols, rows := m.paneSize()
	return m.act(func() actionMsg {
		id, err := m.src.OpenProject(slug, cols, rows)
		return actionMsg{attach: id, current: slug, sel: "p:" + slug, err: err}
	})
}

// projectHere is the project of the selected row, else the current one.
func (m *dash) projectHere() string {
	if r, ok := m.selected(); ok && r.project != "" {
		return r.project
	}
	if m.current != "" {
		return m.current
	}
	if len(m.data.Projects) > 0 {
		return m.data.Projects[0].Slug
	}
	return ""
}

// cycleProject opens the coordinator of the next (or previous) project
// after the current one.
func (m *dash) cycleProject(next bool) tea.Cmd {
	ps := m.data.Projects
	if len(ps) == 0 {
		m.msg = "no projects; n creates one"
		return nil
	}
	i := -1
	cur := m.current
	if cur == "" {
		cur = m.projectHere()
	}
	for j, p := range ps {
		if p.Slug == cur {
			i = j
		}
	}
	switch {
	case i < 0:
		i = 0
	case next:
		i = (i + 1) % len(ps)
	default:
		i = (i - 1 + len(ps)) % len(ps)
	}
	return m.openProject(ps[i].Slug)
}

func (m *dash) markDone(slug string, t *tasks.Task, confirm bool) tea.Cmd {
	if t.Status != tasks.Review && !confirm {
		m.msg = fmt.Sprintf("%s is %s; only a task in review is marked done here", t.Ref(), t.Status)
		return nil
	}
	id, ref := t.ID, t.Ref()
	return m.act(func() actionMsg {
		err := m.src.MarkDone(slug, id)
		return actionMsg{msg: ref + " done", err: err}
	})
}

func (m *dash) prompt(label, initial string, submit func(string) tea.Cmd) {
	m.mode, m.label, m.text, m.submit = modeInput, label, initial, submit
}

func (m *dash) inputKey(k tea.KeyPressMsg) tea.Cmd {
	switch k.String() {
	case "esc":
		m.mode = modeList
	case "enter":
		m.mode = modeList
		if t := strings.TrimSpace(m.text); t != "" {
			return m.submit(t)
		}
	case "backspace":
		if r := []rune(m.text); len(r) > 0 {
			m.text = string(r[:len(r)-1])
		}
	case "ctrl+u":
		m.text = ""
	default:
		if k.Text != "" {
			m.text += oneLine(k.Text)
		}
	}
	return nil
}

func (m *dash) openTasks(slug string) {
	m.mode, m.boardSlug, m.board, m.taskList, m.taskSel = modeTasks, slug, nil, nil, 0
}

// setBoard lists the board's live tasks in board order: needs you, in
// motion, on deck.
func (m *dash) setBoard(b *tasks.Board) {
	var keep int
	if m.taskSel < len(m.taskList) {
		keep = m.taskList[m.taskSel].ID
	}
	m.board, m.taskList = b, nil
	for _, g := range []tasks.Group{tasks.NeedsYou, tasks.InMotion, tasks.OnDeck} {
		for _, t := range b.Tasks {
			if tasks.GroupOf(t.Status) == g {
				m.taskList = append(m.taskList, t)
			}
		}
	}
	m.boardSel(keep)
}

func (m *dash) boardSel(id int) {
	for i, t := range m.taskList {
		if t.ID == id {
			m.taskSel = i
			return
		}
	}
	m.taskSel = min(m.taskSel, max(len(m.taskList)-1, 0))
	if m.board == nil && id != 0 {
		// The board isn't loaded yet: select once it is.
		m.taskList = []*tasks.Task{{ID: id}}
	}
}

func (m *dash) taskKey(key string) tea.Cmd {
	switch key {
	case "esc", "q", "t":
		if m.mode == modeTask {
			m.mode = modeTasks
		} else {
			m.mode = modeList
		}
	case "up", "k":
		m.taskSel = max(m.taskSel-1, 0)
	case "down", "j":
		m.taskSel = min(m.taskSel+1, max(len(m.taskList)-1, 0))
	case "enter":
		if len(m.taskList) > 0 {
			m.mode = modeTask
		}
	case "d":
		if m.board != nil && m.taskSel < len(m.taskList) && !m.busy {
			return m.markDone(m.boardSlug, m.taskList[m.taskSel], false)
		}
	case "r":
		return m.loadBoard(m.boardSlug)
	}
	return nil
}

func (m *dash) switchKey(key string) tea.Cmd {
	switch key {
	case "esc", "q", "p":
		m.mode = modeList
	case "up", "k":
		m.swSel = max(m.swSel-1, 0)
	case "down", "j":
		m.swSel = min(m.swSel+1, max(len(m.data.Projects)-1, 0))
	case "enter":
		if m.swSel < len(m.data.Projects) && !m.busy {
			m.mode = modeList
			return m.openProject(m.data.Projects[m.swSel].Slug)
		}
	}
	return nil
}

// expandDir resolves ~ and relative paths against cwd.
func expandDir(dir, cwd string) string {
	if dir == "~" || strings.HasPrefix(dir, "~/") {
		if h, err := os.UserHomeDir(); err == nil {
			dir = filepath.Join(h, dir[1:])
		}
	}
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(cwd, dir)
	}
	return filepath.Clean(dir)
}

// Rows.

const (
	colWho   = 12 // project slug, or session id
	colWhat  = 30 // coordinator, thread, task, command
	colState = 9
)

func cols(prefix, who, what, state, rest string) string {
	return prefix + fit(who, colWho) + " " + fit(what, colWhat) + " " + fit(state, colState) + " " + rest
}

// buildRows lays out NEEDS YOU, PROJECTS and SESSIONS.
func buildRows(d Data) []row {
	var needs, projs, other []row
	bySlug := map[string]bool{}
	for _, p := range d.Projects {
		bySlug[p.Slug] = true
	}
	now := time.Now()
	for _, s := range d.Sessions {
		if s.State == "blocked" {
			who := s.Project
			if who == "" {
				who = s.ID
			}
			needs = append(needs, row{key: "n:" + s.ID, session: s.ID, project: s.Project,
				text: cols(" ! ", who, sessionName(s), "blocked", progress(s))})
		}
	}
	for _, p := range d.Projects {
		for _, t := range p.NeedsYou {
			rest := ""
			if t.Thread != "" {
				rest = "← " + t.Thread
			}
			needs = append(needs, row{key: fmt.Sprintf("t:%s:%d", p.Slug, t.ID), project: p.Slug, task: t,
				text: cols(" ? ", p.Slug, t.Ref()+" "+oneLine(t.Title), string(t.Status), rest)})
		}
		for _, it := range p.Inbox {
			r := row{key: "i:" + p.Slug + ":" + it.ID, project: p.Slug,
				text: cols(" ? ", p.Slug, oneLine(it.Summary), "inbox", it.Kind)}
			if it.Task != nil {
				r.task, r.confirm = it.Task, true
				r.text = cols(" ? ", p.Slug, it.Task.Ref()+" "+oneLine(it.Task.Title), "confirm", "d marks it done · "+oneLine(it.Summary))
			}
			needs = append(needs, r)
		}

		var coord *proto.SessionInfo
		var members []proto.SessionInfo
		for i, s := range d.Sessions {
			if s.Project != p.Slug {
				continue
			}
			if s.Role == proto.RoleCoordinator && coord == nil {
				coord = &d.Sessions[i]
				continue
			}
			members = append(members, s)
		}
		state, rest := "—", "enter starts the coordinator"
		if coord != nil {
			state, rest = stateWord(*coord), progress(*coord)
		}
		if p.Err != "" {
			rest = "error: " + oneLine(p.Err)
		}
		if p.Unread > 0 {
			rest = strings.TrimSpace(rest + fmt.Sprintf("  %d inbox", p.Unread))
		}
		r := row{key: "p:" + p.Slug, project: p.Slug, text: cols("  ", p.Slug, "coordinator", state, rest)}
		if coord != nil {
			r.session = coord.ID
		}
		projs = append(projs, r)
		sort.SliceStable(members, func(i, j int) bool { return members[i].Thread < members[j].Thread })
		for _, s := range members {
			projs = append(projs, row{key: "s:" + s.ID, session: s.ID, project: p.Slug,
				text: cols("    ", s.ID, sessionName(s), stateWord(s), joinSp(progress(s), age(now.Sub(s.Created))))})
		}
		c := p.Counts
		projs = append(projs, row{text: fmt.Sprintf("    tasks: %d needs you · %d in motion · %d on deck", c["needs_you"], c["in_motion"], c["on_deck"])})
	}
	home, _ := os.UserHomeDir()
	for _, s := range d.Sessions {
		if s.Project != "" && bySlug[s.Project] {
			continue
		}
		where := s.Cwd
		if home != "" && strings.HasPrefix(where, home) {
			where = "~" + where[len(home):]
		}
		other = append(other, row{key: "s:" + s.ID, session: s.ID,
			text: cols("  ", s.ID, sessionName(s), stateWord(s), joinSp(progress(s), age(now.Sub(s.Created)), where))})
	}

	var rows []row
	if len(needs) > 0 {
		rows = append(rows, row{head: true, text: "NEEDS YOU"})
		rows = append(rows, needs...)
	}
	rows = append(rows, row{head: true, text: "PROJECTS"})
	if len(projs) == 0 {
		projs = []row{{text: "  no projects; n creates one"}}
	}
	rows = append(rows, projs...)
	rows = append(rows, row{head: true, text: "SESSIONS"})
	if len(other) == 0 {
		other = []row{{text: "  no sessions; s starts a shell, c an agent"}}
	}
	return append(rows, other...)
}

func joinSp(parts ...string) string {
	var out []string
	for _, p := range parts {
		if p != "" {
			out = append(out, p)
		}
	}
	return strings.Join(out, "  ")
}

// View.

var (
	styleHead  = lipgloss.NewStyle().Bold(true)
	styleSel   = lipgloss.NewStyle().Reverse(true)
	styleFaint = lipgloss.NewStyle().Faint(true)
)

// rule is a section header drawn across the width: "NEEDS YOU ───…".
func (m *dash) rule(title string) string {
	if title == "" {
		return styleFaint.Render(strings.Repeat("─", m.w))
	}
	t := " " + title + " "
	return styleHead.Render(t) + styleFaint.Render(strings.Repeat("─", max(m.w-len([]rune(t)), 0)))
}

func (m *dash) View() tea.View {
	v := tea.NewView(m.render())
	v.AltScreen = true
	v.WindowTitle = "termalator"
	return v
}

func (m *dash) render() string {
	switch m.mode {
	case modeHelp:
		return m.frame("help", helpLines(m.agentName), -1, "any key returns")
	case modeTasks, modeTask:
		return m.renderTasks()
	case modeSwitch:
		return m.renderSwitch()
	}
	var lines []string
	sel := -1
	for _, r := range m.rows {
		switch {
		case r.head:
			lines = append(lines, m.rule(r.text))
		case r.key != "" && r.key == m.sel:
			sel = len(lines)
			lines = append(lines, styleSel.Render(fit(r.text, m.w)))
		case r.key == "":
			lines = append(lines, styleFaint.Render(fit(r.text, m.w)))
		default:
			lines = append(lines, fit(r.text, m.w))
		}
	}
	keys := "enter attach · s shell · c " + m.agentName + " · n project · t tasks · d done · p projects · ? help · q quit"
	return m.frame("", lines, sel, keys)
}

// frame draws the header, the body scrolled so line sel shows, and the
// footer: a rule, the keys or the input prompt, and the message.
func (m *dash) frame(title string, body []string, sel int, keys string) string {
	right := "server ok"
	switch {
	case !m.loaded:
		right = "loading…"
	case !m.data.ServerOK:
		right = "server down: " + oneLine(m.data.Err)
	default:
		right += fmt.Sprintf(" · %d session%s", len(m.data.Sessions), map[bool]string{true: "s"}[len(m.data.Sessions) != 1])
	}
	left := " termalator"
	if title != "" {
		left += " · " + title
	}
	head := fit(left, max(m.w-len([]rune(right))-1, 1)) + " " + right
	foot := []string{m.rule("")}
	if m.mode == modeInput {
		foot = append(foot, fit(" "+m.label+m.text+"█", m.w))
	} else {
		foot = append(foot, styleFaint.Render(fit(" "+keys, m.w)))
	}
	msg := m.msg
	if m.busy {
		msg = "working…"
	}
	foot = append(foot, fit(" "+oneLine(msg), m.w))

	room := max(m.h-1-len(foot), 1)
	top := 0
	if sel >= room {
		top = sel - room + 1
	}
	if top > 0 && top+room > len(body) {
		top = max(len(body)-room, 0)
	}
	end := min(top+room, len(body))
	out := []string{styleHead.Render(head)}
	out = append(out, body[top:end]...)
	for i := end - top; i < room; i++ {
		out = append(out, "")
	}
	out = append(out, foot...)
	return strings.Join(out, "\n")
}

func (m *dash) renderTasks() string {
	if m.board == nil {
		return m.frame(m.boardSlug+" tasks", []string{" loading…"}, -1, "esc back")
	}
	if m.mode == modeTask && m.taskSel < len(m.taskList) {
		return m.renderTask(m.taskList[m.taskSel])
	}
	var lines []string
	sel := -1
	var group tasks.Group
	for i, t := range m.taskList {
		if g := tasks.GroupOf(t.Status); g != group {
			group = g
			lines = append(lines, m.rule(strings.ToUpper(string(g))))
		}
		steps := ""
		if len(t.Steps) > 0 {
			steps = fmt.Sprintf("%d/%d", t.StepsDone(), len(t.Steps))
		}
		text := cols("  ", t.Ref(), oneLine(t.Title), string(t.Status), joinSp(steps, t.Thread))
		if i == m.taskSel {
			sel = len(lines)
			text = styleSel.Render(fit(text, m.w))
		} else {
			text = fit(text, m.w)
		}
		lines = append(lines, text)
	}
	done := 0
	for _, t := range m.board.Tasks {
		if t.Status == tasks.Done {
			done++
		}
	}
	if len(m.taskList) == 0 {
		lines = append(lines, " no open tasks")
	}
	lines = append(lines, styleFaint.Render(fmt.Sprintf("  done: %d", done)))
	return m.frame(m.boardSlug+" tasks", lines, sel, "enter show · d mark done (review) · r refresh · esc back")
}

func (m *dash) renderTask(t *tasks.Task) string {
	lines := []string{
		fit(" "+t.Ref()+" "+oneLine(t.Title), m.w),
		fit(" status: "+string(t.Status)+map[bool]string{true: " · thread " + t.Thread, false: ""}[t.Thread != ""], m.w),
		"",
	}
	for _, l := range strings.Split(strings.TrimSpace(t.Notes), "\n") {
		if l != "" {
			lines = append(lines, fit(" "+oneLine(l), m.w))
		}
	}
	if len(t.Steps) > 0 {
		lines = append(lines, "", " steps:")
		for _, s := range t.Steps {
			box := "[ ]"
			if s.Done {
				box = "[x]"
			}
			lines = append(lines, fit(fmt.Sprintf("  %s %d %s", box, s.N, oneLine(s.Text)), m.w))
		}
	}
	return m.frame(m.boardSlug+" "+t.Ref(), lines, -1, "d mark done (review) · esc back")
}

func (m *dash) renderSwitch() string {
	var lines []string
	for i, p := range m.data.Projects {
		state := "no coordinator"
		for _, s := range m.data.Sessions {
			if s.Role == proto.RoleCoordinator && s.Project == p.Slug {
				state = joinSp(stateWord(s), progress(s))
				break
			}
		}
		text := cols("  ", p.Slug, oneLine(p.Name), "", state)
		if p.Slug == m.current {
			text = cols("* ", p.Slug, oneLine(p.Name), "", state)
		}
		if i == m.swSel {
			text = styleSel.Render(fit(text, m.w))
		} else {
			text = fit(text, m.w)
		}
		lines = append(lines, text)
	}
	return m.frame("projects", lines, m.swSel, "enter open its coordinator · esc back")
}

func helpLines(agentName string) []string {
	return []string{
		" enter     attach to the selected session; on a project, open its coordinator",
		" s         new shell session (in the directory tm was started in)",
		" c         new " + agentName + " session in a directory you choose",
		" n         new project",
		" t         the project's tasks; enter shows one, d marks a task in review done",
		" d         mark the selected task done (tasks in review)",
		" p         project switcher; enter opens that project's coordinator",
		" ] [       next / previous project's coordinator",
		" r         refresh       ? help       q quit (the server keeps running)",
		"",
		" While attached: " + DefaultDetachKey + " returns here. " + DefaultDetachKey + " then p, ] or [",
		" switches project straight from a session.",
	}
}
