package tui

import (
	"os"
	"regexp"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/theclifmeister/terminatr/internal/project"
	"github.com/theclifmeister/terminatr/internal/proto"
	"github.com/theclifmeister/terminatr/internal/tasks"
)

// Asking the coordinator (docs/SPEC.md §4): keys on a task in the Tasks
// tab or the t list ask the project's coordinator to act on it. The
// dashboard still changes no task: it drops an inbox item, which the
// coordinator takes as the user's word. D delegates an open, ready or
// blocked task, A accepts a task in review, x sends one back with a
// note (a done one too, which the coordinator reopens); c opens the
// coordinator, to answer what a task is blocked on.

// notDelegable is why task t can't be delegated, "" when it can: an
// open, ready or blocked task can.
func notDelegable(t *tasks.Task) string {
	switch t.Status {
	case tasks.Open, tasks.Ready, tasks.Blocked:
		return ""
	case tasks.Started:
		return t.Ref() + " is started: a thread already works on it"
	case tasks.Review:
		return t.Ref() + " is in review: A accepts it, x sends it back"
	case tasks.Done:
		return t.Ref() + " is done"
	}
	return t.Ref() + " is " + string(t.Status)
}

// notInReview is why task t can't be accepted, "" when it can: only a
// task in review can.
func notInReview(t *tasks.Task) string {
	if t.Status == tasks.Review {
		return ""
	}
	return t.Ref() + " is " + string(t.Status) + ", not in review"
}

// notSendable is why task t can't be sent back, "" when it can: a task
// in review, or a done one (the coordinator reopens it), can.
func notSendable(t *tasks.Task) string {
	if t.Status == tasks.Review || t.Status == tasks.Done {
		return ""
	}
	return t.Ref() + " is " + string(t.Status) + ", not in review or done"
}

// notAskable is why kind can't be asked for task t, "" when it can.
func notAskable(t *tasks.Task, kind string) string {
	switch kind {
	case project.KindDelegate:
		return notDelegable(t)
	case project.KindAccept:
		return notInReview(t)
	case project.KindSendBack:
		return notSendable(t)
	}
	return "unknown ask " + kind
}

// delegateWaiting is what a task's row says while an item asks the
// coordinator something about it.
const delegateWaiting = "waiting on the coordinator"

// delegateWaitingRow is delegateWaiting for the t list's narrow row.
const delegateWaitingRow = "waiting on coordinator"

// waitingFor says what the coordinator is asked, for kind's item.
func waitingFor(kind string) string {
	switch kind {
	case project.KindAccept:
		return delegateWaiting + " to accept it"
	case project.KindSendBack:
		return delegateWaiting + " to send it back"
	}
	return delegateWaiting + " to delegate it"
}

// asked is the kind of the item that asks slug's coordinator about task
// t, "" for none: while there is one, the task waits on the coordinator.
func (m *dash) asked(slug string, t *tasks.Task) string {
	if p := m.projectData(slug); p != nil {
		return project.TaskAsked(p.Items, t.Ref())
	}
	return ""
}

// loaded tells whether t is a task from a loaded board.
func loaded(t *tasks.Task) bool { return t != nil && (t.Title != "" || t.Status != "") }

// canAsk checks before asking: the footer says why when kind can't be
// asked for t (with what the key does, hint), or when an item already
// asks something of it.
func (m *dash) canAsk(slug string, t *tasks.Task, kind, hint string) bool {
	if !loaded(t) {
		return false // the board isn't loaded yet
	}
	if why := notAskable(t, kind); why != "" {
		m.msg = why + "; " + hint
		return false
	}
	if k := m.asked(slug, t); k != "" {
		m.msg = t.Ref() + " is already " + waitingFor(k)
		return false
	}
	return true
}

// askCoordinator drops kind's item for slug's task t; the footer says
// done.
func (m *dash) askCoordinator(slug string, t *tasks.Task, kind, note, done string) tea.Cmd {
	src, ref, id := m.src, t.Ref(), t.ID
	return m.act(func() actionMsg {
		asked, err := src.Ask(slug, id, kind, note)
		if err != nil {
			return actionMsg{err: err}
		}
		if !asked {
			return actionMsg{msg: ref + " is already " + delegateWaiting}
		}
		return actionMsg{msg: done}
	})
}

// delegate asks, then has the coordinator delegate slug's task t.
func (m *dash) delegate(slug string, t *tasks.Task) tea.Cmd {
	if !m.canAsk(slug, t, project.KindDelegate, "D delegates open, ready and blocked tasks") {
		return nil
	}
	ref := t.Ref()
	question := "Delegate " + ref + " to the coordinator? It starts a thread for " + ref + " " + oneLine(t.Title) + "."
	m.confirmNo(question, ref+" not delegated", func() tea.Cmd {
		return m.askCoordinator(slug, t, project.KindDelegate, "", "asked the coordinator to delegate "+ref+"; it starts a thread for it")
	})
	return nil
}

// accept asks, then tells the coordinator the user accepts slug's task
// t in review: it marks the task done.
func (m *dash) accept(slug string, t *tasks.Task) tea.Cmd {
	if !m.canAsk(slug, t, project.KindAccept, "A accepts tasks in review") {
		return nil
	}
	ref := t.Ref()
	question := "Accept " + ref + "? The coordinator marks " + ref + " " + oneLine(t.Title) + " done."
	m.confirmNo(question, ref+" not accepted", func() tea.Cmd {
		return m.askCoordinator(slug, t, project.KindAccept, "", "told the coordinator you accept "+ref+"; it marks it done")
	})
	return nil
}

