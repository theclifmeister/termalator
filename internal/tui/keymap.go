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

// sessionKeys are the prefix commands (attach.go, prefix.go), in the
// order the help lists them. Each means the same everywhere: in a
// session, on the dashboard, over any popup (which it closes first).
var sessionKeys = []keyHelp{
	{"prefix+d", "back to the dashboard, on every console of the view (the session keeps running); on the dashboard it closes the popups"},
	{"prefix+q", "quit this console, from anywhere: a session, the dashboard, a popup, the sidebar, tm attach (the server, the sessions and other consoles keep running)"},
	{"prefix+a i t , ?", "the project popup, inbox, tasks, settings or help, over the session or the dashboard; esc closes it"},
	{"prefix+p ] [", "the project switcher, the next / previous project's coordinator"},
	{"prefix+{ } b", "narrow / widen the sidebar, or make it a slim strip"},
	{"prefix+|", "show or hide the panel on the right: beside a thread's pane its info panel (task, steps, state, PR, last report; drag its border to resize it), on the dashboard the details panel"},
	{"prefix+tab", "the keyboard to the next area: in a session the projects sidebar (its keys below), the info panel (↑ ↓ scroll, enter its task) and back to the pane, which gets no keys meanwhile; on the dashboard as tab"},
	{"prefix+r", "turn remote control of a coordinator on or off, to continue it from another device (asks first): the session's, or on the dashboard the selected project's; ⌁ beside the coordinator's state in the sidebar while it is on. With the project's setting on, tm turns it back on when it drops, but your off holds until the coordinator is started anew"},
	{"prefix+prefix", "send the prefix key itself to the program"},
}

// popupKeys are the project popup's own keys (projectView). None is a
// prefix command (TestNoPlainKeyIsAPrefixCommand).
var popupKeys = []keyHelp{
	{"← → 1-6", "previous / next tab, or pick one"},
	{"↑ ↓ pgup pgdown", "move in the tab, or scroll it (Keys, Memory)"},
	{"enter space + -", "on the Settings tab: as in the settings, below"},
	{"+ x", "on the overview: add a repository / remove the selected one (asks first)"},
	{"enter", "on a task (Tasks tab): show it, with what it is blocked on, or how to check it and whether its pull request merged"},
	{"D", "on an open, ready or blocked task (Tasks tab, or the t list): delegate it; the coordinator starts a thread for it (asks first)"},
	{"A", "on a task in review (Tasks tab, or the t list): accept it; the coordinator marks it done (asks first)"},
	{"x", "on a task in review: send it back with a note on what to change; the coordinator passes it on"},
	{"c", "on a task: open the project's coordinator, to answer what a blocked task waits on"},
	{"esc", "close; esc is the only key that closes a popup"},
}

// settingsKeys are the settings' keys (settings.go): the , popup and
// the project popup's Settings tab. None is a prefix command.
var settingsKeys = []keyHelp{
	{"↑ ↓ pgup pgdown", "move through the settings"},
	{"enter space", "change the selected setting (yolo mode asks first); on the prefix key, the next ctrl+<key> is the new prefix"},
	{"+ -", "on a number: one step up / down"},
	{"esc", "close"},
}

// mouseKeys are the mouse's ways, in the help beside the keys: every
// key has one (TestEveryKeyHasMousePath).
var mouseKeys = []keyHelp{
	{"click", "a row selects it and gives its area the keyboard (the list, the details panel); a footer hint presses its key; a popup's tab, row or setting picks it (a number's − + step it); outside a popup closes it"},
	{"double-click", "a row opens it: a coordinator or a thread attaches, a task shows"},
	{"right-click", "a row of the list or the sidebar: a menu of its actions (open or attach, its popup, …); in a session, the status bar or a pane that doesn't take the mouse: the session's menu"},
	{menuButton + " menu", "every action, first in the footer; in a session, every prefix command, on the status bar"},
	{"status bar", menuButton + " menu, prefix+d dashboard, y yes"},
	{"sidebar", "a row takes the keyboard, its cursor on it, and a project shows its dashboard, its coordinator or a thread attaches; a click on the pane gives the keyboard back"},
	{"info panel", "beside a thread's pane: its task opens the task view, its PR the browser; a click elsewhere gives it the keyboard"},
	{"wheel", "moves through a list; scrolls the details panel and long popups"},
	{"drag", "the divider beside the details panel, the sidebar's border, the info panel's border"},
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
		{"Prefix commands: the same everywhere", sessionKeys},
		{"In the sidebar (tab, or prefix+tab in a session)", side},
		{"In the project popup", popupKeys},
		{"In the settings (, or the project popup's Settings tab)", settingsKeys},
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
	out = append(out, faintLines("A prefix command does the same in a session, on the dashboard and over a popup, and no plain key means something else: popups close with esc, and the lists refresh by themselves. The prefix is in the settings (,); inside tmux, which takes ctrl+b, pick another.", w)...)
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
