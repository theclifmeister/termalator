package tui

import (
	"strings"
	"time"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// The mouse on the dashboard (docs/SPEC.md §4): every key has a mouse
// path. A click selects a row and a double-click opens it; the wheel
// moves through lists and scrolls the details panel and long popups; the
// footer's hints are buttons; a popup's tabs, rows and settings take
// clicks, and a click outside it closes it; a right-click on
// a row opens a menu of that row's actions, and the ≡ menu has every
// action of the keys table (actions.go), so none lacks a mouse path.
// Popups and menus are this console's own; what they do acts on the
// view, as the keys do.

// menuButton opens the menu of every action: the first hint of the
// dashboard's footer.
const menuButton = "≡"

// doubleClick is the most time between the clicks of a double-click.
const doubleClick = 400 * time.Millisecond

// click is a left click, remembered to spot a double-click.
type click struct {
	x, y int
	at   time.Time
}

// double records a left click at (x, y) and says whether it completes a
// double-click: a second click on the same line, near the first, soon.
func (c *click) double(x, y int, now time.Time) bool {
	d := !c.at.IsZero() && now.Sub(c.at) < doubleClick && c.y == y && max(c.x-x, x-c.x) <= 2
	if d {
		c.at = time.Time{} // a third click starts anew
	} else {
		*c = click{x, y, now}
	}
	return d
}

// hint is a button in a line of hints: cells [x0, x1) press key.
type hint struct {
	x0, x1 int
	key    string
}

// hints are the buttons of a key list as keysLine draws it from column
// x: "enter attach · a project" has enter at the first, a at the second.
// A hint naming several keys ("p ] [ projects") has a button per key, its
// words go to the first; hints whose first word isn't a key (↑ ↓ scroll,
// 1-5 pick one) are no buttons.
func hints(keys string, x int) []hint {
	var out []hint
	for i, p := range strings.Split(keys, " · ") {
		if i > 0 {
			x += 3
		}
		w := ansi.StringWidth(p)
		var own []hint
		cx := x
		for _, t := range strings.Split(p, " ") {
			k := hintKey(t)
			if k == "" {
				break
			}
			tw := ansi.StringWidth(t)
			own = append(own, hint{cx, cx + tw, k})
			cx += tw + 1
		}
		switch {
		case len(own) == 1:
			out = append(out, hint{x, x + w, own[0].key})
		case len(own) > 1:
			out = append(out, own...)
			if cx < x+w {
				out = append(out, hint{cx, x + w, own[0].key})
			}
		}
		x += w
	}
	return out
}

// hintKey is the key a hint's first word names, "" for none.
func hintKey(k string) string {
	switch k {
	case "any": // "any other key no"
		return "n"
	case "enter", "esc", "tab", "shift+tab", "space", "ctrl+u", menuButton:
		return k
	case "↑", "↓":
		return ""
	}
	if utf8.RuneCountInString(k) == 1 {
		return k
	}
	return ""
}

// hintAt is the key of the button at column x, "" for none.
func hintAt(hs []hint, x int) string {
	for _, h := range hs {
		if x >= h.x0 && x < h.x1 {
			return h.key
		}
	}
	return ""
}

// keyMsg is the key press a key's name stands for, as tea names keys.
func keyMsg(name string) tea.KeyPressMsg {
	switch name {
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	case "esc":
		return tea.KeyPressMsg{Code: tea.KeyEscape}
	case "tab":
		return tea.KeyPressMsg{Code: tea.KeyTab}
	case "shift+tab":
		return tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift}
	case "space":
		return tea.KeyPressMsg{Code: tea.KeySpace, Text: " "}
	case "up":
		return tea.KeyPressMsg{Code: tea.KeyUp}
	case "down":
		return tea.KeyPressMsg{Code: tea.KeyDown}
	}
	if c, ok := strings.CutPrefix(name, "ctrl+"); ok {
		r, _ := utf8.DecodeRuneInString(c)
		return tea.KeyPressMsg{Code: r, Mod: tea.ModCtrl}
	}
	r, _ := utf8.DecodeRuneInString(name)
	return tea.KeyPressMsg{Code: r, Text: name}
}

