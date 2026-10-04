package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/x/ansi"

	"github.com/theclifmeister/termalator/internal/emu"
	"github.com/theclifmeister/termalator/internal/project"
	"github.com/theclifmeister/termalator/internal/proto"
	"github.com/theclifmeister/termalator/internal/thread"
	"github.com/theclifmeister/termalator/internal/view"
)

// The projects sidebar (docs/SPEC.md §4): a column on the left of every
// screen, the dashboard and the attach view alike, listing every project
// with its coordinator's state glyph and its count of open threads. The
// current project is highlighted; a click opens a project's coordinator.
// Its border column can be dragged, and { } b change it from the keys.
//
// Its state is SidebarLayout, part of the server-owned view; the rest here
// only draws and hit-tests.

// ThenOpen, then a project's slug, is the dashboard key (Result.Then) of
// a click on the sidebar while attached: open that project's coordinator.
const ThenOpen = "open:"

// SidebarLayout is the sidebar's layout: part of the server-owned view
// (docs/SPEC.md §3.3), and kept in ui.json as the layout new views start
// with.
type SidebarLayout = view.Sidebar

const (
	sideDefault = view.SideDefault
	sideSlim    = view.SideSlim
	sideRoom    = view.SideRoom
)

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

// sidebar is the attach client's sidebar: what it shows. Its layout is
// the view's. Guarded by client.mu.
type sidebar struct {
	uiFile string
	items  []sideItem
	drag   bool     // the mouse is moving its border
	drawn  []string // the lines on screen; nil repaints them all
}

// sideCurrent is the project the attach view's sidebar highlights: the
// focused pane's, else the view's current one. c.mu held.
func (c *client) sideCurrent() string {
	if c.focus != nil && c.focus.info.Project != "" {
		return c.focus.info.Project
	}
	return c.v.Current
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

// sideMouse handles the mouse over the sidebar, or dragging its border: a
// click on a project goes back to the dashboard, which opens that
// project's coordinator. c.mu held; released here.
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
	slug, border := sideHit(sd.items, c.sideCurrent(), c.sideW, c.rows, m.X, m.Y)
	switch {
	case border:
		sd.drag = true
	case slug == "":
	case c.focus != nil && c.focus.info.Role == proto.RoleCoordinator && c.focus.info.Project == slug:
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
