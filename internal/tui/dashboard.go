package tui

import (
	"cmp"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/theclifmeister/terminatr/internal/project"
	"github.com/theclifmeister/terminatr/internal/proto"
	"github.com/theclifmeister/terminatr/internal/tasks"
	"github.com/theclifmeister/terminatr/internal/view"
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

// refresh is how often the dashboard polls; an open task list reloads
// every boardRefresh (it asks git where reviews stand), so no key
// refreshes.
const (
	refresh      = time.Second
	boardRefresh = 5 * time.Second
)

// DashState is what survives an attach: the selected row, the project
// last attached to (for ] and [), and a message for the footer.
type DashState struct {
	Selected string
	Current  string
	Message  string
	// Then is a key to run once the first poll is in: the key typed
	// after the prefix in a session (p, ] or [).
	Then string
	// SideFocus starts the dashboard with the keyboard in the sidebar,
	// where the session left it.
	SideFocus bool
}

// DashOptions configure Dashboard.
type DashOptions struct {
	Source Source
	In     *os.File
	Out    *os.File
	// Cwd is where s starts shells.
	Cwd   string
	State DashState
	// Width and Height size the first frame before the terminal reports
	// its size (tests).
	Width, Height int
	// UIFile is ui.json, where the layout is kept; empty keeps it in
	// memory only (tests).
	UIFile string
	// Prefix is the prefix key ("ctrl+b"); empty is the default.
	Prefix string
	// View is the server-owned view the dashboard is a screen of
	// (docs/SPEC.md §3.3): its selection, current project and sidebar are
	// the view's, and the dashboard ends when the view shows sessions,
	// from this console or another. Nil keeps them here (tests).
	View *ViewConn
	// Over runs the dashboard as a popup over a session instead: see
	// Over.
	Over *Over
}

// Over is a popup opened over a session (docs/SPEC.md §4): prefix then
// a, i, t, , or ? in a session. The dashboard runs its key and draws the
// popup over the session's screen, dimmed, rather than over the list,
// and follows the session's output meanwhile (paneFeed); the view stays
// as it is. When the popup closes the dashboard ends with
// Attach set, and the caller attaches again. Should the view go back to
// its dashboard meanwhile (another console, prefix then d), it carries on
// as the dashboard.
type Over struct {
	Key     string   // the dashboard key that opens the popup
	Task    int      // with Key t: the task the task view shows, 0 for the list
	Project string   // the project it is about: the session's, or a sidebar row's
	Session string   // the session it is drawn over
	Title   string   // how the header names the session
	Screen  []string // the session's area as shown, plain, a line per row
	// Where the pane is in Screen: after X columns of padding, at rows
	// Y to Y+H, from its row Top. The session's output goes there.
	X, Y, H, Top int
}

// DashResult says why the dashboard ended: Attach names a session to
// attach to (with a View: the view shows sessions now); empty means the
// user quit.
type DashResult struct {
	Attach string
	State  DashState
	// Message is the footer's message when a popup over a session closed
	// (Over), for the session's status bar: a key that couldn't open its
	// popup says why there.
	Message string
	// SideFocus: the sidebar has the keyboard, and keeps it in the
	// session attached (a click on a sidebar row attached it, say).
	SideFocus bool
	// Command is a prefix command for the session to run once attached
	// again: prefix then r or tab over it, which are the session's own.
	Command string
}

// Dashboard runs the dashboard until the user quits or picks a session.
func Dashboard(opts DashOptions) (DashResult, error) {
	loadIcons()
	m := newDash(opts)
	defer close(m.done)
	if o := opts.Over; o != nil && o.Session != "" && opts.View != nil {
		// The session keeps drawing under the popup.
		if f, err := followPane(opts.View.paths, o.Session); err == nil {
			m.feed = f
			defer func() {
				if m.feed != nil {
					m.feed.close()
				}
			}()
		}
	}
	if m.stopWatch != nil {
		defer m.stopWatch()
	}
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
	d.result.SideFocus = d.result.Attach != "" && d.focus == areaSide
	return d.result, nil
}

type dash struct {
	src  Source
	cwd  string
	w, h int // the dashboard's own area: the window less the sidebar
	winW int // the window's width

	data    Data
	loaded  bool
	rows    []row
	sel     string // key of the selected row
	current string // the current project: the one listed (listProject)
	// focus is the area with this console's keyboard: the list, the
	// sidebar or the details panel (tab). sideSel is the sidebar's
	// keyboard row, the view's. sideSent is the last one sent; one is
	// on its way at a time (sideBusy), so they arrive in order, and the
	// view's own doesn't overwrite this console's meanwhile.
	focus    area
	sideSel  string
	sideSent string
	sideBusy bool
	msg      string
	errMsg   string    // msg when it reports a failure, drawn as one
	busy     bool      // an action is running
	stack    []overlay // views open on top of the list, topmost last
	geo      *boxGeo   // where the topmost was drawn, for the mouse
	// lastClick is the last left click, for double-clicks; detailTop is
	// the details panel's first line, scrolled with the wheel, for the
	// row detailKey (another row shows from the top).
	lastClick click
	detailTop int
	detailKey string
	shownKeys string // the footer's hints as drawn: its buttons

	layout   Layout
	uiFile   string
	dragging bool // the mouse is moving the divider
	sideDrag bool // the mouse is moving the sidebar's border

	prefix   string    // the prefix key, as tea names it
	prefixed bool      // the prefix was typed: the next key is a command
	quitting bool      // prefix+q: this console quits, popup or not
	then     string    // a key to run after the first poll
	boardAt  time.Time // when the open task list last reloaded on its own

	alerts uint64
	seen   bool // the first poll arrived (no bell for old alerts)

	// The view (nil without one). viewSeq is its version when the
	// dashboard opened; selPending counts this console's selections on
	// their way, which the view's own doesn't overwrite meanwhile.
	view       *ViewConn
	viewSeq    uint64
	viewSide   SidebarLayout // the view's sidebar, as last sent or seen
	watch      <-chan struct{}
	stopWatch  func()
	selPending int
	userSel    bool // the user moved the selection: tell the view
	sized      bool // the first window size came
	done       chan struct{}

	// over: a popup over a session (Over), nil for the dashboard; feed
	// follows the session under it, nil for none. leaving: a popup action
	// asked the view to show a session.
	over    *Over
	feed    *paneFeed
	leaving bool

	result DashResult
}

func newDash(o DashOptions) *dash {
	w, h := o.Width, o.Height
	if w <= 0 || h <= 0 {
		w, h = 80, 24
	}
	m := &dash{src: o.Source, cwd: o.Cwd, h: h,
		sel: o.State.Selected, current: o.State.Current, msg: o.State.Message,
		layout: LoadLayout(o.UIFile), uiFile: o.UIFile,
		prefix: cmp.Or(o.Prefix, DefaultPrefixKey), then: o.State.Then, done: make(chan struct{}), over: o.Over}
	if o.Over != nil {
		m.then = o.Over.Key
	}
	if o.State.SideFocus {
		m.focus = areaSide
	}
	if vc := o.View; vc != nil {
		v := vc.View()
		m.view, m.viewSeq = vc, v.Seq
		m.sel = cmp.Or(v.Selected, m.sel)
		m.current = v.Current
		m.sideSel, m.sideSent = v.SideSel, v.SideSel
		m.layout.Sidebar, m.viewSide = v.Sidebar, v.Sidebar
		m.layout.Info = v.Info
		m.watch, m.stopWatch = vc.Watch()
	}
	m.setWidth(w)
	return m
}

// setWidth takes the window's width; the dashboard gets what the sidebar
// leaves.
func (m *dash) setWidth(w int) {
	m.winW = w
	m.w = max(w-m.sideW(), 1)
}

// sideW is the sidebar's width in this window.
func (m *dash) sideW() int { return m.layout.Sidebar.Cols(m.winW) }

type dataMsg Data
type tickMsg struct{}

// viewMsg: the view has a new version. viewDoneMsg: a call on it
// answered.
type viewMsg struct{}
type viewDoneMsg struct {
	sel  bool // a selection
	side bool // a move of the sidebar's keyboard row
	err  error
}
type boardMsg struct {
	slug  string
	board *tasks.Board
	// reviews are the board's tasks in review, by id.
	reviews map[int]Review
	err     error
	// memory is the project's memory, for the project popup's Memory
	// tab (loadPopup), nil when not loaded; memErr is why it failed.
	memory *project.Memory
	memErr error
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
		msg := boardMsg{slug: slug, board: b, err: err, reviews: map[int]Review{}}
		if b != nil {
			for _, t := range b.Tasks {
				if t.Status == tasks.Review {
					msg.reviews[t.ID] = src.Review(slug, t)
				}
			}
		}
		return msg
	}
}

