package tui

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/theclifmeister/termalator/internal/emu"
	"github.com/theclifmeister/termalator/internal/project"
	"github.com/theclifmeister/termalator/internal/proto"
	"github.com/theclifmeister/termalator/internal/server"
	"github.com/theclifmeister/termalator/internal/thread"
	"github.com/theclifmeister/termalator/internal/view"
)

// The projects sidebar (docs/SPEC.md §4): a column on the left of every
// screen, the dashboard and the attach view alike, holding the project
// tree. Each project row has its coordinator's state glyph, a hint when
// one of its threads is blocked or waiting, and its count of open
// threads; an expanded project lists its coordinator and open threads
// under it, with their state glyphs and progress. The current project is
// always expanded, others open and close with ▸ ▾, and the row you are on
// is highlighted. A click on a project shows its dashboard, on the
// coordinator attaches to it, on a thread watches it. Its border column
// can be dragged, and { } b change it from the keys.
//
// Its state (width, slim strip, expanded projects) is part of the
// server-owned view; the rest here only draws and hit-tests.

// SidebarLayout is the sidebar's layout: part of the server-owned view
// (docs/SPEC.md §3.3), and kept in ui.json as the layout new views start
// with.
type SidebarLayout = view.Sidebar

const (
	sideDefault = view.SideDefault
	sideSlim    = view.SideSlim
	sideRoom    = view.SideRoom
)

// The kinds of tree rows.
const (
	treeProject = iota
	treeCoordinator
	treeThread
)

// treeRow is one row of the sidebar's tree.
type treeRow struct {
	kind    int
	slug    string // its project
	session string // the coordinator's or the thread's live session, "" for none
	thread  string // a thread's id
	title   string // a thread's title
	// state is the coordinator's (on a project or coordinator row) or the
	// thread's state word; "" when no coordinator runs.
	state   string
	pct     int  // a thread's percent, -1 for none
	open    bool // a project: expanded
	current bool // a project: the current one, always expanded
	hint    bool // a project: one of its threads is blocked or waiting
	remote  bool // a project or coordinator row: its coordinator's remote control is on
	threads int  // a project: its open threads
	here    bool // the row you are on
}

// treeIn is where a tree is drawn from: which project is current, which
// session has the focus ("" on the dashboard) and which projects are
// expanded besides the current one.
type treeIn struct {
	current  string
	focus    string
	expanded func(slug string) bool
}

// buildTree lays out the tree: every project, and under each expanded
// one its coordinator and open threads (ordered as the dashboard orders
// them). The row of the focused session is the one you are on, else the
// current project's.
func buildTree(ps []ProjectData, sessions []proto.SessionInfo, in treeIn) []treeRow {
	byID := map[string]proto.SessionInfo{}
	for _, s := range sessions {
		byID[s.ID] = s
	}
	var out []treeRow
	for _, p := range ps {
		pr := treeRow{kind: treeProject, slug: p.Slug, pct: -1, threads: len(p.Threads), current: p.Slug == in.current}
		pr.open = pr.current || in.expanded != nil && in.expanded(p.Slug)
		for _, s := range sessions {
			if s.Role == proto.RoleCoordinator && s.Project == p.Slug {
				pr.session, pr.state, pr.remote = s.ID, stateWord(s), s.RemoteControl
				break
			}
		}
		threads := append([]ThreadRow(nil), p.Threads...)
		sort.SliceStable(threads, func(i, j int) bool {
			return threadGroup(threads[i], byID) < threadGroup(threads[j], byID)
		})
		var kids []treeRow
		for i := range threads {
			t := &threads[i]
			tr := treeRow{kind: treeThread, slug: p.Slug, thread: t.ID, title: oneLine(t.Title), pct: -1}
			tr.state, _, tr.session = threadState(t, byID)
			tr.pct = t.Status.Progress().Percent
			pr.hint = pr.hint || tr.state == "blocked" || t.Status != nil && t.Status.NeedsYou != ""
			kids = append(kids, tr)
		}
		out = append(out, pr)
		if pr.open {
			out = append(out, treeRow{kind: treeCoordinator, slug: p.Slug, session: pr.session, state: pr.state, remote: pr.remote, pct: -1})
			out = append(out, kids...)
		}
	}
	here := -1
	for i, r := range out {
		if in.focus != "" && r.kind != treeProject && r.session == in.focus {
			here = i
			break
		}
		if here < 0 && r.kind == treeProject && r.current {
			here = i
		}
	}
	if here >= 0 {
		out[here].here = true
	}
	return out
}

