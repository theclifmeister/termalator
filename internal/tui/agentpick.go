package tui

import (
	"errors"
	"fmt"
	"strconv"

	tea "charm.land/bubbletea/v2"

	"github.com/theclifmeister/terminatr/internal/config"
	"github.com/theclifmeister/terminatr/internal/models"
)

// agentPickView asks the user, once, which installed agent coordinators
// and threads run, when several are installed and the settings name
// none (docs/SPEC.md §8.2, §11.2): the answer is saved for all projects
// ([defaults] thread_agent and coordinator_agent), and the project opens.
// A project can still set its own in its popup.
type agentPickView struct {
	slug   string
	agents []string
	sel    int
}

// notChosen reports whether err is the refusal to pick between several
// installed agents.
func notChosen(err error) bool {
	var me *models.Error
	return errors.As(err, &me) && me.Code == models.CodeNotChosen
}

func (v *agentPickView) key(m *dash, k tea.KeyPressMsg) tea.Cmd {
	switch s := k.String(); s {
	case "esc", "q":
		m.pop()
		m.msg = "no agent chosen: " + v.slug + " stays closed"
	case "up", "k", "down", "j":
		v.sel = moveSel(v.sel, scrollKeys[s], len(v.agents))
	case "enter":
		return v.pick(m, v.sel)
	default:
		if n, err := strconv.Atoi(s); err == nil && n >= 1 && n <= len(v.agents) {
			return v.pick(m, n-1)
		}
	}
	return nil
}

func (v *agentPickView) pick(m *dash, i int) tea.Cmd {
	m.pop()
	name, slug, src := v.agents[i], v.slug, m.src
	cols, rows := m.paneSize()
	return m.act(func() actionMsg {
		for _, key := range []string{"coordinator_agent", "thread_agent"} {
			if err := src.SetSetting(config.DefaultsTable, key, name); err != nil {
				return actionMsg{err: settingsErr(err)}
			}
		}
		id, err := src.OpenProject(slug, cols, rows)
		return actionMsg{attach: id, current: slug, sel: "p:" + slug, err: err,
			msg: fmt.Sprintf("coordinators and threads of all projects run %s (a project's popup can choose another)", name)}
	})
}

func (v *agentPickView) render(m *dash) string {
	w := m.inner(dialogWidth)
	body := wrapLines("Several agents are installed. Which one should coordinators and threads run? It is saved for all projects; a project's popup (Settings) can choose another.", w)
	body = append(body, "")
	head := len(body)
	hits := make([]int, head)
	for i := range hits {
		hits[i] = noHit
	}
	for i, a := range v.agents {
		body = append(body, fmt.Sprintf("  %d  %s", i+1, a))
		hits = append(hits, i)
	}
	return m.popup(box{title: "Choose an agent", body: body, sel: head + v.sel, hits: hits, keys: "1-9 or enter pick · esc cancel", dialog: true})
}

func (v *agentPickView) click(m *dash, item, _ int, _ bool) tea.Cmd {
	if item == noHit {
		return nil
	}
	if item != v.sel {
		v.sel = item
		return nil
	}
	return v.pick(m, item)
}
