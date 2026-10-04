package tui

import (
	"cmp"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/theclifmeister/termalator/internal/tasks"
)

// The dashboard (docs/SPEC.md §4): NEEDS YOU across projects, then each
// project with its coordinator and threads, then the other sessions. It
// is a client with no state of its own: it polls the server and the
// project files every second. Attaching leaves the dashboard; the caller
// runs the attach view and opens the dashboard again afterwards.
//
// The list's rows are built in rows.go, its keys are the actions in
// actions.go, and the views opened on top of it are the overlays in
// overlays.go.

// refresh is how often the dashboard polls.
const refresh = time.Second

// DashState is what survives an attach: the selected row, the project
// last attached to (for ] and [), and a message for the footer.
type DashState struct {
	Selected string
	Current  string
	Message  string
	// Then is a key to run once the first poll is in: the key typed
	// after the prefix in a session (p, ], [, i, t, , or ?).
	Then string
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
	// UIFile is ui.json, where the layout is kept; empty keeps it in
	// memory only (tests).
	UIFile string
	// Prefix is the prefix key ("ctrl+\\"); empty is the default.
	Prefix string
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
	errMsg  string    // msg when it reports a failure, drawn as one
	busy    bool      // an action is running
	stack   []overlay // views open on top of the list, topmost last

	layout   Layout
	uiFile   string
	dragging bool // the mouse is moving the divider

	prefix   string // the prefix key, as tea names it
	prefixed bool   // the prefix was typed: the next key is a command
	then     string // a key to run after the first poll

	alerts uint64
	seen   bool // the first poll arrived (no bell for old alerts)

	result DashResult
}

func newDash(o DashOptions) *dash {
	w, h := o.Width, o.Height
	if w <= 0 || h <= 0 {
		w, h = 80, 24
	}
	return &dash{src: o.Source, cwd: o.Cwd, agentName: o.AgentName, w: w, h: h,
		sel: o.State.Selected, current: o.State.Current, msg: o.State.Message,
		layout: LoadLayout(o.UIFile), uiFile: o.UIFile,
		prefix: cmp.Or(o.Prefix, DefaultPrefixKey), then: o.State.Then}
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
		b := m.boardView()
		if b == nil || msg.slug != b.slug {
			return m, nil
		}
		if msg.err != nil {
			m.fail(msg.err)
			m.close(b)
			return m, nil
		}
		b.setBoard(msg.board)
		return m, nil
	case actionMsg:
		m.busy = false
		if msg.err != nil {
			m.fail(msg.err)
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
		if b := m.boardView(); b != nil {
			cmds = append(cmds, m.loadBoard(b.slug))
		}
		return m, tea.Batch(cmds...)
	case tea.KeyPressMsg:
		return m, m.key(msg)
	case editedMsg:
		if msg.err != nil {
			m.fail(fmt.Errorf("editor: %w", msg.err))
		}
		if p, err := prefixKey(); err == nil {
			m.prefix = p.String()
		}
		if sv, ok := m.top().(*settingsView); ok {
			sv.load(m)
		}
	case tea.MouseClickMsg:
		m.click(msg.Mouse())
	case tea.MouseMotionMsg:
		if m.dragging {
			m.layout.Split = clampSplit(float64(msg.Mouse().X) / float64(max(m.w, 1)))
		}
	case tea.MouseReleaseMsg:
		if m.dragging {
			m.dragging = false
			m.saveLayout()
		}
	case tea.MouseWheelMsg:
		if m.top() == nil {
			switch msg.Mouse().Button {
			case tea.MouseWheelUp:
				m.move(-1)
			case tea.MouseWheelDown:
				m.move(1)
			}
		}
	}
	return m, nil
}

// click selects the row under the mouse, or starts dragging the divider.
func (m *dash) click(mo tea.Mouse) {
	if m.top() != nil || mo.Button != tea.MouseLeft {
		return
	}
	split, lw := m.split()
	if split && mo.X == lw {
		m.dragging = true
		return
	}
	if split && mo.X > lw {
		return
	}
	_, keys, sel := m.listLines(lw, !split)
	i := scrollTop(sel, m.bodyRows(), len(keys)) + mo.Y - 1
	if mo.Y >= 1 && mo.Y <= m.bodyRows() && i < len(keys) && keys[i] != "" {
		m.sel = keys[i]
	}
}