// arrow is the key the wheel stands for: up for d < 0, else down.
func arrow(d int) tea.KeyPressMsg {
	if d < 0 {
		return keyMsg("up")
	}
	return keyMsg("down")
}

// A clicker is an overlay with clickable lines: item is what the line's
// hit names, col the column within the box's text.
type clicker interface {
	click(m *dash, item, col int, double bool) tea.Cmd
}

// A wheeler is an overlay the wheel moves through (d is -1 or 1).
type wheeler interface {
	wheel(m *dash, d int)
}

// mouse handles a mouse event anywhere in the window.
func (m *dash) mouse(msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case tea.MouseClickMsg:
		return m.press(msg.Mouse())
	case tea.MouseMotionMsg:
		x := msg.Mouse().X
		switch {
		case m.sideDrag:
			m.layout.Sidebar = m.layout.Sidebar.DragTo(x, m.winW)
			m.setWidth(m.winW)
		case m.dragging:
			m.layout.Split = clampSplit(float64(x-m.sideW()) / float64(max(m.w, 1)))
		}
	case tea.MouseReleaseMsg:
		if m.dragging || m.sideDrag {
			m.dragging, m.sideDrag = false, false
			m.saveLayout()
		}
	case tea.MouseWheelMsg:
		mo := msg.Mouse()
		d := 0
		switch mo.Button {
		case tea.MouseWheelUp:
			d = -1
		case tea.MouseWheelDown:
			d = 1
		}
		if d != 0 && mo.X >= m.sideW() {
			m.wheel(mo.X-m.sideW(), mo.Y, d)
		}
	}
	return nil
}

// press handles a button pressed: on the sidebar, the footer's hints,
// the topmost popup, else the list.
func (m *dash) press(mo tea.Mouse) tea.Cmd {
	if mo.X < m.sideW() {
		if mo.Button == tea.MouseRight {
			return m.sideMenu(mo)
		}
		return m.sideClick(mo)
	}
	x, y := mo.X-m.sideW(), mo.Y
	double := mo.Button == tea.MouseLeft && m.lastClick.double(x, y, time.Now())
	if y == m.h-2 {
		if mo.Button == tea.MouseLeft {
			return m.hintClick(x)
		}
		return nil
	}
	if o := m.top(); o != nil {
		return m.popupClick(o, mo.Button, x, y-1, double)
	}
	switch mo.Button {
	case tea.MouseLeft:
		// A click gives its area the keyboard.
		m.focus = focusList
		if split, lw := m.split(); split && x > lw {
			m.focus = focusDetails
		}
		return m.click(x, y, double)
	case tea.MouseRight:
		return m.rowMenu(x, y)
	}
	return nil
}

// hintClick presses the footer's button at column x.
func (m *dash) hintClick(x int) tea.Cmd {
	switch key := hintAt(hints(m.shownKeys, 1), x); key {
	case "":
	case menuButton:
		m.openMenu("", m.dashItems(), x, m.bodyRows())
	default:
		return m.key(keyMsg(key))
	}
	return nil
}

// popupClick handles a button pressed on body cell (x, y) while o is the
// topmost overlay: a click outside closes it, as esc does; a
// click on a line goes to the overlay.
func (m *dash) popupClick(o overlay, btn tea.MouseButton, x, y int, double bool) tea.Cmd {
	g := m.geo
	if g == nil || y < 0 || y >= m.bodyRows() {
		return nil
	}
	if !g.inside(x, y) {
		return o.key(m, keyMsg("esc"))
	}
	c, ok := o.(clicker)
	li := g.line(y)
	if !ok || btn != tea.MouseLeft || li < 0 || li >= len(g.hits) || g.hits[li] == noHit {
		return nil
	}
	return c.click(m, g.hits[li], x-g.x-2, double)
}

// wheel moves through what is under body cell (x, y) by d: the topmost
// popup, the details panel, else the list.
func (m *dash) wheel(x, y, d int) {
	if o := m.top(); o != nil {
		if w, ok := o.(wheeler); ok {
			w.wheel(m, d)
		}
		return
	}
	if split, lw := m.split(); split && x > lw {
		m.scrollDetails(3 * d)
		return
	}
	m.move(d)
}