// slimRows are the rows the slim strip shows: the projects alone.
func slimRows(rows []treeRow) []treeRow {
	var out []treeRow
	for _, r := range rows {
		if r.kind == treeProject {
			r.here = r.current
			out = append(out, r)
		}
	}
	return out
}

// shownRows are the rows a sidebar w columns wide shows.
func shownRows(rows []treeRow, w int) []treeRow {
	if w <= sideSlim {
		return slimRows(rows)
	}
	return rows
}

// sideTop is the first row shown in h rows (one is the header), so that
// the row you are on shows.
func sideTop(rows []treeRow, h int) int {
	cur := -1
	for i, r := range rows {
		if r.here {
			cur = i
		}
	}
	return scrollTop(cur, max(h-1, 1), len(rows))
}

// sidebarLines draws the sidebar w columns wide (its border included) and
// h rows tall.
func sidebarLines(all []treeRow, w, h int) []string {
	cw := max(w-1, 0) // less the border
	slim := w <= sideSlim
	rows := shownRows(all, w)
	border := styleFaint.Render("│")
	out := make([]string, 0, h)
	head, n := " PROJECTS", 0
	if slim {
		head = " PRJ"
	}
	for _, r := range all {
		if r.kind == treeProject {
			n++
		}
	}
	out = append(out, styleTitle.Render(fit(countLabel(head, n), cw))+reset+border)
	if n == 0 {
		note := " no projects"
		if slim {
			note = " —"
		}
		out = append(out, styleFaint.Render(fit(note, cw))+reset+border)
	}
	for _, r := range rows[sideTop(rows, h):] {
		if len(out) >= h {
			break
		}
		out = append(out, treeLine(r, cw, slim)+reset+border)
	}
	for len(out) < h {
		out = append(out, strings.Repeat(" ", cw)+border)
	}
	return out[:h]
}

// coordLook is a coordinator's glyph and style: "·" when none runs.
func coordLook(state string) (string, lipgloss.Style) {
	if state == "" {
		return "·", styleFaint
	}
	return stateLook(state)
}

// treeLine is one tree row cw cells wide. Full width:
//
//	"▾● termalator   ◆ 2 "   a project: open or closed, its coordinator's
//	                         glyph, the hint, its open threads
//	"  ○ coordinator"        its coordinator
//	"  ● t-0002 Bootstr 40%" a thread, with its progress
//
// The slim strip shows projects alone, "▸●term", the current one marked.
// A coordinator with remote control on gets "⌁" after the project's name,
// in either width, and after "coordinator".
// The row you are on is in reverse video (and, in the slim strip, marked),
// so colour is never the only signal.
func treeLine(r treeRow, cw int, slim bool) string {
	sel := styleSel.Bold(true)
	rc := ""
	if r.remote {
		rc = remoteMark
	}
	if slim {
		mark := " "
		if r.current {
			mark = "▸"
		}
		g, st := coordLook(r.state)
		if r.hint && r.state != "blocked" {
			g, st = "◆", styleWarn
		}
		name := []rune(r.slug)
		name = name[:min(len(name), max(cw-2-len([]rune(rc)), 0))]
		if r.here {
			return sel.Render(fit(mark+g+string(name)+rc, cw))
		}
		return fit(mark+st.Render(g)+string(name)+rc, cw)
	}
	switch r.kind {
	case treeProject:
		toggle := "▸"
		if r.open {
			toggle = "▾"
		}
		g, st := coordLook(r.state)
		hint := " "
		if r.hint {
			hint = "◆"
		}
		count := fmt.Sprintf(" %d ", r.threads)
		nw := max(cw-3-1-len(count), 1)
		name := fit(r.slug, nw)
		if rc != "" {
			name = fit(ansi.Truncate(r.slug, max(nw-1, 0), "…")+rc, nw)
		}
		if r.here {
			return sel.Render(fit(toggle+g+" "+name+hint+count, cw))
		}
		cnt := styleFaint.Render(count)
		if r.threads > 0 {
			cnt = count
		}
		nm := name
		if r.current {
			nm = styleHead.Render(name)
		}
		return fit(styleFaint.Render(toggle)+st.Render(g)+" "+nm+styleWarn.Render(hint)+cnt, cw)
	case treeCoordinator:
		g, st := coordLook(r.state)
		if r.here {
			return sel.Render(fit("  "+g+" coordinator"+rc, cw))
		}
		label := "coordinator"
		if r.state == "" {
			label = styleFaint.Render(label)
		}
		return fit("  "+st.Render(g)+" "+label+rc, cw)
	}
	g, st := stateLook(r.state)
	if g == "" {
		g = "·"
	}
	pct := ""
	if r.pct >= 0 {
		pct = fmt.Sprintf(" %d%%", r.pct)
	}
	label := fit(r.thread+" "+r.title, max(cw-4-len(pct), 1))
	if r.here {
		return sel.Render(fit("  "+g+" "+label+pct, cw))
	}
	return fit("  "+st.Render(g)+" "+label+styleFaint.Render(pct), cw)
}

