package tui

import (
	"fmt"
	"regexp"
	"slices"

	tea "charm.land/bubbletea/v2"

	"github.com/theclifmeister/termalator/internal/config"
	"github.com/theclifmeister/termalator/internal/proto"
)

// Settings (docs/SPEC.md §4, §11.2): plain labels, each with a line on
// what it does, changed in place with enter or space. The , popup has the
// settings of every project (the prefix key, the default agent, the
// layout); a project's own are the Settings tab of its popup (a). The UI
// never names the file or its keys.
//
// Where they live: the prefix key, the default agent and the projects'
// settings are the human's, in the settings file, shared by every
// console; the details panel and list width are this console's (ui.json);
// the sidebar is the view's. Which popup is open is each console's own.

// setting is one row of a settings list.
type setting struct {
	label, help string
	value       func(m *dash) string
	// change runs on enter or space; nil shows the value only.
	change func(m *dash) tea.Cmd
	// note adds lines under the help (e.g. the running coordinator's
	// remote control when it differs).
	note func(m *dash) []string
}

// settingsList is a list of settings with a selection.
type settingsList struct {
	rows []setting
	sel  int
}

func (l *settingsList) key(m *dash, k tea.KeyPressMsg) (tea.Cmd, bool) {
	switch k.String() {
	case "up", "k":
		l.sel = moveSel(l.sel, -1, len(l.rows))
	case "down", "j":
		l.sel = moveSel(l.sel, 1, len(l.rows))
	case "enter", "space", " ":
		if l.sel < len(l.rows) && l.rows[l.sel].change != nil && !m.busy {
			return l.rows[l.sel].change(m), true
		}
	default:
		return nil, false
	}
	return nil, true
}

// lines draws the list w cells wide; sel is the selected row's line,
// hits what a click on each line picks: a setting's own line is its
// index, which changes it; its help is helpHit more, which selects it.
func (l *settingsList) lines(m *dash, w int) (out []string, sel int, hits []int) {
	sel = -1
	defer func() {
		for len(hits) < len(out) {
			hits = append(hits, noHit)
		}
	}()
	lw := 0
	for _, r := range l.rows {
		lw = max(lw, len([]rune(r.label)))
	}
	for i, r := range l.rows {
		if i > 0 {
			out = append(out, "")
			hits = append(hits, noHit)
		}
		hits = append(hits, i)
		text := fit(r.label, lw) + "  " + r.value(m)
		if i == l.sel {
			sel = len(out)
			out = append(out, styleSel.Render(fit(text, w)))
		} else {
			out = append(out, styleHead.Render(fit(r.label, lw))+"  "+styleAccent.Render(r.value(m)))
		}
		out = append(out, faintLines(r.help, w)...)
		if r.note != nil {
			out = append(out, r.note(m)...)
		}
		for len(hits) < len(out) {
			hits = append(hits, i+helpHit)
		}
	}
	return out, sel, hits
}

// helpHit marks a setting's help lines in its list's hits.
const helpHit = 1 << 16

// click selects the clicked setting; a click on its own line changes it,
// as enter does.
func (l *settingsList) click(m *dash, item int) tea.Cmd {
	if item >= helpHit {
		l.sel = moveSel(item-helpHit, 0, len(l.rows))
		return nil
	}
	l.sel = moveSel(item, 0, len(l.rows))
	cmd, _ := l.key(m, keyMsg("enter"))
	return cmd
}

// onOff is a bool as the settings show it.
func onOff(b bool) string { return map[bool]string{true: "on", false: "off"}[b] }

// setSetting changes a setting in the background, then reloads.
func (m *dash) setSetting(table, key string, value any, msg string) tea.Cmd {
	src := m.src
	return m.act(func() actionMsg {
		if err := src.SetSetting(table, key, value); err != nil {
			return actionMsg{err: settingsErr(err)}
		}
		return actionMsg{msg: msg}
	})
}

var lineRE = regexp.MustCompile(`line (\d+)`)

// settingsErr is a settings error in the user's words: no file, no keys.
func settingsErr(err error) error {
	if err == nil {
		return nil
	}
	if err == config.ErrForm {
		return fmt.Errorf("%w; it stays as it is", err)
	}
	if m := lineRE.FindStringSubmatch(err.Error()); m != nil {
		return fmt.Errorf("the settings can't be read: line %s is broken", m[1])
	}
	return err
}

// settingsView is the , popup: the settings of every project.
type settingsView struct{ list settingsList }

func (m *dash) openSettings() {
	sv := &settingsView{list: settingsList{rows: globalSettings()}}
	m.push(sv)
}

