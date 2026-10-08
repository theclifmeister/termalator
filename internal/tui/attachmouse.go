package tui

import (
	"fmt"
	"unicode/utf8"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"

	"github.com/theclifmeister/terminatr/internal/emu"
	"github.com/theclifmeister/terminatr/internal/proto"
)

// The mouse in a session (docs/SPEC.md §4). Inside a pane the program
// gets the mouse whenever it tracks it (Claude Code does); everything
// else is tm's: the sidebar, the status bar's buttons (the ≡ menu,
// prefix+d dashboard, a question's y yes) and a pane
// whose program doesn't take the mouse, where a right-click opens the
// menu. The ≡ menu has every prefix command
// (sessionMenu), so none lacks a mouse path; a right-click on a sidebar
// row opens that row's menu. Menus are this console's own; what they do
// are the same view actions the keys run.

// sessionMenu is the ≡ menu of a session: every prefix command, in the
// help's order (sessionKeys in keymap.go).
var sessionMenu = []struct{ label, key string }{
	{"dashboard", "d"},
	{"quit this console", "q"},
	{"project popup", "a"},
	{"new project", "n"},
	{"next project", "]"},
	{"previous project", "["},
	{"inbox", "i"},
	{"tasks", "t"},
	{"settings", ","},
	{"help: keys and mouse", "?"},
	{"narrower sidebar", "{"},
	{"wider sidebar", "}"},
	{"slim sidebar on / off", "b"},
	{"info panel on / off", "|"},
	{"keyboard to the sidebar", "tab"},
	{"remote control on / off…", "r"},
	{"send the prefix key", "prefix"},
}

// cmdKey is the key a prefix command's name stands for.
func cmdKey(name string) uv.Key {
	if name == "tab" {
		return uv.Key{Code: uv.KeyTab}
	}
	r, _ := utf8.DecodeRuneInString(name)
	return uv.Key{Code: r, Text: name}
}

// command runs a prefix command as if typed after the prefix: what a
// button or a menu item does. "prefix" sends the prefix key to the
// program, "menu" opens the ≡ menu over the status bar.
func (c *client) command(name string) {
	switch name {
	case "prefix":
		c.input(uv.Key{Code: c.prefix.r, Mod: uv.ModCtrl})
		return
	case "menu":
		if c.lock() {
			c.openMenu("", c.sessionItems(), c.sideW, c.rows-1, true)
			c.mu.Unlock()
			c.poke()
		}
		return
	}
	c.run(prefixStep(c.prefix, true, cmdKey(name), c.dashboard))
}

// amenu is a menu, or a dialog (text and no items), drawn over the
// window: the same box as the dashboard's popups, its keys in its own
// action row.
type amenu struct {
	title string
	items []aitem
	// text is a dialog's question, wrapped to its width w.
	text  []string
	w     int
	h     int // a dialog's height, fixed; its text fills it
	sel   int
	x, y  int  // its top left cell
	drawn bool // on screen as it is
}

// menuActs are a menu's keys, in its action row.
const menuActs = "enter pick · esc close"

// acts are the box's action rows.
func (mn *amenu) acts() []string {
	if len(mn.text) > 0 {
		return actionRows(confirmKeys, mn.w-4)
	}
	lw, kw := mn.widths()
	return actionRows(menuActs, max(lw+2+kw, ansi.StringWidth(menuActs)))
}

// action is the key of the action row's button at window cell (x, y),
// "" for none.
func (mn *amenu) action(x, y int) string {
	_, h := mn.size()
	acts := mn.acts()
	r := y - mn.y - (h - 1 - len(acts)) // the action rows end above the bottom border
	if r >= 0 && r < len(acts) {
		return hintAt(hints(acts[r], mn.x+2), x)
	}
	return ""
}

// aitem is a menu line: its label, the keys that do the same, and what
// picking it does (run without c.mu).
type aitem struct {
	label, key string
	run        func()
}

// size is the box: its width and height.
func (mn *amenu) size() (int, int) {
	h := len(mn.items) + len(mn.text) + 2 + 1 + len(mn.acts())
	if len(mn.text) > 0 {
		return mn.w, max(h, mn.h)
	}
	lw, kw := mn.widths()
	w := lw
	if kw > 0 {
		w += 2 + kw
	}
	w = max(w, ansi.StringWidth(mn.title)+2, ansi.StringWidth(menuActs))
	return w + 4, h
}