// loadPopup loads what slug's project popup shows from the project
// folder: its board, as loadBoard, and its memory.
func (m *dash) loadPopup(slug string) tea.Cmd {
	board, src := m.loadBoard(slug), m.src
	return func() tea.Msg {
		msg := board().(boardMsg)
		mem, err := src.Memory(slug)
		msg.memory, msg.memErr = &mem, err
		return msg
	}
}

// reloadBoards reloads the open task lists, every boardRefresh.
func (m *dash) reloadBoards() tea.Cmd {
	if time.Since(m.boardAt) < boardRefresh {
		return nil
	}
	var cmds []tea.Cmd
	if b := m.boardView(); b != nil {
		cmds = append(cmds, m.loadBoard(b.slug))
	}
	if pv := m.projectPopupView(); pv != nil {
		cmds = append(cmds, m.loadPopup(pv.slug))
	}
	if len(cmds) > 0 {
		m.boardAt = time.Now()
	}
	return tea.Batch(cmds...)
}

func (m *dash) Init() tea.Cmd { return tea.Batch(m.load(), m.waitView(), m.waitFeed()) }

// feedMsg: the session under the popup drew something.
type feedMsg struct{}

// waitFeed waits for the session under the popup to change.
func (m *dash) waitFeed() tea.Cmd {
	f, done := m.feed, m.done
	if f == nil {
		return nil
	}
	return func() tea.Msg {
		select {
		case <-f.wake:
		case <-f.end:
		case <-done:
			return nil
		}
		time.Sleep(feedInterval) // a burst of output is one redraw
		return feedMsg{}
	}
}

