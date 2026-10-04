package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/theclifmeister/termalator/internal/tasks"
)

// Actions: the list's keys. One table drives the key handling, the help
// and the footer, so they can't disagree.

// An action is a key on the list and what it does.
type action struct {
	keys []string
	// label and help are its line in the help; an empty label leaves it
	// out. {agent} in help is the agent's name.
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
		{keys: []string{"enter"}, label: "enter", help: "attach to the selected session; on a project, open its coordinator; on a task, show it",
			foot: func(_ *dash, r row, ok bool) string {
				switch {
				case !ok:
					return ""
				case r.task != nil:
					return "show"
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
		{keys: []string{"c"}, label: "c", help: "new {agent} session in a directory you choose",
			run: (*dash).startAgent},
		{keys: []string{"n"}, label: "n", help: "new project",
			foot: func(m *dash, _ row, _ bool) string {
				if len(m.data.Projects) == 0 {
					return "new project"
				}
				return ""
			},
			run: (*dash).newProject},
		{keys: []string{"t"}, label: "t", help: "the project's tasks; enter shows one, d marks a task in review done",
			foot: withProject("tasks"), run: (*dash).taskBoard},
		{keys: []string{"d"}, label: "d", help: "mark the selected task done (tasks in review)",
			foot: func(_ *dash, r row, ok bool) string {
				if ok && r.task != nil && (r.confirm || r.task.Status == tasks.Review) {
					return "done"
				}
				return ""
			},
			run: (*dash).done},
		{keys: []string{"i"}, label: "i", help: "the project's inbox: what the coordinator is told about",
			foot: withProject("inbox"), run: (*dash).inbox},
		{keys: []string{"a"}, label: "a", help: "acknowledge the selected thread's report",
			foot: func(_ *dash, r row, ok bool) string {
				if ok && r.thread != nil && r.thread.ReportState() == "new" {
					return "ack"
				}
				return ""
			},
			run: (*dash).ack},
		{keys: strings.Split("1 2 3 4 5 6 7 8 9", " "), label: "1-9", help: "send that ## Next line of the thread's report as its next prompt",
			foot: func(_ *dash, r row, ok bool) string {
				if ok && r.thread != nil && r.thread.Report != nil && len(r.thread.Report.Next) > 0 {
					return "send next"
				}
				return ""
			},
			run: (*dash).sendNext},
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

func helpLines(agentName, prefix string) []string {
	var out []string
	for _, a := range actions {
		if a.label != "" {
			out = append(out, fmt.Sprintf("%-9s %s", a.label, strings.ReplaceAll(a.help, "{agent}", agentName)))
		}
	}
	return append(out,
		"",
		styleHead.Render("In a session")+", the prefix "+styleAccent.Render(prefix)+" then:",
		fmt.Sprintf("%-9s %s", "d", "back to this dashboard (the session keeps running)"),
		fmt.Sprintf("%-9s %s", "p ] [", "back here and switch project"),
		fmt.Sprintf("%-9s %s", "i t , ?", "back here with the inbox, tasks, settings or help open"),
		fmt.Sprintf("%-9s %s", prefix, "send "+prefix+" itself to the program"),
		styleFaint.Render("Here, the prefix then a key is that key. The prefix is [keys] prefix in config.toml."),
	)
}

func (m *dash) enter(string) tea.Cmd {
	r, ok := m.selected()
	switch {
	case !ok:
	case r.task != nil:
		return m.openBoard(r.project, r.task.ID)
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

func (m *dash) startAgent(string) tea.Cmd {
	m.prompt(m.agentName+" session in directory: ", m.cwd, func(dir string) tea.Cmd {
		dir = expandDir(dir, m.cwd)
		cols, rows := m.paneSize()
		return m.act(func() actionMsg {
			id, err := m.src.StartAgent(dir, cols, rows)
			return actionMsg{attach: id, sel: "s:" + id, err: err}
		})
	})
	return nil
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

func (m *dash) done(string) tea.Cmd {
	r, ok := m.selected()
	if !ok || r.task == nil {
		m.msg = "d marks a task in review done; select one (t shows the tasks)"
		return nil
	}
	return m.markDone(r.project, r.task, r.confirm)
}

func (m *dash) ack(string) tea.Cmd {
	r, ok := m.selected()
	if !ok || r.thread == nil {
		m.msg = "a acknowledges a thread's report; select the thread"
		return nil
	}
	if r.thread.ReportState() != "new" {
		m.msg = r.thread.ID + " has no unacknowledged report"
		return nil
	}
	slug, id, n := r.project, r.thread.ID, r.thread.Reports
	return m.act(func() actionMsg {
		err := m.src.Ack(slug, id)
		return actionMsg{msg: fmt.Sprintf("%s report %d acknowledged", id, n), err: err}
	})
}

func (m *dash) sendNext(key string) tea.Cmd {
	r, ok := m.selected()
	if !ok || r.thread == nil {
		return nil
	}
	n := int(key[0] - '0')
	if r.thread.Report == nil || n > len(r.thread.Report.Next) {
		m.msg = fmt.Sprintf("%s's report has no ## Next line %d", r.thread.ID, n)
		return nil
	}
	slug, id := r.project, r.thread.ID
	line := r.thread.Report.Next[n-1]
	return m.act(func() actionMsg {
		err := m.src.PromptNext(slug, id, n)
		return actionMsg{msg: id + " ← " + oneLine(line), err: err}
	})
}

func (m *dash) resize(key string) tea.Cmd {
	if split, _ := m.split(); !split {
		m.msg = fmt.Sprintf("the details panel shows in windows %d columns or wider; | turns it on", splitMin)
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