// scrollDetails scrolls the details panel by d lines.
func (m *dash) scrollDetails(d int) {
	if m.detailKey != m.sel {
		m.detailKey, m.detailTop = m.sel, 0
	}
	m.detailTop = max(m.detailTop+d, 0)
}

// rowAt is the key of the list's row at window row y ("" for none).
func (m *dash) rowAt(x, y int) string {
	split, lw := m.split()
	if split && x >= lw {
		return ""
	}
	_, keys, sel := m.listLines(lw, !split)
	i := scrollTop(sel, m.bodyRows(), len(keys)) + y - 1
	if y < 1 || y > m.bodyRows() || i >= len(keys) {
		return ""
	}
	return keys[i]
}

// click selects the row under the mouse, opens it on a double-click, or
// starts dragging the divider.
func (m *dash) click(x, y int, double bool) tea.Cmd {
	if split, lw := m.split(); split && x == lw && y >= 1 && y <= m.bodyRows() {
		m.dragging = true
		return nil
	}
	key := m.rowAt(x, y)
	if key == "" {
		return nil
	}
	if double && key == m.sel {
		return m.key(keyMsg("enter"))
	}
	m.sel, m.userSel = key, true
	return nil
}

// rowMenu opens the menu of the row under a right-click, which it
// selects; elsewhere in the list, the menu of every action.
func (m *dash) rowMenu(x, y int) tea.Cmd {
	key := m.rowAt(x, y)
	if key == "" {
		if y >= 1 && y <= m.bodyRows() {
			m.openMenu("", m.dashItems(), x, y-1)
		}
		return nil
	}
	m.sel, m.userSel = key, true
	if r, ok := m.selected(); ok {
		m.openMenu(oneLine(rowTitle(r)), m.rowItems(r), x, y-1)
	}
	return nil
}

// rowTitle names a row in its menu's title.
func rowTitle(r row) string {
	switch {
	case r.task != nil:
		return r.project + " " + r.task.Ref()
	case r.thread != nil:
		return r.thread.ID
	case strings.HasPrefix(r.key, "p:"):
		return r.project + " coordinator"
	case r.session != "":
		return r.session
	}
	return r.what
}

// menuItem is a line of a menu: its label, the key that does the same
// (shown beside it, "" for none) and what picking it does.
type menuItem struct {
	label, key string
	run        func(m *dash) tea.Cmd
}

// pressItem is an item that presses key.
func pressItem(label, key string) menuItem {
	return menuItem{label: label, key: key, run: func(m *dash) tea.Cmd { return m.key(keyMsg(key)) }}
}

// dashItems are the ≡ menu's items: every action of the keys table that
// has a menu entry, in the table's order.
func (m *dash) dashItems() []menuItem {
	var out []menuItem
	for _, a := range actions {
		for i, word := range a.menu {
			if word != "" && i < len(a.keys) {
				out = append(out, pressItem(word, a.keys[i]))
			}
		}
	}
	return out
}

// enterWord is what enter does on r (attach, open), "" nothing.
func (m *dash) enterWord(r row) string {
	for _, a := range actions {
		if a.keys[0] == "enter" {
			return a.foot(m, r, true)
		}
	}
	return ""
}

// rowItems are the menu of a list row: open it, and the project's popup,
// tasks and inbox. Each
// selects the row again first, should the view have moved it.
func (m *dash) rowItems(r row) []menuItem {
	on := func(it menuItem) menuItem {
		run := it.run
		it.run = func(m *dash) tea.Cmd {
			m.sel, m.userSel = r.key, true
			return run(m)
		}
		return it
	}
	var out []menuItem
	if word := m.enterWord(r); word != "" {
		out = append(out, on(pressItem(word, "enter")))
	}
	if r.project != "" {
		out = append(out, on(pressItem("project popup", "a")), on(pressItem("tasks", "t")), on(pressItem("inbox", "i")))
	}
	return out
}

