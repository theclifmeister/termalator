package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/x/ansi"

	"github.com/theclifmeister/termalator/internal/emu"
	"github.com/theclifmeister/termalator/internal/project"
	"github.com/theclifmeister/termalator/internal/proto"
	"github.com/theclifmeister/termalator/internal/thread"
)

// The projects sidebar (docs/SPEC.md §4): a column on the left of every
// screen, the dashboard and the attach view alike, listing every project
// with its coordinator's state glyph and its count of open threads. The
// current project is highlighted; a click opens a project's coordinator.
// Its border column can be dragged, and { } b change it from the keys.
//
// Its state is SidebarLayout, kept on its own in ui.json, so it can move
// into a server-owned view later; the rest here only draws and hit-tests.

// ThenOpen, then a project's slug, is the dashboard key (Result.Then) of
// a click on the sidebar while attached: open that project's coordinator.
const ThenOpen = "open:"

// SidebarLayout is the sidebar's part of ui.json.
type SidebarLayout struct {
	// Width is the full sidebar's width in columns, its border included.
	Width int `json:"width"`
	// Slim keeps the slim strip (glyphs and short names) in any window.
	Slim bool `json:"slim"`
}

const (
	sideDefault = 24 // a full sidebar's width
	sideMin     = 14 //
	sideMax     = 48 //
	sideSlim    = 7  // the slim strip: a marker, a glyph, 4 letters, the border
	sideRoom    = 60 // a full sidebar leaves the panes at least this many columns
	sideStep    = 2  // { and } change the width this much
)

// clamp keeps the width in range; 0 is the default.
func (s SidebarLayout) clamp() SidebarLayout {
	if s.Width == 0 {
		s.Width = sideDefault
	}
	s.Width = min(max(s.Width, sideMin), sideMax)
	return s
}

// cols is the sidebar's width in a window w columns wide: the full width,
// or the slim strip when asked for or when the window is narrow. It never
// disappears.
func (s SidebarLayout) cols(w int) int {
	full := s.clamp().Width
	if s.Slim || w-full < sideRoom {
		return min(sideSlim, max(w-1, 1))
	}
	return full
}

// full says whether the sidebar is at its full width in a window w wide.
func (s SidebarLayout) full(w int) bool { return s.cols(w) > sideSlim }

// sideKey applies a sidebar key ({ narrower, } wider, b slim strip on or
// off) to s in a window w wide; msg says why nothing changed.
func (s SidebarLayout) sideKey(key string, w int) (out SidebarLayout, msg string) {
	s = s.clamp()
	switch key {
	case "b":
		s.Slim = !s.Slim
		if !s.Slim && !s.full(w) {
			msg = fmt.Sprintf("the window is too narrow for the full sidebar (it needs %d columns)", s.Width+sideRoom)
		}
		return s, msg
	case "{", "}":
		if s.Slim || !s.full(w) {
			s.Slim = false
			if !s.full(w) {
				return s, fmt.Sprintf("the window is too narrow for the full sidebar (it needs %d columns)", s.Width+sideRoom)
			}
			return s, ""
		}
		d := sideStep
		if key == "{" {
			d = -d
		}
		// Never wider than the window allows: the panes keep sideRoom.
		s.Width = min(max(s.Width+d, sideMin), sideMax, max(w-sideRoom, sideMin))
	}
	return s, ""
}

// dragTo is s with its border dragged to column x of a window w wide.
func (s SidebarLayout) dragTo(x, w int) SidebarLayout {
	s.Slim = x+1 <= sideSlim
	if !s.Slim {
		s.Width = min(max(x+1, sideMin), sideMax, max(w-sideRoom, sideMin))
	}
	return s.clamp()
}

// sideProject is a project as the sidebar lists it.
type sideProject struct {
	slug    string
	threads int // open (unresolved) threads
}