// fromFeed draws the session's screen as it is now under the popup.
func (m *dash) fromFeed() tea.Cmd {
	if m.over == nil || m.feed == nil {
		return nil
	}
	if rows := m.feed.screen(); rows != nil {
		m.over.Screen = m.over.place(rows, len(m.over.Screen))
	}
	select {
	case <-m.feed.end:
		return nil // the last of it
	default:
		return m.waitFeed()
	}
}

// dropOver turns the popup over a session into the dashboard.
func (m *dash) dropOver() {
	if m.feed != nil {
		m.feed.close()
	}
	m.over, m.feed = nil, nil
}

// waitView waits for the view's next version.
func (m *dash) waitView() tea.Cmd {
	if m.view == nil {
		return nil
	}
	watch, done := m.watch, m.done
	return func() tea.Msg {
		select {
		case <-watch:
			return viewMsg{}
		case <-done:
			return nil
		}
	}
}

// call runs a view action in the background.
func (m *dash) call(method string, p proto.ViewParams) tea.Cmd {
	vc, sel, side := m.view, method == proto.MethodViewSelect, method == proto.MethodViewSideSel
	if sel {
		m.selPending++
	}
	if side {
		m.sideBusy, m.sideSent = true, p.Key
	}
	return func() tea.Msg {
		_, err := vc.Do(method, p)
		return viewDoneMsg{sel: sel, side: side, err: err}
	}
}