// sideMenu opens the menu of the sidebar row under a right-click.
func (m *dash) sideMenu(mo tea.Mouse) tea.Cmd {
	r, ok, _ := sideHitAt(m.tree(), m.sideW(), m.h, mo.X, mo.Y)
	if !ok {
		return nil
	}
	t, can, why := r.target()
	if !can {
		m.msg = why
		return nil
	}
	m.stack = nil
	title := r.slug
	var items []menuItem
	// show runs a dashboard key on the row's project.
	show := func(label, key string) menuItem {
		return menuItem{label: label, key: key, run: func(m *dash) tea.Cmd {
			return tea.Batch(m.showProject(r.slug), m.key(keyMsg(key)))
		}}
	}
	switch r.kind {
	case treeProject:
		items = append(items,
			menuItem{label: "show its dashboard", run: func(m *dash) tea.Cmd { return m.showProject(r.slug) }},
			menuItem{label: "open its coordinator", run: func(m *dash) tea.Cmd { return m.openProject(r.slug) }})
		items = append(items, show("project popup", "a"), show("tasks", "t"), show("inbox", "i"))
	case treeCoordinator:
		title += " coordinator"
		items = append(items, menuItem{label: "open the coordinator", key: "enter", run: func(m *dash) tea.Cmd { return m.openProject(r.slug) }},
			show("project popup", "a"))
	default:
		title = r.thread
		items = append(items,
			menuItem{label: "attach", key: "enter", run: func(m *dash) tea.Cmd {
				return m.act(func() actionMsg { return actionMsg{attach: t.Session, current: t.Project} })
			}})
	}
	m.openMenu(title, items, 0, mo.Y-1)
	return nil
}

// menuView is a menu: picked with a click, or the arrows and enter.
type menuView struct {
	title string
	items []menuItem
	sel   int
	at    [2]int
}

// openMenu opens a menu with its top left corner at body cell (x, y).
func (m *dash) openMenu(title string, items []menuItem, x, y int) {
	if len(items) > 0 {
		m.push(&menuView{title: title, items: items, at: [2]int{x, y}})
	}
}

func (mv *menuView) key(m *dash, k tea.KeyPressMsg) tea.Cmd {
	switch k.String() {
	case "up", "k":
		mv.sel = moveSel(mv.sel, -1, len(mv.items))
	case "down", "j":
		mv.sel = moveSel(mv.sel, 1, len(mv.items))
	case "enter", "space":
		return mv.pick(m, mv.sel)
	default:
		m.close(mv)
	}
	return nil
}

// pick closes the menu and runs item i.
func (mv *menuView) pick(m *dash, i int) tea.Cmd {
	m.close(mv)
	if i < 0 || i >= len(mv.items) || m.busy {
		return nil
	}
	return mv.items[i].run(m)
}

func (mv *menuView) click(m *dash, item, _ int, _ bool) tea.Cmd {
	mv.sel = item
	return mv.pick(m, item)
}

func (mv *menuView) wheel(m *dash, d int) { mv.key(m, arrow(d)) }

func (mv *menuView) render(m *dash) string { return m.popup(mv.box()) }

func (mv *menuView) box() box {
	lw, kw := 0, 0
	for _, it := range mv.items {
		lw, kw = max(lw, ansi.StringWidth(it.label)), max(kw, ansi.StringWidth(it.key))
	}
	w := lw
	if kw > 0 {
		w += 2 + kw
	}
	w = max(w, ansi.StringWidth(mv.title)+2)
	lines := make([]string, len(mv.items))
	hits := make([]int, len(mv.items))
	for i, it := range mv.items {
		hits[i] = i
		key := fit(it.key, kw)
		if i == mv.sel {
			lines[i] = styleSel.Render(fit(fit(it.label, lw)+"  "+key, w))
			continue
		}
		lines[i] = fit(it.label, lw) + "  " + styleFaint.Render(key)
	}
	return box{title: mv.title, body: lines, sel: mv.sel, hits: hits, at: &mv.at,
		keys: "enter pick · esc close", width: w + 4}
}
