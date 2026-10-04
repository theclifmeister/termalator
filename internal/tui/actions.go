package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
)

// Actions: the list's keys. One table drives the key handling, the help
// and the footer, so they can't disagree.

// An action is a key on the list and what it does.
type action struct {
	keys []string
	// label and help are its line in the help; an empty label leaves it
	// out.
	label, help string
	// foot is its word in the footer for the selected row (ok is false
	// without one), "" to leave it out there.
	foot func(m *dash, r row, ok bool) string
	run  func(m *dash, key string) tea.Cmd
}

// always shows an action in the footer as word.
func always(word string) func(*dash, row, bool) string {
	return func(*dash, row, bool) string { return word }
}

// withProject shows an action in the footer as word when there is a
// project to apply it to.
func withProject(word string) func(*dash, row, bool) string {
	return func(m *dash, _ row, _ bool) string {
		if m.projectHere() == "" {
			return ""
		}
		return word
	}
}

// actions is set in init: the ? action opens the help, which lists the
// actions.
var actions []action

func init() {
	actions = []action{
		{keys: []string{"up", "k"}, run: func(m *dash, _ string) tea.Cmd { m.move(-1); return nil }},
		{keys: []string{"down", "j"}, run: func(m *dash, _ string) tea.Cmd { m.move(1); return nil }},
		{keys: []string{"enter"}, label: "enter", help: "attach to the selected session; on a project, open its coordinator; a thread opens watch-only",
			foot: func(_ *dash, r row, ok bool) string {
				switch {
				case !ok:
					return ""
				case r.thread != nil && r.session != "":
					return "watch"
				case r.session != "":
					return "attach"
				case r.thread == nil && r.project != "":
					return "open"
				}
				return ""
			},
			run: (*dash).enter},
		{keys: []string{"s"}, label: "s", help: "new shell session (in the directory tm was started in)",
			run: (*dash).startShell},
		{keys: []string{"n"}, label: "n", help: "new project",
			foot: func(m *dash, _ row, _ bool) string {
				if len(m.data.Projects) == 0 {
					return "new project"
				}
				return ""
			},
			run: (*dash).newProject},
		{keys: []string{"t"}, label: "t", help: "the project's tasks, read-only; enter shows one",
			foot: withProject("tasks"), run: (*dash).taskBoard},
		{keys: []string{"i"}, label: "i", help: "the project's inbox, read-only: what the coordinator is told about",
			foot: withProject("inbox"), run: (*dash).inbox},
		{keys: []string{"p"}, label: "p", help: "project switcher; enter opens that project's coordinator",
			foot: func(m *dash, _ row, _ bool) string {
				if len(m.data.Projects) > 1 {
					return "projects"
				}
				return ""
			},
			run: (*dash).switcher},
		{keys: []string{"]", "["}, label: "] [", help: "next / previous project's coordinator",
			run: func(m *dash, key string) tea.Cmd { return m.cycleProject(key == "]") }},
		{keys: []string{"<", ">"}, label: "< >", help: "narrow / widen the list beside the details panel (or drag the divider)",
			run: (*dash).resize},
		{keys: []string{"{", "}"}, label: "{ }", help: "narrow / widen the projects sidebar (or drag its border)",
			run: (*dash).sideKey},
		{keys: []string{"b"}, label: "b", help: "the sidebar as a slim strip (glyphs and short names), and back",
			run: (*dash).sideKey},
		{keys: []string{"|"}, label: "|", help: "show or hide the details panel (windows 120 columns or wider)",
			run: func(m *dash, _ string) tea.Cmd {
				l := m.layout
				l.Details = !l.Details
				m.setLayout(l)
				return nil
			}},
		{keys: []string{","}, label: ",", help: "settings: the prefix key, the project's safety settings, the layout",
			foot: always("settings"), run: func(m *dash, _ string) tea.Cmd { m.openSettings(m.projectHere()); return nil }},
		{keys: []string{"r"}, label: "r", help: "refresh",
			run: func(m *dash, _ string) tea.Cmd { return m.load() }},
		{keys: []string{"?"}, label: "?", help: "help",
			foot: always("help"), run: func(m *dash, _ string) tea.Cmd { m.push(helpView{}); return nil }},
		{keys: []string{"q"}, label: "q", help: "quit (the server keeps running)",
			foot: always("quit"), run: func(*dash, string) tea.Cmd { return tea.Quit }},
	}
}

// listKey runs the list's action for key, if it has one.
func (m *dash) listKey(key string) tea.Cmd {
	for _, a := range actions {
		for _, k := range a.keys {
			if k == key {
				return a.run(m, key)
			}
		}
	}
	return nil
}

// footKeys is the footer's key list: the actions that apply to the
// selected row.
func (m *dash) footKeys() string {
	r, ok := m.selected()
	var out []string
	for _, a := range actions {
		if a.foot == nil {
			continue
		}
		if word := a.foot(m, r, ok); word != "" {
			out = append(out, a.label+" "+word)
		}
	}
	return strings.Join(out, " · ")
}