// sideItem is one sidebar line: a project and its coordinator's state.
type sideItem struct {
	sideProject
	state  string // the coordinator's state, "" without one
	remote bool   // its coordinator's remote control is on
}

// sideItems joins the projects with their coordinators' sessions.
func sideItems(ps []sideProject, sessions []proto.SessionInfo) []sideItem {
	out := make([]sideItem, 0, len(ps))
	for _, p := range ps {
		it := sideItem{sideProject: p}
		for _, s := range sessions {
			if s.Role == proto.RoleCoordinator && s.Project == p.slug {
				it.state, it.remote = stateWord(s), s.RemoteControl
				break
			}
		}
		out = append(out, it)
	}
	return out
}

// sideProjectsOf is the dashboard's projects as the sidebar lists them.
func sideProjectsOf(ps []ProjectData) []sideProject {
	out := make([]sideProject, 0, len(ps))
	for _, p := range ps {
		out = append(out, sideProject{slug: p.Slug, threads: len(p.Threads)})
	}
	return out
}

// loadSideProjects reads the projects and their open threads from the
// project folders: what the attach client polls for its sidebar.
func loadSideProjects() []sideProject {
	list, _ := project.List()
	out := make([]sideProject, 0, len(list))
	for _, sum := range list {
		sp := sideProject{slug: sum.Slug}
		if p, err := project.Open(sum.Slug); err == nil {
			recs, _ := thread.List(p)
			for _, r := range recs {
				if r.State != thread.Resolved {
					sp.threads++
				}
			}
		}
		out = append(out, sp)
	}
	return out
}

// sideTop is the first item shown in h rows (one is the header), so that
// the current project shows.
func sideTop(items []sideItem, current string, h int) int {
	cur := -1
	for i, it := range items {
		if it.slug == current {
			cur = i
		}
	}
	return scrollTop(cur, max(h-1, 1), len(items))
}

// sidebarLines draws the sidebar w columns wide (its border included) and
// h rows tall. current is highlighted.
func sidebarLines(items []sideItem, current string, w, h int) []string {
	cw := max(w-1, 0) // less the border
	slim := w <= sideSlim
	border := styleFaint.Render("│")
	out := make([]string, 0, h)
	head := " PROJECTS"
	if slim {
		head = " PRJ"
	}
	out = append(out, styleTitle.Render(fit(countLabel(head, len(items)), cw))+reset+border)
	if len(items) == 0 {
		note := " no projects"
		if slim {
			note = " —"
		}
		out = append(out, styleFaint.Render(fit(note, cw))+reset+border)
	}
	for _, it := range items[sideTop(items, current, h):] {
		if len(out) >= h {
			break
		}
		out = append(out, sideLine(it, it.slug == current, cw, slim)+reset+border)
	}
	for len(out) < h {
		out = append(out, strings.Repeat(" ", cw)+border)
	}
	return out[:h]
}

// sideLine is one project cw cells wide: "▸● alpha        2" when full,
// "▸●alph" in the slim strip. The current project is marked and in
// reverse video, so colour is never the only signal. A coordinator with
// remote control on gets "⌁" after the name, in either width.
func sideLine(it sideItem, current bool, cw int, slim bool) string {
	mark := " "
	if current {
		mark = "▸"
	}
	g, st := stateLook(it.state)
	if it.state == "" {
		g, st = "·", styleFaint // no coordinator runs
	}
	rc := ""
	if it.remote {
		rc = remoteMark
	}
	if slim {
		name := []rune(it.slug)
		name = name[:min(len(name), max(cw-2-len([]rune(rc)), 0))]
		if current {
			return styleSel.Bold(true).Render(fit(mark+g+string(name)+rc, cw))
		}
		return fit(mark+st.Render(g)+string(name)+rc, cw)
	}
	count := fmt.Sprintf(" %d ", it.threads)
	nw := max(cw-3-len(count), 1)
	name := fit(it.slug, nw)
	if rc != "" {
		name = fit(ansi.Truncate(it.slug, max(nw-1, 0), "…")+rc, nw)
	}
	if current {
		return styleSel.Bold(true).Render(fit(mark+g+" "+name+count, cw))
	}
	cnt := styleFaint.Render(count)
	if it.threads > 0 {
		cnt = count
	}
	return fit(mark+st.Render(g)+" "+name+cnt, cw)
}