func globalSettings() []setting {
	return []setting{
		{label: "Prefix key", help: "Starts the session commands, written prefix+<key> in hints. Inside tmux, which takes ctrl+b, pick another. Enter, then press the new one.",
			value:  func(m *dash) string { return m.prefix },
			change: func(m *dash) tea.Cmd { m.push(&captureView{}); return nil }},
		{label: "Default agent", help: "The agent new coordinators run. Enter picks the next one tm knows.",
			value: func(m *dash) string { return config.DefaultAgent(DefaultAgent) },
			change: func(m *dash) tea.Cmd {
				names := m.src.Agents()
				if len(names) < 2 {
					m.msg = "tm knows one agent; add others to choose between them (tm agent)"
					return nil
				}
				i := slices.Index(names, config.DefaultAgent(DefaultAgent))
				next := names[(i+1)%len(names)]
				return m.setSetting("", "default_agent", next, "new coordinators run "+next)
			}},
		{label: "Details panel", help: "The panel beside the list, in windows 120 columns or wider (| on the dashboard). This console only.",
			value: func(m *dash) string { return onOff(m.layout.Details) },
			change: func(m *dash) tea.Cmd {
				l := m.layout
				l.Details = !l.Details
				m.setLayout(l)
				return nil
			}},
		{label: "List width", help: "The list's share beside the details panel; < and > on the dashboard fine-tune it. This console only.",
			value: func(m *dash) string { return fmt.Sprintf("%d%%", int(m.layout.Split*100+0.5)) },
			change: func(m *dash) tea.Cmd {
				l := m.layout
				l.Split = nextSplit(l.Split)
				m.setLayout(l)
				return nil
			}},
		{label: "Sidebar", help: "The projects sidebar: full, or the slim strip (b on the dashboard). Every console of this view follows.",
			value: func(m *dash) string {
				if m.layout.Sidebar.Slim {
					return "slim"
				}
				return "full"
			},
			change: func(m *dash) tea.Cmd { return m.sideKey("b") }},
	}
}

// splitSteps are the list widths enter steps through.
var splitSteps = []float64{0.5, 0.65, 0.8}

func nextSplit(cur float64) float64 {
	for _, s := range splitSteps {
		if s > cur+0.001 {
			return s
		}
	}
	return splitSteps[0]
}

func (sv *settingsView) key(m *dash, k tea.KeyPressMsg) tea.Cmd {
	switch k.String() {
	case "esc", "q", ",":
		m.pop()
		return nil
	}
	cmd, _ := sv.list.key(m, k)
	return cmd
}

func (sv *settingsView) render(m *dash) string {
	w := m.inner(settingsWidth)
	lines, sel, hits := sv.list.lines(m, w)
	lines = append(lines, "")
	lines = append(lines, faintLines("A project's own settings (starting threads, yolo mode, remote control, …) are in its popup: a on the dashboard, prefix+a in a session.", w)...)
	return m.popup(box{title: "settings", body: lines, sel: sel, hits: hits, keys: "enter change · ↑ ↓ move · esc back", width: settingsWidth})
}

func (sv *settingsView) click(m *dash, item, _ int, _ bool) tea.Cmd { return sv.list.click(m, item) }

func (sv *settingsView) wheel(m *dash, d int) { sv.list.key(m, arrow(d)) }

const settingsWidth = 88

// captureView takes the next ctrl+<key> as the new prefix key.
type captureView struct{ err string }

func (cv *captureView) key(m *dash, k tea.KeyPressMsg) tea.Cmd {
	if k.String() == "esc" {
		m.pop()
		return nil
	}
	c, err := parseChord(k.String())
	if err != nil {
		cv.err = k.String() + " can't be the prefix: press ctrl and a key"
		return nil
	}
	m.pop()
	key := c.String()
	m.prefix = key
	return m.setSetting("keys", "prefix", key, "the prefix is "+key+" now; consoles started earlier pick it up when they next open")
}

func (cv *captureView) render(m *dash) string {
	lines := []string{"Press the new prefix key: ctrl and a key, e.g. ctrl+a.", styleFaint.Render("Now " + m.prefix + ".")}
	if cv.err != "" {
		lines = append(lines, styleBad.Render(cv.err))
	}
	return m.popup(box{title: "prefix key", body: lines, sel: -1, keys: "esc cancel", width: 64})
}

// confirmView asks a yes/no question: y runs yes, any other key cancels.
type confirmView struct {
	question string
	yes      func() tea.Cmd
}

func (m *dash) confirm(question string, yes func() tea.Cmd) {
	m.push(&confirmView{question: question, yes: yes})
}