func (mn *amenu) widths() (lw, kw int) {
	for _, it := range mn.items {
		lw, kw = max(lw, ansi.StringWidth(it.label)), max(kw, ansi.StringWidth(it.key))
	}
	return lw, kw
}

// lines draws the box: the items (the keys right-aligned, in the accent
// colour) or the text, then the action row.
func (mn *amenu) lines() []string {
	bw, _ := mn.size()
	if len(mn.text) > 0 {
		text := mn.text
		_, h := mn.size()
		for len(text)+2+1+len(mn.acts()) < h {
			text = append(text[:len(text):len(text)], "")
		}
		return drawBox(mn.title, bw, text, mn.acts(), "")
	}
	_, kw := mn.widths()
	inner := bw - 4
	var rows []string
	for i, it := range mn.items {
		key := fmt.Sprintf("%*s", kw, it.key)
		l := fit(it.label, inner-kw) + styleAccent.Render(key)
		if i == mn.sel {
			l = styleSel.Render(fit(it.label, inner-kw) + key)
		}
		rows = append(rows, l)
	}
	title := mn.title
	if title == "" {
		title = "Menu"
	}
	return drawBox(title, bw, rows, mn.acts(), "")
}

// at is the item at window cell (x, y): -1 on the border; in is false
// outside the box.
func (mn *amenu) at(x, y int) (i int, in bool) {
	w, h := mn.size()
	if x < mn.x || x >= mn.x+w || y < mn.y || y >= mn.y+h {
		return -1, false
	}
	if i = y - mn.y - 1; i < 0 || i >= len(mn.items) || x == mn.x || x == mn.x+w-1 {
		return -1, true
	}
	return i, true
}

// openMenu opens a menu at window cell (x, y), kept in the window; above
// puts its bottom edge on the row above y (the status bar's menus). c.mu
// held.
func (c *client) openMenu(title string, items []aitem, x, y int, above bool) {
	if len(items) == 0 {
		return
	}
	mn := &amenu{title: title, items: items}
	w, h := mn.size()
	if above {
		y -= h
	}
	mn.x = max(min(x, c.cols-w), 0)
	mn.y = max(min(y, c.rows-h), 0)
	c.menu = mn
}

// closeMenu takes the menu, or the dialog, away: everything under it is
// drawn again. c.mu held.
func (c *client) closeMenu() {
	c.menu = nil
	c.dialog = nil
	c.full = true
	for _, p := range c.shown() {
		p.r.Invalidate()
	}
}

// appendMenu draws the menu over the frame, the cursor hidden meanwhile.
// c.mu held.
func (c *client) appendMenu(b []byte, mn *amenu) []byte {
	b = append(b, "\x1b[?2026h"...)
	for i, l := range mn.lines() {
		b = append(b, fmt.Sprintf("\x1b[%d;%dH\x1b[0m", mn.y+i+1, mn.x+1)...)
		b = append(b, l...)
		b = append(b, "\x1b[0m"...)
	}
	mn.drawn = true
	return append(b, "\x1b[?25l\x1b[?2026l"...)
}

// menuKey handles a key while the menu is open: the arrows move, enter
// or space picks, any other key closes it. c.mu held; released here.
func (c *client) menuKey(k uv.Key) {
	mn := c.menu
	var run func()
	switch keyName(k) {
	case "up", "k":
		mn.sel, mn.drawn = moveSel(mn.sel, -1, len(mn.items)), false
	case "down", "j":
		mn.sel, mn.drawn = moveSel(mn.sel, 1, len(mn.items)), false
	case "enter", "space":
		run = mn.items[mn.sel].run
		c.closeMenu()
	default:
		c.closeMenu()
	}
	c.mu.Unlock()
	if run != nil {
		run()
	}
	c.poke()
}

// menuMouse handles the mouse while the menu is open: a click on an item
// picks it, anywhere else closes the menu; the wheel moves. c.mu held;
// released here.
func (c *client) menuMouse(m emu.Mouse) {
	mn := c.menu
	var run func()
	switch {
	case m.Action != emu.MousePress:
	case m.Button == emu.MouseWheelUp, m.Button == emu.MouseWheelDown:
		d := map[bool]int{true: -1, false: 1}[m.Button == emu.MouseWheelUp]
		mn.sel, mn.drawn = moveSel(mn.sel, d, len(mn.items)), false
	default:
		i, in := mn.at(m.X, m.Y)
		if in && i < 0 {
			// The action row's buttons; elsewhere the border.
			switch mn.action(m.X, m.Y) {
			case "enter":
				run = mn.items[mn.sel].run
				c.closeMenu()
			case "esc":
				c.closeMenu()
			}
			break
		}
		if in && m.Button == emu.MouseLeft {
			run = mn.items[i].run
		}
		c.closeMenu()
	}
	c.mu.Unlock()
	if run != nil {
		run()
	}
	c.poke()
}