// sideHit is what column x, row y of the sidebar holds: the border, or a
// project's slug.
func sideHit(items []sideItem, current string, w, h, x, y int) (slug string, border bool) {
	if x == w-1 {
		return "", true
	}
	i := sideTop(items, current, h) + y - 1
	if y >= 1 && x >= 0 && x < w-1 && i < len(items) {
		return items[i].slug, false
	}
	return "", false
}

// sidebar is the attach client's sidebar: its layout and what it shows.
// Guarded by client.mu.
type sidebar struct {
	layout  SidebarLayout
	uiFile  string
	current string // the dashboard's project
	items   []sideItem
	drag    bool     // the mouse is moving its border
	drawn   []string // the lines on screen; nil repaints them all
}

// sideCurrent is the project the attach view's sidebar highlights: the
// focused pane's, else the one the dashboard was on. c.mu held.
func (c *client) sideCurrent() string {
	if c.focus != nil && c.focus.info.Project != "" {
		return c.focus.info.Project
	}
	return c.side.current
}

// appendSidebar draws the sidebar's changed lines. c.mu held.
func (c *client) appendSidebar(b []byte, wrote bool) ([]byte, bool) {
	lines := sidebarLines(c.side.items, c.sideCurrent(), c.sideW, c.rows)
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

// setSideLayout changes the sidebar's layout: a layout change, so the
// panes whose rectangle changed are resized (docs/SPEC.md §3.3). It
// returns those sizes and whether ui.json needs saving. c.mu held.
func (c *client) setSideLayout(l SidebarLayout) ([]resize, bool) {
	if l == c.side.layout {
		return nil, false
	}
	c.side.layout = l
	c.setWindow(c.cols, c.rows)
	return c.relayout(true), true
}

// saveSide writes the sidebar's width to ui.json; a failure shows in the
// status bar.
func (c *client) saveSide() {
	if !c.lock() {
		return
	}
	path, l := c.side.uiFile, c.side.layout
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
	l, msg := c.side.layout.sideKey(key, c.cols)
	sizes, save := c.setSideLayout(l)
	if msg != "" {
		c.flash = msg
		c.status()
	}
	c.mu.Unlock()
	c.sendSizes(sizes)
	if save {
		c.saveSide()
	}
	c.poke()
}

// sideMouse handles the mouse over the sidebar, or dragging its border: a
// click on a project goes back to the dashboard, which opens that
// project's coordinator. c.mu held; released here.
func (c *client) sideMouse(m emu.Mouse) {
	sd := c.side
	switch {
	case sd.drag && m.Action == emu.MouseMotion:
		sizes, _ := c.setSideLayout(sd.layout.dragTo(m.X, c.cols))
		c.mu.Unlock()
		c.sendSizes(sizes)
		c.poke()
		return
	case sd.drag && m.Action == emu.MouseRelease:
		sd.drag = false
		c.mu.Unlock()
		c.saveSide()
		return
	case m.Action != emu.MousePress || m.Button != emu.MouseLeft:
		c.mu.Unlock()
		return
	}
	slug, border := sideHit(sd.items, c.sideCurrent(), c.sideW, c.rows, m.X, m.Y)
	switch {
	case border:
		sd.drag = true
	case slug == "":
	case c.focus.info.Role == proto.RoleCoordinator && c.focus.info.Project == slug:
		c.flash = "you are on " + slug + "'s coordinator"
		c.status()
	default:
		c.mu.Unlock()
		c.detachThen(ThenOpen + slug)
		return
	}
	c.mu.Unlock()
	c.poke()
}
