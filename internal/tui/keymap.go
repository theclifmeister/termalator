package tui

import (
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// The keys list: every key, grouped, in the prefix+<key> hint style. The
// help (?) and the project popup's Keys tab both draw it from here, the
// dashboard's part from the actions table (actions.go), so none of them
// can disagree with what the keys do.

// keyHelp is one key's line: its keys and what they do.
type keyHelp struct{ keys, help string }

// keyGroup is a heading and its keys.
type keyGroup struct {
	title string
	keys  []keyHelp
}

// sessionKeys are the prefix commands in a session (attach.go), in the
// order the help lists them.
var sessionKeys = []keyHelp{
	{"prefix+d", "back to the dashboard, on every console of the view (the session keeps running)"},
	{"prefix+a", "back to the dashboard with the project popup open: overview, inbox, tasks, settings, keys"},
	{"prefix+p ] [", "back to the dashboard and switch project"},
	{"prefix+i t , ?", "back to the dashboard with the inbox, tasks, settings or help open"},
	{`prefix+% "`, "split the window: a new shell beside / below"},
	{"prefix+arrows o", "focus another pane"},
	{"prefix+ctrl+arrows", "resize: move the nearest divider (for half a second more, no prefix needed)"},
	{"prefix+z x space", "zoom the focused pane, close it (its session keeps running), switch the layout"},
	{"prefix+{ } b", "narrow / widen the sidebar, or make it a slim strip"},
	{"prefix+tab", "the keyboard to the projects sidebar (its keys below); esc or tab back to the pane, which gets no keys meanwhile"},
	{"prefix+u", "take over a watch-only thread pane and type into it (asks first; its coordinator is told)"},
	{"prefix+r", "turn remote control of a coordinator on or off, to continue it from another device (asks first)"},
	{"prefix+prefix", "send the prefix key itself to the program"},
}

// popupKeys are the project popup's own keys (projectView).
var popupKeys = []keyHelp{
	{"tab shift+tab", "next / previous tab; 1 to 5 or ← → pick one"},
	{"↑ ↓", "move in the tab"},
	{"enter space", "on a setting: change it (yolo mode asks first)"},
	{"+ x", "on the overview: add a repository / remove the selected one (asks first)"},
	{"esc", "close"},
}

// mouseKeys are the mouse's ways, in the help beside the keys: every
// key has one (TestEveryKeyHasMousePath).
var mouseKeys = []keyHelp{
	{"click", "a row selects it; a footer hint presses its key; a popup's tab, row or setting picks it (a number's − + step it); outside a popup, or its ×, closes it"},
	{"double-click", "a row opens it: a coordinator attaches, a thread watches, a task shows; in a session, a pane whose program doesn't take the mouse zooms, and back"},
	{"right-click", "a row of the list or the sidebar: a menu of its actions (open, watch, take over, its popup, …); in a session, the status bar or a pane that doesn't take the mouse: the session's menu"},
	{menuButton + " menu", "every action, first in the footer; in a session, every prefix command, on the status bar"},
	{"status bar", "│ ─ split beside / below, ⤢ zoom, × close the pane (its session keeps running; these four when the bar has room), prefix+d dashboard, prefix+u takes over, y yes"},
	{"sidebar", "▸ ▾ open or close a project; a project shows its dashboard, its coordinator attaches, a thread watches it"},
	{"wheel", "moves through a list; scrolls the details panel and long popups"},
	{"drag", "the divider beside the details panel, the sidebar's border, a divider between panes"},
	{"shift+drag", "select text, as usual; a pane whose program takes the mouse (Claude Code does) gets its clicks"},
}

// keyGroups is every key, grouped.
func keyGroups() []keyGroup {
	var dash []keyHelp
	for _, a := range actions {
		if a.label != "" {
			dash = append(dash, keyHelp{a.label, a.help})
		}
	}
	var side []keyHelp
	for _, a := range sideActions {
		if a.label != "" {
			side = append(side, keyHelp{a.label, a.help})
		}
	}
	return []keyGroup{
		{"On the dashboard", dash},
		{"With the mouse", mouseKeys},
		{"In a session", sessionKeys},
		{"In the sidebar (tab, or prefix+tab in a session)", side},
		{"In the project popup", popupKeys},
	}
}

// keyCol is the width of the keys column.
const keyCol = 19

// keyLines draws the keys list w cells wide: each help wraps under
// itself, so nothing is cut off.
func keyLines(w int) []string {
	var out []string
	for i, g := range keyGroups() {
		if i > 0 {
			out = append(out, "")
		}
		out = append(out, styleHead.Render(g.title))
		for _, k := range g.keys {
			out = append(out, keyLine(k, w)...)
		}
	}
	out = append(out, "")
	out = append(out, faintLines("On the dashboard, prefix+<key> is that key, so the same keys work in both places. The prefix is in the settings (,); inside tmux, which takes ctrl+b, pick another.", w)...)
	return append(out, faintLines("Every tm shows the same view: what one does, the others show, sized by the one typed in. tm --own keeps to itself. You talk to coordinators; they run the threads, their reports and the tasks.", w)...)
}

// keyLine is one key's lines: the keys in the accent colour, the help
// wrapped beside them.
func keyLine(k keyHelp, w int) []string {
	hw := max(w-keyCol-1, 10)
	help := strings.Split(ansi.Wordwrap(k.help, hw, ""), "\n")
	out := make([]string, 0, len(help))
	for i, h := range help {
		key := ""
		if i == 0 {
			key = k.keys
		}
		out = append(out, styleAccent.Render(fit(key, keyCol))+" "+h)
	}
	return out
}

// wrapLines is text wrapped to w cells.
func wrapLines(text string, w int) []string {
	return strings.Split(ansi.Wordwrap(text, max(w, 10), ""), "\n")
}

// faintLines is text wrapped to w cells, faint.
func faintLines(text string, w int) []string {
	lines := wrapLines(text, w)
	for i, l := range lines {
		lines[i] = styleFaint.Render(l)
	}
	return lines
}