func helpLines() []string {
	var out []string
	for _, a := range actions {
		if a.label != "" {
			out = append(out, fmt.Sprintf("%-9s %s", a.label, a.help))
		}
	}
	return append(out,
		fmt.Sprintf("%-9s %s", "click", "in the sidebar: ▸ ▾ open or close a project; a project shows its dashboard, its coordinator"),
		fmt.Sprintf("%-9s %s", "", "attaches, a thread watches it; the same from a session, tm attach and tm project open"),
		"",
		styleHead.Render("In a session")+" (prefix+<key> is the prefix, then the key):",
		fmt.Sprintf("%-16s %s", "prefix+d", "back to this dashboard, on every console of the view (the session keeps running)"),
		fmt.Sprintf("%-16s %s", "prefix+p ] [", "back here and switch project"),
		fmt.Sprintf("%-16s %s", "prefix+i t , ?", "back here with the inbox, tasks, settings or help open"),
		fmt.Sprintf("%-16s %s", `prefix+% "`, "split the window: a new shell beside / below"),
		fmt.Sprintf("%-16s %s", "prefix+arrows o", "focus another pane;  ctrl+arrows resize, z zooms, x closes, space switches the layout"),
		fmt.Sprintf("%-16s %s", "prefix+{ } b", "narrow / widen the sidebar, or make it a slim strip"),
		fmt.Sprintf("%-16s %s", "prefix+u", "take over a watch-only thread pane and type into it (asks first; its coordinator is told)"),
		fmt.Sprintf("%-16s %s", "prefix+r", "turn remote control of a coordinator on or off, to continue it from another device (asks first)"),
		fmt.Sprintf("%-16s %s", "prefix+prefix", "send the prefix key itself to the program"),
		"",
		styleFaint.Render("Every tm shows the same view: what one does, the others show, sized by the one typed in. tm --own keeps to itself."),
		styleFaint.Render("You talk to coordinators; they run the threads, their reports and the tasks (tm thread, tm task)."),
		styleFaint.Render("Here, prefix+<key> is that key. The prefix is in the settings (,); inside tmux, pick another."),
	)
}

func (m *dash) enter(string) tea.Cmd {
	r, ok := m.selected()
	switch {
	case !ok:
	case r.session != "":
		return m.act(func() actionMsg { return actionMsg{attach: r.session, current: r.project} })
	case r.thread != nil:
		m.msg = r.thread.ID + " has no running session; the coordinator restarts it (tm thread restart " + r.thread.ID + ")"
	case r.project != "":
		return m.openProject(r.project)
	}
	return nil
}

func (m *dash) startShell(string) tea.Cmd {
	cols, rows := m.paneSize()
	cwd := m.cwd
	return m.act(func() actionMsg {
		id, err := m.src.StartShell(cwd, cols, rows)
		return actionMsg{attach: id, sel: "s:" + id, err: err}
	})
}

func (m *dash) newProject(string) tea.Cmd {
	m.prompt("new project name: ", "", func(name string) tea.Cmd {
		return m.act(func() actionMsg {
			slug, err := m.src.NewProject(name)
			return actionMsg{sel: "p:" + slug, msg: "created project " + slug + "; enter starts its coordinator", err: err}
		})
	})
	return nil
}

// needProject is the project the key is about, or "" with a hint in the
// footer when there is none.
func (m *dash) needProject() string {
	slug := m.projectHere()
	if slug == "" {
		m.msg = "no project; n creates one"
	}
	return slug
}

func (m *dash) taskBoard(string) tea.Cmd {
	if slug := m.needProject(); slug != "" {
		return m.openBoard(slug, 0)
	}
	return nil
}

func (m *dash) inbox(string) tea.Cmd {
	if slug := m.needProject(); slug != "" {
		m.push(&inboxView{slug: slug})
	}
	return nil
}

func (m *dash) switcher(string) tea.Cmd {
	if len(m.data.Projects) == 0 {
		m.msg = "no projects; n creates one"
		return nil
	}
	sw := &switchView{}
	for i, p := range m.data.Projects {
		if p.Slug == m.projectHere() {
			sw.sel = i
		}
	}
	m.push(sw)
	return nil
}

func (m *dash) resize(key string) tea.Cmd {
	if split, _ := m.split(); !split {
		m.msg = fmt.Sprintf("the details panel shows when the dashboard (the window less the sidebar) is %d columns or wider; | turns it on", splitMin)
		return nil
	}
	l := m.layout
	if key == "<" {
		l.Split -= splitStep
	} else {
		l.Split += splitStep
	}
	m.setLayout(l)
	return nil
}

func (m *dash) sideKey(key string) tea.Cmd {
	l := m.layout
	var msg string
	l.Sidebar, msg = l.Sidebar.Key(key, m.winW)
	m.setLayout(l)
	if msg != "" {
		m.msg = msg
	}
	return nil
}
