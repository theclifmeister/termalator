package tui

import (
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	uv "github.com/charmbracelet/ultraviolet"

	"github.com/theclifmeister/terminatr/internal/emu"
	"github.com/theclifmeister/terminatr/internal/project"
	"github.com/theclifmeister/terminatr/internal/proto"
	"github.com/theclifmeister/terminatr/internal/server"
	"github.com/theclifmeister/terminatr/internal/thread"
	"github.com/theclifmeister/terminatr/internal/view"
)

// The projects sidebar (docs/SPEC.md §4): a column on the left of every
// screen, the dashboard and the attach view alike, holding the project
// tree. Each project row has its coordinator's state glyph, a hint when
// one of its threads is blocked or waiting or one of its tasks needs
// you (review or blocked), and its count of open threads. Every project
// is always expanded: its coordinator hangs under it and its open
// threads under the coordinator, on tree connectors (├─ └─), with their
// state glyphs and progress in columns of their own. The current project is in
// the accent colour, and the row you are on is highlighted. A click on a project shows its dashboard, on the
// coordinator attaches to it, on a thread attaches its session. Its border column
// can be dragged, and { } b change it from the keys.
//
// Its state (width, slim strip, keyboard row) is part of the
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
	name    string // a thread's name: its task's id, else its own (threadName)
	title   string // a thread's title
	// state is the coordinator's (on a project or coordinator row) or the
	// thread's state word; "" when no coordinator runs.
	state   string
	pct     int  // a thread's percent, -1 for none
	last    bool // a coordinator or thread: the last row under its parent
	current bool // a project: the current one
	hint    bool // a project: one of its threads is blocked or waiting, or a task needs you
	remote  bool // a coordinator row: its remote control is on
	ctx     int  // a coordinator row: its context window's use in percent, -1 for unknown
	ctxHint bool // a coordinator row: ctx reached [ui] context_hint
	paused  bool // a project: paused (no nudges, follow-up or new threads)
	threads int  // a project: its open threads
	here    bool // the row you are on
	// cursor is the keyboard's row, set while the sidebar has this
	// console's keyboard focus (markCursor).
	cursor bool
}

// key names r as View.SideSel does: "p:<project>", "c:<project>" or
// "t:<project>/<thread>".
func (r treeRow) key() string {
	switch r.kind {
	case treeCoordinator:
		return "c:" + r.slug
	case treeThread:
		return "t:" + r.slug + "/" + r.thread
	}
	return "p:" + r.slug
}

// sideCursor is the index in rows of the keyboard's row: the row sel
// names, else the row you are on, else the first; -1 without rows.
func sideCursor(rows []treeRow, sel string) int {
	here := -1
	for i, r := range rows {
		if sel != "" && r.key() == sel {
			return i
		}
		if r.here && here < 0 {
			here = i
		}
	}
	if here < 0 && len(rows) > 0 {
		here = 0
	}
	return here
}

// hereKey is the key of the row you are on, "" for none.
func hereKey(rows []treeRow) string {
	for _, r := range rows {
		if r.here {
			return r.key()
		}
	}
	return ""
}

// markCursor marks the keyboard's row among the rows a sidebar w columns
// wide shows: the sidebar has the keyboard focus.
func markCursor(all []treeRow, w int, sel string) {
	rows := shownRows(all, w)
	i := sideCursor(rows, sel)
	if i < 0 {
		return
	}
	key := rows[i].key()
	for j := range all {
		if all[j].key() == key {
			all[j].cursor = true
			return
		}
	}
}

// treeIn is where a tree is drawn from: which project is current and
// which session has the focus ("" on the dashboard).
type treeIn struct {
	current string
	focus   string
	// ctxHint is [ui] context_hint: the percent from which a coordinator
	// row says to consider /clear; 0 for never.
	ctxHint int
}

