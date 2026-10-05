package tui

import (
	tea "charm.land/bubbletea/v2"

	"github.com/theclifmeister/termilator/internal/project"
	"github.com/theclifmeister/termilator/internal/tasks"
)

// Delegating (docs/SPEC.md §4): d on a task in the Tasks tab or the t
// list asks the project's coordinator to start a thread for it. The
// dashboard still changes no task: it drops a delegate inbox item, which
// the coordinator takes as the user's go-ahead.

// notDelegable is why task t can't be delegated, "" when it can: an
// open, ready or blocked task can.
func notDelegable(t *tasks.Task) string {
	switch t.Status {
	case tasks.Open, tasks.Ready, tasks.Blocked:
		return ""
	case tasks.Started:
		return t.Ref() + " is started: a thread already works on it"
	case tasks.Review:
		return t.Ref() + " is in review: it waits for you to accept it"
	case tasks.Done:
		return t.Ref() + " is done"
	}
	return t.Ref() + " is " + string(t.Status)
}

// delegateWaiting is what a task's row says while its delegate item
// waits for the coordinator.
const delegateWaiting = "waiting on the coordinator"

// delegating tells whether slug's task t waits on the coordinator: an
// unhandled delegate item asks for it.
func (m *dash) delegating(slug string, t *tasks.Task) bool {
	if p := m.projectData(slug); p != nil {
		return project.DelegateAsked(p.Items, t.Ref())
	}
	return false
}

// delegate asks, then has the coordinator delegate slug's task t; the
// footer says why when it can't be.
func (m *dash) delegate(slug string, t *tasks.Task) tea.Cmd {
	if t == nil || t.Title == "" && t.Status == "" {
		return nil // the board isn't loaded yet
	}
	if why := notDelegable(t); why != "" {
		m.msg = why + "; d delegates open, ready and blocked tasks"
		return nil
	}
	ref, id := t.Ref(), t.ID
	if m.delegating(slug, t) {
		m.msg = ref + " is already " + delegateWaiting
		return nil
	}
	question := "Delegate " + ref + " to the coordinator? It starts a thread for " + ref + " " + oneLine(t.Title) + "."
	m.confirmNo(question, ref+" not delegated", func() tea.Cmd {
		src := m.src
		return m.act(func() actionMsg {
			asked, err := src.Delegate(slug, id)
			if err != nil {
				return actionMsg{err: err}
			}
			if !asked {
				return actionMsg{msg: ref + " is already " + delegateWaiting}
			}
			return actionMsg{msg: "asked the coordinator to delegate " + ref + "; it starts a thread for it"}
		})
	})
	return nil
}