// fromView takes the view's new version: its selection (unless one of
// this console's is on its way), current project and sidebar. When it
// shows sessions now, from here or another console, the dashboard ends.
func (m *dash) fromView() tea.Cmd {
	v := m.view.View()
	if m.over != nil {
		switch {
		case v.Mode != view.ModeLayout:
			// Back to its dashboard, from another console: carry on as it.
			m.dropOver()
		case v.Focus != "" && v.Focus != m.over.Session:
			m.result.Attach = v.Focus
			return tea.Quit
		}
	} else if v.Mode == view.ModeLayout && v.Focus != "" && v.Seq != m.viewSeq {
		m.result.Attach = cmp.Or(v.Focus, "view")
		return tea.Quit
	}
	if m.selPending == 0 && v.Selected != "" {
		m.sel = v.Selected
	}
	if m.current != v.Current {
		m.current = v.Current
		m.rebuild()
	}
	if !m.sideBusy && m.sideSel == m.sideSent {
		m.sideSel, m.sideSent = v.SideSel, v.SideSel
	}
	if !m.sideDrag && v.Sidebar != m.viewSide {
		m.layout.Sidebar, m.viewSide = v.Sidebar, v.Sidebar
		m.setWidth(m.winW)
	}
	// The info panel is the attach screen's; keep it so that saving the
	// layout here keeps it too.
	m.layout.Info = v.Info
	return m.waitView()
}

// Update runs update, then tells the view what this console changed in
// it: the selection and the sidebar.
func (m *dash) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	_, cmd := m.update(msg)
	if m.quitting {
		m.result.Attach = ""
		return m, tea.Quit
	}
	if m.overDone() {
		// The popup over the session closed: back to the session, with
		// what the footer said.
		m.result.Attach, m.result.Message = cmp.Or(m.over.Session, "view"), m.msg
		return m, tea.Quit
	}
	if m.view == nil {
		return m, cmd
	}
	cmds := []tea.Cmd{cmd}
	if m.userSel {
		m.userSel = false
		cmds = append(cmds, m.call(proto.MethodViewSelect, proto.ViewParams{Key: m.sel}))
	}
	if l := m.layout.Sidebar; !m.sideDrag && l != m.viewSide {
		m.viewSide = l
		cmds = append(cmds, m.call(proto.MethodViewSidebar, proto.ViewParams{Sidebar: &l}))
	}
	return m, tea.Batch(cmds...)
}

func (m *dash) update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.h = msg.Height
		m.setWidth(msg.Width)
		if m.view != nil {
			// The first size is the window as it was; later ones are the
			// user resizing it (docs/SPEC.md §3.3).
			resize := m.sized
			m.sized = true
			return m, m.call(proto.MethodViewSize, proto.ViewParams{Cols: uint16(msg.Width), Rows: uint16(msg.Height), Resize: resize})
		}
		return m, nil
	case viewMsg:
		return m, m.fromView()
	case feedMsg:
		return m, m.fromFeed()
	case viewDoneMsg:
		if msg.sel {
			m.selPending--
		}
		if msg.side {
			m.sideBusy = false
			return m, m.sendSideSel()
		}
		if msg.err != nil && !errors.Is(msg.err, ErrViewDown) {
			m.fail(msg.err)
		}
		return m, nil
	case dataMsg:
		return m, m.setData(Data(msg))
	case tickMsg:
		return m, tea.Batch(m.load(), m.reloadBoards())
	case boardMsg:
		if pv := m.projectPopupView(); pv != nil && pv.slug == msg.slug {
			if msg.err == nil {
				pv.setBoard(msg.board)
				pv.reviews = msg.reviews
			}
			if msg.memory != nil {
				pv.memory, pv.memErr = msg.memory, msg.memErr
			}
		}
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
		b.reviews = msg.reviews
		return m, nil
	case actionMsg:
		m.busy = false
		if m.leaving && msg.err == nil && msg.attach == "" {
			// Over a session, the view shows what the popup opened.
			m.result.Attach = "view"
			return m, tea.Quit
		}
		m.leaving = false
		if msg.err != nil {
			m.fail(msg.err)
			return m, m.load()
		}
		if msg.sel != "" {
			m.sel, m.userSel = msg.sel, true
		}
		m.msg = msg.msg
		if msg.attach != "" {
			if msg.current != "" && msg.current != m.current {
				m.current = msg.current
				m.rebuild()
			}
			if m.view != nil {
				// The view shows it, on every console; the dashboard ends
				// when that version comes.
				id, project := msg.attach, msg.current
				vc := m.view
				m.busy, m.leaving = true, m.over != nil
				return m, func() tea.Msg {
					_, err := vc.Do(proto.MethodViewAttach, proto.ViewParams{Session: id, Project: project})
					return actionMsg{err: err}
				}
			}
			m.result.Attach = msg.attach
			return m, tea.Quit
		}
		cmds := []tea.Cmd{m.load()}
		if b := m.boardView(); b != nil {
			cmds = append(cmds, m.loadBoard(b.slug))
		}
		if pv := m.projectPopupView(); pv != nil {
			cmds = append(cmds, m.loadBoard(pv.slug))
		}
		return m, tea.Batch(cmds...)
	case tea.KeyPressMsg:
		return m, m.key(msg)
	case tea.MouseClickMsg, tea.MouseMotionMsg, tea.MouseReleaseMsg, tea.MouseWheelMsg:
		return m, m.mouse(msg)
	}
	return m, nil
}