// sideHitAt is what column x, row y of a sidebar w×h holds: the border,
// or a tree row; toggle is set on a project's ▸ ▾ (its first two
// columns).
func sideHitAt(all []treeRow, w, h, x, y int) (r treeRow, ok, toggle, border bool) {
	if x == w-1 {
		return r, false, false, true
	}
	rows := shownRows(all, w)
	i := sideTop(rows, h) + y - 1
	if y < 1 || x < 0 || x >= w-1 || i >= len(rows) {
		return r, false, false, false
	}
	r = rows[i]
	return r, true, r.kind == treeProject && w > sideSlim && x < 2, false
}

// Target is where a click on the sidebar goes: a project's dashboard, its
// coordinator (started when none runs), or a thread's session, watched.
type Target struct {
	Project string
	// Coordinator opens the project's coordinator; Session shows that
	// session; neither shows the project's dashboard.
	Coordinator bool
	Session     string
}

// target is where a click on r goes; ok is false for a thread without a
// running session, with why.
func (r treeRow) target() (t Target, ok bool, why string) {
	t.Project = r.slug
	switch r.kind {
	case treeCoordinator:
		t.Coordinator = true
	case treeThread:
		if r.session == "" {
			return t, false, r.thread + " has no running session; its coordinator restarts it"
		}
		t.Session = r.session
	}
	return t, true, ""
}

// OpenTarget opens t in the view vc: shows the project's dashboard, or
// attaches the coordinator (started first, by agentName, when none runs)
// or the session. A bare view's console hands over to view main with it
// (docs/SPEC.md §3.3); a full one opens it in place.
func OpenTarget(p server.Paths, vc *ViewConn, agentName string, t Target) error {
	switch {
	case t.Session != "":
		_, err := vc.Do(proto.MethodViewAttach, proto.ViewParams{Session: t.Session, Project: t.Project})
		return err
	case t.Coordinator:
		c, err := server.Connect(p, true)
		if err != nil {
			return err
		}
		v := vc.View()
		cols, rows := int(v.Cols), int(v.Rows)
		if cols == 0 || rows == 0 {
			cols, rows = 80, 24
		}
		area := v.Lay(cols, rows).Area
		id, err := OpenCoordinator(c.Call, t.Project, agentName, area.W, area.H)
		c.Close()
		if err != nil {
			return err
		}
		_, err = vc.Do(proto.MethodViewAttach, proto.ViewParams{Session: id, Project: t.Project})
		return err
	}
	_, err := vc.Do(proto.MethodViewProject, proto.ViewParams{Project: t.Project})
	return err
}

// loadSideProjects reads the projects and their open threads, with their
// STATUS.md, from the project folders: what the attach client polls for
// its sidebar.
func loadSideProjects() []ProjectData {
	list, _ := project.List()
	out := make([]ProjectData, 0, len(list))
	for _, sum := range list {
		pd := ProjectData{Slug: sum.Slug, Name: sum.Name}
		if p, err := project.Open(sum.Slug); err == nil {
			recs, _ := thread.List(p)
			for _, r := range recs {
				if r.State == thread.Resolved {
					continue
				}
				tr := ThreadRow{Record: r}
				tr.Status, _ = thread.ReadStatus(p, r.ID)
				pd.Threads = append(pd.Threads, tr)
			}
		}
		out = append(out, pd)
	}
	return out
}

// sidebar is the attach client's sidebar: what it shows. Its layout and
// tree state are the view's. Guarded by client.mu.
type sidebar struct {
	uiFile   string
	agent    string // starts a clicked project's coordinator
	projects []ProjectData
	sessions []proto.SessionInfo
	drag     bool     // the mouse is moving its border
	drawn    []string // the lines on screen; nil repaints them all
}

// sideCurrent is the attach view's current project: the focused pane's,
// else the view's. c.mu held.
func (c *client) sideCurrent() string {
	if c.focus != nil && c.focus.info.Project != "" {
		return c.focus.info.Project
	}
	return c.v.Current
}