// sendBack asks for a note, then has the coordinator send slug's task t
// back to work with it: one in review, or a done one, which it reopens.
func (m *dash) sendBack(slug string, t *tasks.Task) tea.Cmd {
	if !m.canAsk(slug, t, project.KindSendBack, "x sends back tasks in review or done") {
		return nil
	}
	ref := t.Ref()
	m.promptNo("Send "+ref+" back. What should change? ", ref+" not sent back", project.MaxSendBackNote, func(note string) tea.Cmd {
		return m.askCoordinator(slug, t, project.KindSendBack, note, "sent "+ref+" back with your note; the coordinator passes it on")
	})
	return nil
}

// taskKey runs a task key on slug's task t: D, A, x or c (none of
// them a prefix command, docs/SPEC.md §4).
func (m *dash) taskKey(slug string, t *tasks.Task, key string) tea.Cmd {
	switch key {
	case "D":
		return m.delegate(slug, t)
	case "A":
		return m.accept(slug, t)
	case "x":
		return m.sendBack(slug, t)
	case "c":
		return m.openProject(slug)
	}
	return nil
}

// taskKeys is the footer's key list for task t, before the view's own
// keys: the one table of which task keys work in which status, for the
// Tasks tab, the t list and a shown task alike (each key only where it
// works, as notAskable says). Each list, with the view's keys, fits the
// 60 columns a full sidebar leaves at the least (view.SideRoom).
func taskKeys(t *tasks.Task) string {
	if t == nil {
		return ""
	}
	switch t.Status {
	case tasks.Review:
		return "A accept · x send back"
	case tasks.Done:
		return "x send back"
	case tasks.Blocked:
		return "c coordinator · D delegate"
	case tasks.Open, tasks.Ready:
		return "D delegate"
	}
	return ""
}

// blockedOn is what task t is blocked on: its latest "blocked (date):"
// note, "" when it has none.
func blockedOn(t *tasks.Task) string {
	paras := strings.Split(strings.TrimSpace(t.Notes), "\n\n")
	for i := len(paras) - 1; i >= 0; i-- {
		p := strings.TrimSpace(paras[i])
		if rest, ok := strings.CutPrefix(p, string(tasks.Blocked)+" ("); ok {
			if _, note, ok := strings.Cut(rest, "): "); ok {
				return oneLine(note)
			}
		}
	}
	return ""
}

// statusNoteRE is a status note's start: "review (2026-10-05): ".
var statusNoteRE = regexp.MustCompile(`^[a-z]+ \([0-9-]+\): `)

// noteChecks are the task notes that say how to check it: a paragraph
// starting "Check:", "To check:" or "How to check:" (after a status
// note's "review (date): ").
func noteChecks(t *tasks.Task) []string {
	var out []string
	for _, p := range strings.Split(strings.TrimSpace(t.Notes), "\n\n") {
		p = strings.TrimSpace(p)
		p = statusNoteRE.ReplaceAllString(p, "")
		low := strings.ToLower(p)
		for _, pre := range []string{"check:", "to check:", "how to check:"} {
			if strings.HasPrefix(low, pre) {
				if s := oneLine(strings.TrimSpace(p[len(pre):])); s != "" {
					out = append(out, s)
				}
				break
			}
		}
	}
	return out
}

// adoptable is the session of row r that T can ask to adopt, or why
// not (docs/SPEC.md §4, Adopt): an agent session outside the projects,
// with a current project to adopt it into.
func (m *dash) adoptable(r row, ok bool) (proto.SessionInfo, string) {
	if !ok || r.session == "" {
		return proto.SessionInfo{}, "T adopts an agent session outside the projects as a thread; select one"
	}
	s, found := m.session(r.session)
	switch {
	case !found:
		return s, r.session + " is gone"
	case s.Project != "" || s.Role != proto.RoleShell:
		return s, r.session + " is a project's session already"
	case s.Agent == "" || s.State == "exited":
		return s, "no agent runs in " + r.session + "; only an agent session becomes a thread"
	case m.projectHere() == "":
		return s, "no project to adopt " + r.session + " into; n creates one"
	}
	return s, ""
}

// adopt asks, then has the current project's coordinator adopt the
// selected agent session as a thread.
func (m *dash) adopt(string) tea.Cmd {
	r, ok := m.selected()
	s, why := m.adoptable(r, ok)
	if why != "" {
		m.msg = why
		return nil
	}
	slug, src := m.projectHere(), m.src
	home, _ := os.UserHomeDir()
	question := "Adopt " + s.ID + " (" + adoptWhere(s, home) + ") as a thread of " + slug + "? The coordinator links it to a task and briefs it."
	m.confirmNo(question, s.ID+" not adopted", func() tea.Cmd {
		return m.act(func() actionMsg {
			asked, err := src.AskAdopt(slug, s)
			if err != nil {
				return actionMsg{err: err}
			}
			if !asked {
				return actionMsg{msg: s.ID + " is already " + delegateWaiting + " to adopt it"}
			}
			return actionMsg{msg: "asked the coordinator to adopt " + s.ID}
		})
	})
	return nil
}