// sideClick handles a click on the sidebar, from under any popup: its
// border starts a drag; a click on a row gives the sidebar the keyboard,
// and a project row shows
// its dashboard, the coordinator row opens the coordinator, a thread row
// attaches its session.
func (m *dash) sideClick(mo tea.Mouse) tea.Cmd {
	if mo.Button != tea.MouseLeft {
		return nil
	}
	r, ok, border := sideHitAt(m.tree(), m.sideW(), m.h, mo.X, mo.Y)
	t, can, why := r.target()
	var cmd tea.Cmd
	if ok && !border {
		// A click on a row gives the sidebar the keyboard, its cursor on
		// the row; it keeps it in the session the row attaches.
		m.focus, m.sideSel = areaSide, r.key()
		cmd = m.sendSideSel()
	}
	switch {
	case border:
		m.sideDrag = true
	case !ok || m.busy:
	case !can:
		m.msg = why
	default:
		return tea.Batch(cmd, m.openTarget(t))
	}
	return cmd
}

// openTarget opens a sidebar row's target, closing any popup: a thread's
// session is attached, a coordinator opened, else the project's dashboard
// shown.
func (m *dash) openTarget(t Target) tea.Cmd {
	m.stack = nil
	switch {
	case t.Session != "":
		return m.act(func() actionMsg { return actionMsg{attach: t.Session, current: t.Project} })
	case t.Coordinator:
		return m.openProject(t.Project)
	}
	return m.showProject(t.Project)
}

// showProject shows project's dashboard: it becomes current, with its
// coordinator's row selected, on every console of the view.
func (m *dash) showProject(slug string) tea.Cmd {
	if m.current != slug {
		m.current = slug
		m.rebuild()
	}
	m.sel = "p:" + slug
	if m.view == nil {
		return nil
	}
	m.selPending++ // the view's selection follows with the answer
	return func() tea.Msg {
		_, err := m.view.Do(proto.MethodViewProject, proto.ViewParams{Project: slug})
		return viewDoneMsg{sel: true, err: err}
	}
}

// tree is the sidebar's tree, from the last poll: the listed project is
// current, and its row the one you are on; the keyboard's row is marked
// while the sidebar has the focus.
func (m *dash) tree() []treeRow {
	rows := buildTree(m.data.Projects, m.data.Sessions, treeIn{current: listProject(m.data, m.current)})
	if m.focus == areaSide {
		markCursor(rows, m.sideW(), m.sideSel)
	}
	return rows
}

// area is the area with the keyboard: the list when the details panel
// has it but doesn't show (the window got narrower).
func (m *dash) area() area {
	if split, _ := m.split(); m.focus == areaDetails && !split {
		return areaMain
	}
	return m.focus
}

// cycleFocus moves the keyboard to the next area (tab), or the previous
// (shift+tab): the list, the details panel when it shows, the sidebar.
func (m *dash) cycleFocus(key string) tea.Cmd {
	areas := []area{areaMain, areaSide}
	if split, _ := m.split(); split {
		areas = []area{areaMain, areaDetails, areaSide}
	}
	m.focus = cycle(areas, m.area(), key == "shift+tab")
	if m.focus == areaSide {
		// The keyboard's row starts on the row you are on.
		m.sideSel = hereKey(m.tree())
		return m.sendSideSel()
	}
	return nil
}