// buildTree lays out the tree: every project, under each its
// coordinator, and under that its open threads (ordered as the dashboard
// orders them). The row of the focused session is the one you are on, else the
// current project's.
func buildTree(ps []ProjectData, sessions []proto.SessionInfo, in treeIn) []treeRow {
	byID := map[string]proto.SessionInfo{}
	for _, s := range sessions {
		byID[s.ID] = s
	}
	var out []treeRow
	for _, p := range ps {
		pr := treeRow{kind: treeProject, slug: p.Slug, pct: -1, threads: len(p.Threads), current: p.Slug == in.current,
			hint: p.Counts["needs_you"] > 0, paused: p.Safety != nil && p.Safety.Paused}
		remote, ctx := false, -1
		for _, s := range sessions {
			if s.Role == proto.RoleCoordinator && s.Project == p.Slug {
				pr.session, pr.state, remote, ctx = s.ID, stateWord(s), s.RemoteControl, s.ContextPercent()
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
			tr := treeRow{kind: treeThread, slug: p.Slug, thread: t.ID, name: threadName(t.Task, t.ID), title: oneLine(t.Title), pct: -1}
			tr.state, _, tr.session = threadState(t, byID)
			tr.pct = t.Status.Progress().Percent
			pr.hint = pr.hint || tr.state == "blocked" || t.Status != nil && t.Status.NeedsYou != ""
			kids = append(kids, tr)
		}
		// The threads hang under the coordinator, the project's only child.
		coord := treeRow{kind: treeCoordinator, slug: p.Slug, session: pr.session, state: pr.state, remote: remote, pct: -1, last: true,
			ctx: ctx, ctxHint: ctx >= 0 && in.ctxHint > 0 && ctx >= in.ctxHint}
		if len(kids) > 0 {
			kids[len(kids)-1].last = true
		}
		kids = append([]treeRow{coord}, kids...)
		out = append(out, pr)
		out = append(out, kids...)
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
// the keyboard's row, else the row you are on, shows.
func sideTop(rows []treeRow, h int) int {
	cur := -1
	for i, r := range rows {
		if r.cursor {
			cur = i
			break
		}
		if r.here {
			cur = i
		}
	}
	return scrollTop(cur, max(h-1, 1), len(rows))
}

// sidebarLines draws the sidebar w columns wide (its border included) and
// h rows tall: a header with the count of projects, then the tree. While
// it has the keyboard focus (a row is the cursor) its border is in the
// accent colour and the cursor's row is highlighted instead of the one
// you are on.
func sidebarLines(all []treeRow, w, h int) []string {
	cw := max(w-1, 0) // less the border
	slim := w <= sideSlim
	rows := shownRows(all, w)
	focused := slices.ContainsFunc(all, func(r treeRow) bool { return r.cursor })
	border := styleFaint.Render("│")
	if focused {
		border = styleAccent.Render("│")
	}
	out := make([]string, 0, h)
	n := 0
	for _, r := range all {
		if r.kind == treeProject {
			n++
		}
	}
	// Every row leaves a column before the border (sideGap).
	out = append(out, sideHead(n, cw-sideGap, slim)+strings.Repeat(" ", sideGap)+reset+border)
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
		out = append(out, treeLine(r, cw, slim, focused)+reset+border)
	}
	for len(out) < h {
		out = append(out, strings.Repeat(" ", cw)+border)
	}
	return out[:h]
}

// sideHead is the sidebar's header, cw cells wide: " PROJECTS", its
// count of projects in the state column on the right; in the slim strip
// the word alone, cut with "…".
func sideHead(n, cw int, slim bool) string {
	if slim {
		return styleTitle.Render(fit(" PROJECTS", cw))
	}
	count := ""
	if n > 0 {
		count = fmt.Sprint(n)
	}
	gap := cw - ansi.StringWidth(" PROJECTS") - len(count)
	if gap < 1 {
		return styleTitle.Render(fit(countLabel(" PROJECTS", n), cw))
	}
	return styleTitle.Render(" PROJECTS") + strings.Repeat(" ", gap) + styleFaint.Render(count)
}

// coordLook is a coordinator's glyph and style: "·" when none runs.
func coordLook(state string) (string, lipgloss.Style) {
	if state == "" {
		return ic().none, styleFaint
	}
	return stateLook(state)
}

// sideGap is the blank column between the sidebar's rows and its border,
// so the state glyphs don't touch it.
const sideGap = 1

// treeLine is one tree row cw cells wide, its last sideGap blank: the
// glyphs keep off the border, and the title (the slug in the slim strip)
// gives up the room. On the highlighted row the blank is highlighted
// too: a Nerd Font icon in the last column draws wider than its cell,
// and its overflow, in the highlight's foreground, would vanish on the
// plain background.
func treeLine(r treeRow, cw int, slim, focused bool) string {
	gap := strings.Repeat(" ", min(sideGap, cw))
	if hl, sel := treeSel(r, focused); hl {
		gap = sel.Render(gap)
	}
	return treeCells(r, max(cw-sideGap, 0), slim, focused) + gap
}

// treeSel says whether r is the highlighted row (the keyboard's while
// the sidebar has the focus, else the one you are on) and its style.
func treeSel(r treeRow, focused bool) (bool, lipgloss.Style) {
	sel := styleSel.Bold(true)
	if focused {
		return r.cursor, sel.Foreground(lipgloss.Cyan)
	}
	return r.here, sel
}

// treeCells is one tree row cw cells wide, in this console's icon set
// (icons.go). In a sidebar 25 columns wide, in the unicode set:
//
//	" ■ terminatr       2 ◆"  a project: its name, its open threads,
//	                           the hint that one of them is blocked or
//	                           waiting, or that a task needs you
//	" └─ coordinator     ⌁ ○"  its coordinator, ⌁ while its remote
//	                           control is on
//	"    ├─ T12 Bump…  40% ●"  a thread's task id and title (an ad hoc
//	                           or adopted thread's own id, t-0002), its
//	                           progress, its state, one level under the
//	                           coordinator
//	"    └─ T14 Sort…      ▲"  the coordinator's last thread
//
// The nerd set opens the current project's folder and marks the
// coordinator and threads with an icon of their own after the connector.
// The counts and percents share one column, the state glyphs the last
// one, so they line up down the tree; a long name or title is cut with
// "…", but a thread's name (its task id) never is: its title gives way. Project rows are
// bold, the current one in the accent colour; the
// connectors are faint.
//
// The slim strip shows projects alone, "● term…": its coordinator's
// glyph (or the hint), then its name, cut with "…"; the current one's
// name in the accent colour, as in the full sidebar.
// A coordinator with remote control on gets "⌁" in its row's count
// column, one blank before its state glyph (two in the Nerd set, whose
// icon draws wide); never on the project's row, so
// not in the slim strip either.
// A paused project gets "∥" after its name, in either width.
// The row you are on is in reverse video (and, in the slim strip, marked),
// so colour is never the only signal. While the sidebar has the keyboard
// focus, the keyboard's row is instead, in the accent colour.
func treeCells(r treeRow, cw int, slim, focused bool) string {
	i := ic()
	var sel lipgloss.Style
	r.here, sel = treeSel(r, focused)
	pz := ""
	if r.paused && r.kind == treeProject {
		pz = i.paused
	}
	if slim {
		g, st := coordLook(r.state)
		if r.hint && r.state != "blocked" {
			g, st = i.hint, styleWarn
		}
		name := ansi.Truncate(r.slug, max(cw-3-ansi.StringWidth(pz), 0), "…") + pz
		if r.here {
			return " " + sel.Render(fit(g+" "+name, cw-1))
		}
		look := styleHead
		if r.current {
			look = styleTitle
		}
		return fit(" "+st.Render(g)+" "+look.Render(name), cw)
	}
	// The right-hand columns: a count or percent, then a glyph.
	right := func(num string, g string, st lipgloss.Style, hl bool) string {
		if hl {
			return num + " " + g
		}
		return styleFaint.Render(num) + " " + st.Render(g)
	}
	const rw = pctCol + 2
	switch r.kind {
	case treeProject:
		count := fmt.Sprintf("%*d", pctCol, r.threads)
		hint, hst := " ", stylePlain
		if r.hint {
			hint, hst = i.hint, styleWarn
		}
		folder := i.folder
		if r.current {
			folder = i.folderOpen
		}
		nw := max(cw-3-rw, 1)
		name := fit(ansi.Truncate(r.slug, max(nw-ansi.StringWidth(pz), 0), "…")+pz, nw)
		if r.here {
			return " " + sel.Render(fit(folder+" "+name+right(count, hint, hst, true), cw-1))
		}
		look, mark := styleHead, styleFaint.Render(folder)
		if r.current {
			look, mark = styleTitle, styleAccent.Render(folder)
		}
		cnt := right(count, hint, hst, false) // no threads: faint
		if r.threads > 0 {
			cnt = count + " " + hst.Render(hint)
		}
		return fit(" "+mark+" "+look.Render(name)+cnt, cw)
	}
	conn, node := i.mid, i.thread
	if r.last {
		conn = i.end
	}
	if r.kind == treeCoordinator {
		node = i.coord
	}
	// A thread sits one level down, under the coordinator's label (its
	// icon in the nerd set). The coordinator is the project's last row,
	// so no │ runs down beside its threads.
	indent := ""
	if r.kind == treeThread {
		indent = strings.Repeat(" ", ansi.StringWidth(i.end))
		if i.coord == "" {
			indent += " "
		}
	}
	lead := " " + indent + styleFaint.Render(conn) + " "
	if node != "" {
		lead = " " + indent + styleFaint.Render(conn+node) + " "
	}
	ld := ansi.StringWidth(lead)
	lw := max(cw-ld-rw, 1)
	if r.kind == treeCoordinator {
		g, st := coordLook(r.state)
		// "⌁" sits at the right of the count column; "coordinator" may
		// take the rest of it.
		lw := max(cw-ld-2, 0) // the label column and the count's
		// A Nerd Font's remote icon draws two cells wide, over the blank
		// after it, so that set gets a second one: a blank always shows
		// between the icon and the state glyph, and the label gives way.
		name, rc, gap, tail := "coordinator", "", " ", ""
		if r.remote {
			if i.name == IconsNerd {
				gap = "  "
			}
			lw = max(lw-1-len(gap), 0)
			rc, tail = i.remote, " "
		}
		// The context use follows the name where there is room, coloured
		// (warn from the threshold, red from 80%), with "/clear?" past it.
		name = fit(name, max(lw, 0))
		note, nst := ctxNote(r, lw-len(strings.TrimRight(name, " ")))
		if note != "" {
			name = strings.TrimRight(name, " ")
		}
		pad := strings.Repeat(" ", max(lw-ansi.StringWidth(name)-len(note), 0))
		if r.here {
			return lead + sel.Render(fit(name+note+pad+tail+rc+gap+g, cw-ld))
		}
		if r.state == "" {
			name = styleFaint.Render(name)
		}
		label := name + nst.Render(note) + pad + tail
		return fit(lead+label+styleAccent.Render(rc)+gap+st.Render(g), cw)
	}
	g, st := stateLook(r.state)
	if g == "" {
		g, st = i.none, styleFaint
	}
	// The percent has a column of its own, the same width on every row,
	// so titles are cut at the same place. The name leads and is never
	// cut: the title gives way; where even the name doesn't fit the label
	// column it takes the percent's too, then the space before the
	// glyph, and where it still doesn't fit it is left out.
	pct := strings.Repeat(" ", pctCol)
	if r.pct >= 0 {
		pct = fmt.Sprintf("%*d%%", pctCol-1, r.pct)
	}
	label := threadLabel(r.name, r.title, lw)
	if iw := ansi.StringWidth(r.name); iw > cw-ld-rw {
		pct = ""
		label = threadLabel(r.name, "", max(cw-ld-2, 0))
		if iw == cw-ld-1 {
			// The name fits only right up against the glyph.
			if r.here {
				return lead + sel.Render(r.name+g)
			}
			return lead + r.name + st.Render(g)
		}
	}
	if r.here {
		return lead + sel.Render(fit(label+right(pct, g, st, true), cw-ld))
	}
	return fit(lead+label+right(pct, g, st, false), cw)
}

// sideHitAt is what column x, row y of a sidebar w×h holds: the border,
// or a tree row.
func sideHitAt(all []treeRow, w, h, x, y int) (r treeRow, ok, border bool) {
	if x == w-1 {
		return r, false, true
	}
	rows := shownRows(all, w)
	i := sideTop(rows, h) + y - 1
	if y < 1 || x < 0 || x >= w-1 || i >= len(rows) {
		return r, false, false
	}
	return rows[i], true, false
}

// Target is where a click on the sidebar goes: a project's dashboard, its
// coordinator (started when none runs), or a thread's session.
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
// attaches the coordinator (started first, by the project's
// coordinator_agent, when none runs)
// or the session. A bare view's console hands over to view main with it
// (docs/SPEC.md §3.3); a full one opens it in place.
func OpenTarget(p server.Paths, vc *ViewConn, t Target) error {
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
		id, err := OpenCoordinator(c.Call, t.Project, "", area.W, area.H)
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
		if sum.Safety != nil && sum.Safety.Archived {
			continue
		}
		pd := ProjectData{Slug: sum.Slug, Safety: sum.Safety}
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
	projects []ProjectData
	sessions []proto.SessionInfo
	ctxHint  int      // [ui] context_hint
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
	rows := buildTree(c.side.projects, c.side.sessions, treeIn{current: c.sideCurrent(), focus: focus, ctxHint: c.side.ctxHint})
	if c.kb == areaSide {
		markCursor(rows, c.sideW, c.v.SideSel)
	}
	return rows
}

// areas are the session's areas that take the keyboard, in prefix+tab's
// order: the pane, the sidebar, the info panel while it shows. c.mu held.
func (c *client) areas() []area {
	out := []area{areaMain}
	if c.side != nil && c.sideW > 0 {
		out = append(out, areaSide)
	}
	if c.infoW > 0 {
		out = append(out, areaInfo)
	}
	return out
}

// nextArea moves the keyboard to the next area (prefix+tab, and tab in
// the sidebar or the info panel): pane, sidebar, info panel, pane. The
// sidebar's cursor starts on the row you are on.
func (c *client) nextArea() {
	if !c.lock() {
		return
	}
	here := ""
	areas := c.areas()
	switch next := cycle(areas, c.kb, false); {
	case len(areas) == 1:
		c.flash = "no sidebar here"
	case next == areaSide:
		here = hereKey(c.sideTree())
		c.kb, c.v.SideSel = areaSide, here
	default:
		c.kb = next
	}
	c.status()
	c.mu.Unlock()
	c.poke()
	if here != "" && c.vc != nil {
		c.act(proto.MethodViewSideSel, proto.ViewParams{Key: here})
	}
}

// sideKeyboard runs a key while the sidebar has the keyboard: a sidebar
// key (sideActions) moves its cursor or opens the row; tab moves on to
// the next area, as prefix+tab; any other key is dropped, so nothing
// reaches the pane. c.mu held; released here.
func (c *client) sideKeyboard(k uv.Key) {
	name := keyName(k)
	op, ok := sideOp(name)
	if name == "tab" {
		c.flash = ""
		c.mu.Unlock()
		c.nextArea()
		return
	}
	if !ok || c.side == nil {
		c.flash = ""
		c.status()
		c.mu.Unlock()
		c.poke()
		return
	}
	st := sideKeyStep(c.sideTree(), c.sideW, c.v.SideSel, op)
	c.flash = st.msg
	if st.back || st.target != nil && !st.here {
		c.kb = areaMain
	}
	if st.target != nil && st.here {
		c.flash = "you are on it"
	}
	if st.sel != "" {
		c.v.SideSel = st.sel // at once here; the view follows
	}
	c.status()
	c.mu.Unlock()
	c.poke()
	if st.sel != "" {
		c.act(proto.MethodViewSideSel, proto.ViewParams{Key: st.sel})
	}
	if st.target != nil && !st.here {
		c.sideGo(*st.target)
	}
	c.poke()
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
// a click on a row gives the sidebar the keyboard and goes where the row
// points (sideGo). c.mu held; released here.
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
	r, ok, border := sideHitAt(c.sideTree(), c.sideW, c.rows, m.X, m.Y)
	t, can, why := r.target()
	sel := ""
	if ok && !border {
		// A click on a row gives the sidebar the keyboard, its cursor on
		// the row; it keeps it while the row's session shows.
		sel = r.key()
		c.kb, c.v.SideSel = areaSide, sel
		c.flash = ""
		c.status()
	}
	open := false
	switch {
	case border:
		sd.drag = true
	case !ok:
	case !can:
		c.flash = why
		c.status()
	case r.here && r.kind != treeProject:
		c.flash = "you are on it"
		c.status()
	default:
		open = true
	}
	c.mu.Unlock()
	if sel != "" && c.vc != nil {
		c.act(proto.MethodViewSideSel, proto.ViewParams{Key: sel})
	}
	if open {
		c.sideGo(t)
	}
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
		if err := OpenTarget(c.paths, c.vc, t); err != nil {
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

// The sidebar's keyboard (docs/SPEC.md §4): tab on the dashboard, or
// prefix+tab in a session, gives it the focus; these keys then move its
// cursor (the view's SideSel) and open what it is on. One table drives
// the keys and the help.

// Sidebar key operations.
const (
	sideUp = iota
	sideDown
	sideIn    // on a project, down to its first row
	sideOut   // on a row under a project, up to it
	sideEnter // what a click does
	sideBack  // the focus goes back to the list or the pane
)

// sideAction is a sidebar key: its keys, what it does, its help line and
// the mouse's way to the same thing (TestEverySidebarClickHasKey).
type sideAction struct {
	keys        []string
	op          int
	label, help string
}

var sideActions = []sideAction{
	{[]string{"up", "k"}, sideUp, "↑ ↓ k j", "move through the tree"},
	{[]string{"down", "j"}, sideDown, "", ""},
	{[]string{"right", "l"}, sideIn, "→ ←", "→ on a project goes down into it, ← on a row under a project up to it"},
	{[]string{"left", "h"}, sideOut, "", ""},
	{[]string{"enter"}, sideEnter, "enter", "what a click does: a project shows its dashboard, its coordinator or a thread attaches"},
	{[]string{"esc"}, sideBack, "esc", "back to the list (in a session: to the pane)"},
}

// sideOp is the sidebar operation of key; ok is false for none.
func sideOp(key string) (op int, ok bool) {
	for _, a := range sideActions {
		if slices.Contains(a.keys, key) {
			return a.op, true
		}
	}
	return 0, false
}

// sideStep is what a sidebar key does to the tree: move the cursor to
// sel, go to a row's target, or say msg.
type sideStep struct {
	sel    string
	target *Target
	here   bool // target is the row you are on
	msg    string
	back   bool
}

// sideKeyStep decides what sidebar key op does with the cursor at sel in
// the tree all, drawn w columns wide.
func sideKeyStep(all []treeRow, w int, sel string, op int) sideStep {
	if op == sideBack {
		return sideStep{back: true}
	}
	rows := shownRows(all, w)
	i := sideCursor(rows, sel)
	if i < 0 {
		return sideStep{msg: "no projects"}
	}
	r := rows[i]
	moveTo := func(j int) sideStep {
		if j < 0 || j >= len(rows) || j == i {
			return sideStep{}
		}
		return sideStep{sel: rows[j].key()}
	}
	switch op {
	case sideUp:
		return moveTo(i - 1)
	case sideDown:
		return moveTo(i + 1)
	case sideIn:
		if r.kind == treeProject && w > sideSlim {
			return moveTo(i + 1)
		}
	case sideOut:
		for j := i - 1; j >= 0 && r.kind != treeProject; j-- {
			if rows[j].kind == treeProject {
				return moveTo(j)
			}
		}
	case sideEnter:
		t, can, why := r.target()
		if !can {
			return sideStep{msg: why}
		}
		return sideStep{target: &t, here: r.here && r.kind != treeProject}
	}
	return sideStep{}
}

// sideHint is the hint while the sidebar has the keyboard focus.
const sideHint = "sidebar: ↑ ↓ move · → ← in/out · enter open · esc back"

// pctCol is the width of a thread row's percent column: " 100%".
const pctCol = 5

// threadLabel is a thread row's label w cells wide: "T12 Title" (a
// thread without a task: "t-0001 Title"), the title cut to fit (or left
// out when not even a letter of it fits) and the name never cut. A name
// wider than w leaves the label blank.
func threadLabel(id, title string, w int) string {
	iw := ansi.StringWidth(id)
	switch {
	case iw > w:
		return strings.Repeat(" ", w)
	case title == "" || w-iw-1 < 2:
		return fit(id, w)
	}
	return id + " " + clipWord(title, w-iw-1)
}

// clipWord fits s in w cells; a cut title ends in "…" right after its
// last letter, never after a space.
func clipWord(s string, w int) string {
	if ansi.StringWidth(s) <= w {
		return fit(s, w)
	}
	cut := strings.TrimRight(ansi.Truncate(s, max(w-1, 0), ""), " ")
	return fit(cut+"…", w)
}

// ctxNote is a coordinator row's context use, " 42%" or " 42% /clear?"
// past the threshold, within room cells, and its style; "" when unknown
// or when it doesn't fit.
func ctxNote(r treeRow, room int) (string, lipgloss.Style) {
	if r.ctx < 0 {
		return "", stylePlain
	}
	note := fmt.Sprintf(" %d%%", r.ctx)
	if r.ctxHint && room >= len(note)+len(" /clear?") {
		note += " /clear?"
	}
	if len(note) > room {
		return "", stylePlain
	}
	switch {
	case r.ctx >= 80:
		return note, styleBad
	case r.ctxHint:
		return note, styleWarn
	}
	return note, styleFaint
}
