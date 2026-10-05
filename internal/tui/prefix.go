package tui

import (
	tea "charm.land/bubbletea/v2"

	"github.com/theclifmeister/termilator/internal/proto"
)

// The prefix commands on the dashboard (docs/SPEC.md §4, the key table).
// prefix then a key means the same everywhere: in a session, on the
// dashboard, over any popup and with the sidebar holding the keyboard.
// It never reaches a popup as a plain key: a command that opens or goes
// somewhere closes the popups first. Its keys are those of a session
// (prefixStep): d, q, r, the dashboard keys (prefixCommands), the
// sidebar's (paneCommands) and the prefix itself.

// isPrefixCommand says whether key is a command after the prefix.
func isPrefixCommand(key string) bool {
	return key == "d" || key == "q" || key == "r" || prefixCommands[key] || paneCommands[key]
}

// prefixHint is the footer while the prefix waits for its command.
const prefixHint = "prefix ▸ d dashboard · q quit · a i t , ? popups · p ] [ projects · r remote · esc cancel"

// prefixCommand runs the key typed after the prefix.
func (m *dash) prefixCommand(key string) tea.Cmd {
	switch {
	case key == "q":
		m.quitting = true
		return tea.Quit
	case key == "esc" || key == m.prefix:
		// Cancelled; prefix twice sends the prefix to a program, and
		// the dashboard has none.
		return nil
	case key == "d" && m.over != nil:
		// Over a session, as in it: the view's dashboard.
		m.dropOver()
		m.stack = nil
		return m.toDashboard()
	case key == "d":
		// Already here: back to the bare list.
		m.stack, m.focus = nil, focusList
		return nil
	case m.over != nil && (key == "r" || key == "tab"):
		// The session's own: back to it, which runs the command (the
		// remote control question, the sidebar's keyboard).
		m.stack, m.result.Command = nil, key
		return nil
	case key == "r":
		m.stack = nil
		return m.askRemote()
	case paneCommands[key] && key != "tab":
		// The sidebar's width and slim strip; the popup stays.
		return m.listKey(key)
	case popupCommands[key], key == "tab":
		m.stack = nil
		return m.listKey(key)
	case prefixCommands[key]:
		// p ] [ run on the dashboard: over a session, go back to it.
		m.stack = nil
		var back tea.Cmd
		if m.over != nil {
			m.dropOver()
			back = m.toDashboard()
		}
		return tea.Batch(back, m.listKey(key))
	}
	m.msg = "prefix+" + key + " isn't a command; ? lists them"
	return nil
}

// toDashboard shows the view's dashboard, on every console.
func (m *dash) toDashboard() tea.Cmd {
	if m.view == nil {
		return nil
	}
	return m.call(proto.MethodViewDashboard, proto.ViewParams{})
}

// askRemote asks whether to turn the selected project's coordinator's
// remote control on or off (prefix+r on the dashboard; in a session it
// is the focused coordinator's).
func (m *dash) askRemote() tea.Cmd {
	slug := m.needProject()
	if slug == "" {
		return nil
	}
	for _, s := range m.data.Sessions {
		if s.Role != proto.RoleCoordinator || s.Project != slug {
			continue
		}
		on := !s.RemoteControl
		m.confirmNo(remoteQuestion(s), "remote control unchanged", func() tea.Cmd {
			return m.act(func() actionMsg {
				msg, err := m.src.SetRemote(slug, on)
				return actionMsg{msg: msg, err: err}
			})
		})
		return nil
	}
	m.msg = "no coordinator runs for " + slug + "; enter on the project starts it"
	return nil
}