// sessionItems are the ≡ menu's items for the focused pane: the prefix
// commands, less those that don't apply (the dashboard's in a bare view,
// remote control off a coordinator).
// c.mu held.
func (c *client) sessionItems() []aitem {
	var out []aitem
	for _, e := range sessionMenu {
		key, label := e.key, e.label
		switch {
		case prefixCommands[key] && !c.dashboard:
			continue
		case key == "r" && (c.focus == nil || c.focus.info.Role != proto.RoleCoordinator):
			continue
		case key == "d" && c.bare:
			label = "leave"
		}
		out = append(out, aitem{label: label, key: "prefix+" + key, run: func() { c.command(key) }})
	}
	return out
}

// sideMenu opens the menu of the sidebar row at (m.X, m.Y): show the
// project's dashboard, open its coordinator,
// its popup, tasks and inbox; attach a thread. c.mu held; released here.
func (c *client) sideMenu(m emu.Mouse) {
	r, ok, _ := sideHitAt(c.sideTree(), c.sideW, c.rows, m.X, m.Y)
	t, can, why := r.target()
	switch {
	case !ok:
	case !can:
		c.flash = why
		c.status()
	default:
		c.openMenu(c.sideTitle(r), c.sideItems(r, t), m.X, m.Y, false)
	}
	c.mu.Unlock()
	c.poke()
}

// sideTitle names a sidebar row in its menu.
func (c *client) sideTitle(r treeRow) string {
	switch r.kind {
	case treeCoordinator:
		return r.slug + " coordinator"
	case treeThread:
		return r.thread
	}
	return r.slug
}

// sideItems are a sidebar row's menu items. c.mu held.
func (c *client) sideItems(r treeRow, t Target) []aitem {
	var out []aitem
	add := func(label, key string, run func()) { out = append(out, aitem{label: label, key: key, run: run}) }
	popups := func() {
		if !c.dashboard {
			return
		}
		add("project popup", "prefix+a", func() { c.popupOver(r.slug, "a") })
		if r.kind == treeProject {
			add("tasks", "prefix+t", func() { c.popupOver(r.slug, "t") })
			add("inbox", "prefix+i", func() { c.popupOver(r.slug, "i") })
		}
	}
	switch r.kind {
	case treeProject:
		add("show its dashboard", "", func() { c.sideGo(Target{Project: r.slug}) })
		add("open its coordinator", "", func() { c.sideGo(Target{Project: r.slug, Coordinator: true}) })
		popups()
	case treeCoordinator:
		add("open the coordinator", "", func() { c.sideGo(t) })
		popups()
	default:
		add("attach", "", func() { c.attachThread(t) })
	}
	return out
}

// attachThread shows a thread's session in the view.
func (c *client) attachThread(t Target) {
	if !c.lock() {
		return
	}
	if c.panes[t.Session] == nil {
		c.mu.Unlock()
		c.sideGo(t)
		return
	}
	c.flash = "you are on it"
	c.status()
	c.mu.Unlock()
	c.poke()
}

// statusMouse handles a press on the status bar: its buttons run their
// command, a right-click opens the ≡ menu. c.mu held; released here.
func (c *client) statusMouse(m emu.Mouse) {
	if m.Action != emu.MousePress {
		c.mu.Unlock()
		return
	}
	switch m.Button {
	case emu.MouseRight:
		c.openMenu("", c.sessionItems(), m.X, c.rows-1, true)
	case emu.MouseLeft:
		if key := hintAt(c.statusHits, m.X-c.sideW); key != "" {
			c.mu.Unlock()
			c.command(key)
			c.poke()
			return
		}
	}
	c.mu.Unlock()
	c.poke()
}

// openDialog opens a yes/no dialog over the session: title, question
// and the confirm keys in its action row, a dialog's fixed size, centred
// on the window. c.mu held.
func (c *client) openDialog(title, question string) {
	w := max(min(dialogWidth, c.cols-2), 8)
	d := &amenu{title: title, text: wrapLines(question, max(w-4, 4)), w: w, h: min(dialogHeight, max(c.rows-2, 5))}
	_, h := d.size()
	d.x = max((c.cols-w)/2, 0)
	d.y = max((c.rows-h)/2, 0)
	c.dialog = d
}