// sideKey runs a key while the sidebar has the focus; ok is false for a
// key that isn't the sidebar's, which goes to the list's actions.
func (m *dash) sideKeyboard(key string) (tea.Cmd, bool) {
	op, ok := sideOp(key)
	if !ok {
		return nil, false
	}
	st := sideKeyStep(m.tree(), m.sideW(), m.sideSel, op)
	var cmds []tea.Cmd
	if st.msg != "" {
		m.msg = st.msg
	}
	if st.back {
		m.focus = areaMain
	}
	if st.sel != "" && st.sel != m.sideSel {
		m.sideSel = st.sel
		cmds = append(cmds, m.sendSideSel())
	}
	if t := st.target; t != nil {
		if t.Session != "" || t.Coordinator {
			// enter, unlike a click, gives the session it attaches the
			// keyboard, as in a session.
			m.focus = areaMain
		}
		cmds = append(cmds, m.openTarget(*t))
	}
	return tea.Batch(cmds...), true
}

// setData takes a poll's result, rebuilds the rows and rings the bell
// when the server sent a notification (a session blocked, a thread
// reported) since the last poll.
func (m *dash) setData(d Data) tea.Cmd {
	first := !m.loaded
	m.data, m.loaded = d, true
	m.rebuild()
	ring := false
	if d.ServerOK {
		ring = m.seen && d.Alerts > m.alerts
		m.alerts, m.seen = d.Alerts, true
	}
	cmds := []tea.Cmd{tea.Tick(refresh, func(time.Time) tea.Msg { return tickMsg{} })}
	if ring {
		cmds = append(cmds, tea.Raw("\a"))
	}
	switch {
	case first && m.then == "t" && m.over != nil && m.over.Task > 0:
		// The info panel's task, over its session.
		cmds = append(cmds, m.openBoard(m.over.Project, m.over.Task))
		m.then = ""
	case first && m.then != "":
		// The key typed after the prefix in a session, now that the
		// projects are known.
		cmds = append(cmds, m.listKey(m.then))
		m.then = ""
	}
	if len(cmds) == 1 {
		return cmds[0]
	}
	return tea.Batch(cmds...)
}

// rebuild lays the rows out again from the last poll, for the listed
// project; a selection that went away moves to the first row.
func (m *dash) rebuild() {
	m.rows = buildRows(m.data, listProject(m.data, m.current))
	if m.selIndex() < 0 {
		m.sel = ""
		for _, r := range m.rows {
			if r.selectable() {
				m.sel = r.key
				break
			}
		}
	}
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
			m.sel, m.userSel = m.rows[j].key, true
			return
		}
	}
}

// paneSize is the size new sessions get: the window less the status bar
// and the empty row above it.
func (m *dash) paneSize() (int, int) { return m.w, max(m.h-2, 1) }