// setData takes a poll's result, rebuilds the rows and rings the bell
// when the server sent a notification (a session blocked, a thread
// reported) since the last poll.
func (m *dash) setData(d Data) tea.Cmd {
	first := !m.loaded
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
	if d.ServerOK {
		ring = m.seen && d.Alerts > m.alerts
		m.alerts, m.seen = d.Alerts, true
	}
	cmds := []tea.Cmd{tea.Tick(refresh, func(time.Time) tea.Msg { return tickMsg{} })}
	if ring {
		cmds = append(cmds, tea.Raw("\a"))
	}
	if first && m.then != "" {
		// The key typed after the prefix in a session, now that the
		// projects it may need are known.
		cmds = append(cmds, m.listKey(m.then))
		m.then = ""
	}
	if len(cmds) == 1 {
		return cmds[0]
	}
	return tea.Batch(cmds...)
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

// key sends a key to the topmost overlay, else to the list's actions.
// The prefix works here as in a session, so the same keys do the same
// things: prefix then a key is that key, and prefix d is a no-op.
func (m *dash) key(k tea.KeyPressMsg) tea.Cmd {
	if k.String() == "ctrl+c" {
		return tea.Quit
	}
	if m.prefixed {
		m.prefixed = false
		if k.String() == "d" || k.String() == m.prefix || k.String() == "esc" {
			return nil
		}
	} else if k.String() == m.prefix {
		m.prefixed = true
		return nil
	}
	if o := m.top(); o != nil {
		return o.key(m, k)
	}
	if m.busy {
		return nil
	}
	return m.listKey(k.String())
}

// fail shows err in the footer, as a failure.
func (m *dash) fail(err error) { m.msg = err.Error(); m.errMsg = m.msg }

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

// View.

// rule is a section header drawn across the width: "NEEDS YOU ───…".
func (m *dash) rule(title string) string { return m.ruleIn(title, styleTitle, m.w) }

// ruleIn is a rule w cells wide with the title in st.
func (m *dash) ruleIn(title string, st lipgloss.Style, w int) string {
	if title == "" {
		return styleFaint.Render(strings.Repeat("─", w))
	}
	t := " " + title + " "
	return st.Render(t) + styleFaint.Render(strings.Repeat("─", max(w-len([]rune(t)), 0)))
}

func (m *dash) View() tea.View {
	v := tea.NewView(m.render())
	v.AltScreen = true
	v.MouseMode = tea.MouseModeCellMotion
	v.WindowTitle = "termalator"
	return v
}

func (m *dash) render() string {
	if o := m.top(); o != nil {
		return o.render(m)
	}
	return m.renderList()
}

func (m *dash) renderList() string { return m.frame("", m.listBody(), -1, m.footKeys()) }

// listBody is the list (and the details panel beside it) as the body's
// rows, scrolled so the selected row shows.
func (m *dash) listBody() []string {
	split, lw := m.split()
	lines, _, sel := m.listLines(lw, !split)
	room := m.bodyRows()
	top := scrollTop(sel, room, len(lines))
	if !split {
		body := make([]string, room)
		copy(body, lines[min(top, len(lines)):])
		return body
	}
	// The list scrolls on its own; the details panel shows the selected
	// row from the top.
	var right []string
	if r, ok := m.selected(); ok {
		right = m.details(r, m.w-lw-1)
	}
	body := make([]string, room)
	for i := range body {
		var l, d string
		if top+i < len(lines) {
			l = lines[top+i]
		}
		if i < len(right) {
			d = right[i]
		}
		body[i] = fit(l, lw) + reset + styleFaint.Render("│") + fit(d, m.w-lw-1) + reset
	}
	return body
}

// listLines lays out the list w cells wide: its lines, the row key on
// each (for the mouse) and the selected line. With inline set, a
// selected thread's details show under its row.
func (m *dash) listLines(w int, inline bool) (lines, keys []string, sel int) {
	sel = -1
	add := func(l, key string) {
		lines = append(lines, l)
		keys = append(keys, key)
	}
	for _, r := range m.rows {
		switch {
		case r.head == "NEEDS YOU":
			add(m.ruleIn(countLabel(r.head, r.count), styleWarn.Bold(true), w), "")
		case r.head != "":
			add(m.ruleIn(countLabel(r.head, r.count), styleTitle, w), "")
		case r.key != "" && r.key == m.sel:
			sel = len(lines)
			add(styleSel.Render(fit(r.text(w), w)), r.key)
			if inline && r.thread != nil && strings.HasPrefix(r.key, "th:") {
				for _, l := range threadDetail(r.thread, "        ") {
					add(fit(l, w)+reset, r.key)
				}
			}
		default:
			add(r.styled(w), r.key)
		}
	}
	return lines, keys, sel
}

// footRows is the footer's height: a rule, the keys, the message.
const footRows = 3

// bodyRows is the room between the header and the footer.
func (m *dash) bodyRows() int { return max(m.h-1-footRows, 1) }

// scrollTop is the first of n lines to show in room rows so that line
// sel shows.
func scrollTop(sel, room, n int) int {
	top := 0
	if sel >= room {
		top = sel - room + 1
	}
	if top > 0 && top+room > n {
		top = max(n-room, 0)
	}
	return top
}

// frame draws the header, the body scrolled so line sel shows, and the
// footer: a rule, the keys or the input prompt, and the message.
func (m *dash) frame(title string, body []string, sel int, keys string) string {
	var right string
	switch {
	case !m.loaded:
		right = styleFaint.Render("◌ loading…")
	case !m.data.ServerOK:
		right = styleBad.Render("▲ server down: " + oneLine(m.data.Err))
	default:
		n := len(m.data.Sessions)
		right = styleGood.Render("●") + " server ok" + styleFaint.Render(fmt.Sprintf(" · %d session%s", n, map[bool]string{true: "s"}[n != 1]))
	}
	left := styleTitle.Render(" termalator")
	if title != "" {
		left += styleFaint.Render(" · ") + styleHead.Render(title)
	}
	head := fit(left, max(m.w-ansi.StringWidth(right)-1, 1)) + reset + " " + right
	foot := []string{m.rule("")}
	if m.prefixed {
		keys = m.prefix + " ▸ any dashboard key · d or esc cancels"
	}
	foot = append(foot, fit(" "+keysLine(keys), m.w)+reset)
	msg := " " + oneLine(m.msg)
	switch {
	case m.busy:
		msg = styleFaint.Render(" working…")
	case m.msg != "" && m.msg == m.errMsg:
		msg = styleBad.Render(msg)
	}
	foot = append(foot, fit(msg, m.w)+reset)

	room := m.bodyRows()
	top := scrollTop(sel, room, len(body))
	end := min(top+room, len(body))
	out := []string{head}
	out = append(out, body[top:end]...)
	for i := end - top; i < room; i++ {
		out = append(out, "")
	}
	out = append(out, foot...)
	return strings.Join(out, "\n")
}