func (cv *confirmView) key(m *dash, k tea.KeyPressMsg) tea.Cmd {
	m.pop()
	if k.String() == "y" {
		return cv.yes()
	}
	m.msg = "unchanged"
	return nil
}

func (cv *confirmView) render(m *dash) string {
	return m.popup(box{body: wrapLines(cv.question, m.inner(promptWidth)), sel: -1, keys: "y yes · any other key no", width: promptWidth})
}

// projectSettings are a project's own settings (§11.2), for its popup.
func projectSettings(slug string) []setting {
	safety := func(m *dash) config.Safety {
		if p := m.projectData(slug); p != nil && p.Safety != nil {
			return *p.Safety
		}
		return config.Defaults
	}
	table := "projects." + slug
	toggle := func(key string, get func(config.Safety) bool, what string) func(m *dash) tea.Cmd {
		return func(m *dash) tea.Cmd {
			on := !get(safety(m))
			return m.setSetting(table, key, on, what+" "+onOff(on)+" for "+slug)
		}
	}
	return []setting{
		{label: "Start threads", help: "Ask first: the coordinator proposes threads and starts them after your go-ahead. Automatically: it starts them itself.",
			value: func(m *dash) string {
				if safety(m).StartThreads == config.StartAuto {
					return "automatically"
				}
				return "ask first"
			},
			change: func(m *dash) tea.Cmd {
				next, word := config.StartAuto, "automatically"
				if safety(m).StartThreads == config.StartAuto {
					next, word = config.StartPropose, "after asking you"
				}
				return m.setSetting(table, "start_threads", next, "threads of "+slug+" start "+word)
			}},
		{label: "Yolo mode", help: "New threads skip the agent's permission prompts; the file rules and the sandbox still hold. Turning it on asks first.",
			value: func(m *dash) string { return onOff(safety(m).Yolo) },
			change: func(m *dash) tea.Cmd {
				if safety(m).Yolo {
					return m.setSetting(table, "yolo", false, "yolo mode off for "+slug)
				}
				m.confirm("Turn yolo mode on for "+slug+"? Threads started from now on skip the agent's permission prompts (the file rules and the sandbox still hold).", func() tea.Cmd {
					return m.setSetting(table, "yolo", true, "yolo mode on for "+slug+": new threads skip permission prompts")
				})
				return nil
			}},
		{label: "Coordinator approves", help: "The coordinator may answer its threads' in-scope permission prompts (allow once, never always).",
			value:  func(m *dash) string { return onOff(safety(m).CoordinatorApproves) },
			change: toggle("coordinator_approves", func(s config.Safety) bool { return s.CoordinatorApproves }, "coordinator approvals")},
		{label: "Auto-close finished threads", help: "Close a thread once its pull request merged and its agent is idle; its worktree is removed only when nothing is lost.",
			value:  func(m *dash) string { return onOff(safety(m).AutoResolve) },
			change: toggle("auto_resolve", func(s config.Safety) bool { return s.AutoResolve }, "auto-close")},
		{label: "Pull request follow-up", help: "Prompt a thread when its pull request's checks fail or a reviewer asks for changes.",
			value:  func(m *dash) string { return onOff(safety(m).PRFollowup) },
			change: toggle("pr_followup", func(s config.Safety) bool { return s.PRFollowup }, "pull request follow-up")},
		{label: "Remote control", help: "New coordinators start so you can continue them from another device. prefix+r changes the running one.",
			value:  func(m *dash) string { return onOff(safety(m).CoordinatorRemoteControl) },
			change: toggle("coordinator_remote_control", func(s config.Safety) bool { return s.CoordinatorRemoteControl }, "remote control for new coordinators"),
			note: func(m *dash) []string {
				return remoteNote(safety(m).CoordinatorRemoteControl, m.data.Sessions, slug)
			}},
	}
}

// remoteNote says when the running coordinator's remote control differs
// from the setting (prefix+r changed it since it started).
func remoteNote(setting bool, sessions []proto.SessionInfo, slug string) []string {
	for _, s := range sessions {
		if s.Role == proto.RoleCoordinator && s.Project == slug && s.RemoteControl != setting {
			return []string{styleWarn.Render("the running coordinator has it " + onOff(s.RemoteControl) + " (prefix+r), until it is started anew")}
		}
	}
	return nil
}

// projectData is slug's data from the last poll.
func (m *dash) projectData(slug string) *ProjectData {
	for i := range m.data.Projects {
		if m.data.Projects[i].Slug == slug {
			return &m.data.Projects[i]
		}
	}
	return nil
}