// key sends a key to the topmost overlay, else to the list's actions.
// The prefix works here as in a session (docs/SPEC.md §4, the key
// table): prefix then a key is that prefix command, the same everywhere,
// and never reaches a popup as a plain key.
func (m *dash) key(k tea.KeyPressMsg) tea.Cmd {
	if k.String() == "ctrl+c" {
		return tea.Quit
	}
	if cv, ok := m.top().(*captureView); ok {
		// Any key, the prefix too, is the new prefix.
		m.prefixed = false
		return cv.key(m, k)
	}
	if m.prefixed {
		m.prefixed = false
		return m.prefixCommand(k.String())
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
	switch m.area() {
	case areaSide:
		if cmd, ok := m.sideKeyboard(k.String()); ok {
			return cmd
		}
	case areaDetails:
		if cmd, ok := m.detailsKey(k.String()); ok {
			return cmd
		}
	}
	return m.listKey(k.String())
}

// sendSideSel tells the view where the sidebar's keyboard row is, when
// it moved since the last time and nothing is on its way.
func (m *dash) sendSideSel() tea.Cmd {
	if m.view == nil || m.sideBusy || m.sideSel == m.sideSent {
		return nil
	}
	return m.call(proto.MethodViewSideSel, proto.ViewParams{Key: m.sideSel})
}

// detailsKey runs a key while the details panel has the focus: the
// arrows scroll it, esc goes back to the list. ok is false for the
// other keys, which go to the list's actions.
func (m *dash) detailsKey(key string) (tea.Cmd, bool) {
	switch key {
	case "up", "k", "down", "j", "pgup", "pgdown":
		m.scrollDetails(scrollKeys[key])
	case "esc":
		m.focus = areaMain
	default:
		return nil, false
	}
	return nil, true
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

// projectHere is the project of the selected row, else the listed one;
// over a session, the popup's.
func (m *dash) projectHere() string {
	if m.over != nil && m.over.Project != "" {
		return m.over.Project
	}
	if r, ok := m.selected(); ok && r.project != "" {
		return r.project
	}
	return listProject(m.data, m.current)
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
	v.WindowTitle = "terminatr"
	return v
}

// render draws the sidebar and, beside it, the list or the topmost popup.
func (m *dash) render() string {
	m.geo = nil
	var s string
	if o := m.top(); o != nil {
		s = o.render(m)
	} else {
		s = m.renderList()
	}
	lines := strings.Split(s, "\n")
	side := sidebarLines(m.tree(), m.sideW(), m.h)
	for i := range side {
		var l string
		if i < len(lines) {
			l = lines[i]
		}
		side[i] += fit(l, m.w) + reset
	}
	return strings.Join(side, "\n")
}

func (m *dash) renderList() string {
	if m.over != nil {
		return m.frame("", m.base(), -1, "")
	}
	return m.frame("", m.listBody(), -1, m.footKeys())
}

// overDone says the popup over a session closed: its key ran and no
// overlay is left.
func (m *dash) overDone() bool {
	return m.over != nil && m.loaded && m.then == "" && m.top() == nil && !m.busy
}

// base is what a popup draws over: the list, or the session's screen
// under the header.
func (m *dash) base() []string {
	if m.over == nil {
		return m.listBody()
	}
	body := make([]string, m.bodyRows())
	for i := range body {
		if i+1 < len(m.over.Screen) {
			body[i] = m.over.Screen[i+1]
		}
	}
	return body
}

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
	if m.detailKey == m.sel {
		// Scrolled with the wheel.
		m.detailTop = min(m.detailTop, max(len(right)-room, 0))
		right = right[m.detailTop:]
	}
	// The details panel's divider is in the accent colour while the
	// panel has the keyboard.
	div := styleFaint.Render("│")
	if m.focus == areaDetails {
		div = styleAccent.Render("│")
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
		body[i] = fit(l, lw) + reset + div + fit(d, m.w-lw-1) + reset
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
			st := styleSel
			if m.area() != areaMain {
				st = st.Faint(true) // the keyboard is elsewhere
			}
			add(st.Render(fit(r.text(w), w)), r.key)
			if inline && r.thread != nil && strings.HasPrefix(r.key, "th:") {
				for _, l := range threadDetail(r.thread, "        ", true) {
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
		right = styleGood.Render(ic().working) + " server ok" + styleFaint.Render(fmt.Sprintf(" · %d session%s", n, map[bool]string{true: "s"}[n != 1]))
	}
	// The app, not a project: the project's own section is headed by its
	// slug, which may well be "terminatr".
	left := styleTitle.Render(" tm") + styleFaint.Render(" dashboard")
	if m.over != nil {
		left = styleTitle.Render(" tm") + styleFaint.Render(" "+oneLine(m.over.Title))
	}
	if title != "" {
		left += styleFaint.Render(" · ") + styleHead.Render(title)
	}
	head := fit(left, max(m.w-ansi.StringWidth(right)-1, 1)) + reset + " " + right
	foot := []string{m.rule("")}
	switch {
	case m.prefixed:
		keys = prefixHint
	case m.focus == areaSide && m.top() == nil:
		keys = sideHint + " · tab next"
	case m.area() == areaDetails && m.top() == nil:
		keys = "details: ↑ ↓ scroll · esc back · tab next"
	}
	m.shownKeys = keys
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