// sideTree is the attach view's tree. c.mu held.
func (c *client) sideTree() []treeRow {
	focus := c.v.Focus
	if c.focus != nil {
		focus = c.focus.info.ID
	}
	return buildTree(c.side.projects, c.side.sessions, treeIn{current: c.sideCurrent(), focus: focus, expanded: c.v.IsExpanded})
}

// appendSidebar draws the sidebar's changed lines. c.mu held.
func (c *client) appendSidebar(b []byte, wrote bool) ([]byte, bool) {
	lines := sidebarLines(c.sideTree(), c.sideW, c.rows)
	for y, l := range lines {
		if y < len(c.side.drawn) && c.side.drawn[y] == l {
			continue
		}
		b = append(b, fmt.Sprintf("\x1b[%d;1H\x1b[0m", y+1)...)
		b = append(b, l...)
		wrote = true
	}
	c.side.drawn = lines
	return b, wrote
}

// viewCols is the width the view is laid out at. c.mu held.
func (c *client) viewCols() int {
	if c.v.Cols > 0 {
		return int(c.v.Cols)
	}
	return c.cols
}

// setSidebar changes the view's sidebar: a layout change, so every
// console's panes follow (docs/SPEC.md §3.3). save also keeps it in
// ui.json, as the default of new views.
func (c *client) setSidebar(l SidebarLayout, save bool) {
	c.act(proto.MethodViewSidebar, proto.ViewParams{Sidebar: &l})
	if !save || !c.lock() {
		return
	}
	path := c.side.uiFile
	c.mu.Unlock()
	if path == "" {
		return
	}
	if err := SaveSidebar(path, l); err != nil && c.lock() {
		c.flash = "ui.json: " + err.Error()
		c.status()
		c.mu.Unlock()
		c.poke()
	}
}

// sideKey runs a sidebar key ({, } or b) after the prefix.
func (c *client) sideKey(key string) {
	if !c.lock() {
		return
	}
	if c.side == nil {
		c.mu.Unlock()
		return
	}
	l, msg := c.v.Sidebar.Key(key, c.viewCols())
	same := l == c.v.Sidebar
	if msg != "" {
		c.flash = msg
		c.status()
	}
	c.mu.Unlock()
	if !same {
		c.setSidebar(l, true)
	}
	c.poke()
}

// sideMouse handles the mouse over the sidebar, or dragging its border:
// a click on ▸ ▾ opens or closes a project, on a row goes where it points
// (sideGo). c.mu held; released here.
func (c *client) sideMouse(m emu.Mouse) {
	sd := c.side
	switch {
	case sd.drag && m.Action == emu.MouseMotion:
		l := c.v.Sidebar.DragTo(m.X, c.viewCols())
		same := l == c.v.Sidebar
		c.mu.Unlock()
		if !same {
			c.setSidebar(l, false)
		}
		c.poke()
		return
	case sd.drag && m.Action == emu.MouseRelease:
		sd.drag = false
		l := c.v.Sidebar
		c.mu.Unlock()
		c.setSidebar(l, true)
		return
	case m.Action != emu.MousePress || m.Button != emu.MouseLeft:
		c.mu.Unlock()
		return
	}
	r, ok, toggle, border := sideHitAt(c.sideTree(), c.sideW, c.rows, m.X, m.Y)
	t, can, why := r.target()
	switch {
	case border:
		sd.drag = true
	case !ok:
	case toggle && r.current:
		c.flash = r.slug + " is the current project: it stays open"
		c.status()
	case toggle:
		c.mu.Unlock()
		c.act(proto.MethodViewExpand, proto.ViewParams{Project: r.slug, Expand: !r.open})
		c.poke()
		return
	case !can:
		c.flash = why
		c.status()
	case r.here && r.kind != treeProject:
		c.flash = "you are on it"
		c.status()
	default:
		c.mu.Unlock()
		c.sideGo(t)
		return
	}
	c.mu.Unlock()
	c.poke()
}

// sideGo opens a clicked sidebar row. A full view opens it in place, on
// every console of the view; a bare one (tm attach, tm project open) has
// no dashboard, so this console leaves it and hands over to a full tm
// joined to view main, which opens it (Result.GoTo).
func (c *client) sideGo(t Target) {
	if c.bare {
		c.goTo = &t
		c.detachThen("")
		return
	}
	go func() {
		if err := OpenTarget(c.paths, c.vc, c.side.agent, t); err != nil {
			var perr *proto.Error
			msg := err.Error()
			if errors.As(err, &perr) {
				msg = perr.Message
			}
			if c.lock() {
				c.flash = msg
				c.status()
				c.mu.Unlock()
				c.poke()
			}
			return
		}
		c.apply(c.vc.View())
	}()
}
