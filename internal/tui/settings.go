package tui

import (
	"fmt"
	"regexp"
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/theclifmeister/termilator/internal/config"
	"github.com/theclifmeister/termilator/internal/proto"
	"github.com/theclifmeister/termilator/internal/thread"
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
	// adjust changes a number by delta, on + and -.
	adjust func(m *dash, delta int) tea.Cmd
	// note adds lines under the help (e.g. the running coordinator's
	// remote control when it differs).
	note func(m *dash) []string
	// from follows the value, faint: where it comes from ("all
	// projects" on a project's setting it doesn't set itself).
	from func(m *dash) string
	// unset runs on x, delete or backspace: a project's own value goes,
	// so it follows all projects again.
	unset func(m *dash) tea.Cmd
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
	case "+", "=", "-":
		if l.sel < len(l.rows) && l.rows[l.sel].adjust != nil && !m.busy {
			return l.rows[l.sel].adjust(m, map[bool]int{true: -1, false: 1}[k.String() == "-"]), true
		}
	case "x", "delete", "backspace":
		if l.sel < len(l.rows) && l.rows[l.sel].unset != nil && !m.busy {
			return l.rows[l.sel].unset(m), true
		}
	default:
		return nil, false
	}
	return nil, true
}

// lines draws the list w cells wide; sel is the selected row's last
// line (its help or note), so the whole setting scrolls into view, hits what a click on each line picks: a setting's own line is its
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
		value := r.value(m)
		btns := ""
		if r.adjust != nil {
			btns = adjustButtons
		}
		from := ""
		if r.from != nil {
			if f := r.from(m); f != "" {
				from = " · " + f
			}
		}
		text := fit(r.label, lw) + "  " + value + btns + from
		if i == l.sel {
			out = append(out, styleSel.Render(fit(text, w)))
		} else {
			out = append(out, styleHead.Render(fit(r.label, lw))+"  "+styleAccent.Render(value)+styleHead.Render(btns)+styleFaint.Render(from))
		}
		out = append(out, faintLines(r.help, w)...)
		if r.note != nil {
			out = append(out, r.note(m)...)
		}
		if i == l.sel {
			sel = len(out) - 1
		}
		for len(hits) < len(out) {
			hits = append(hits, i+helpHit)
		}
	}
	return out, sel, hits
}

// helpHit marks a setting's help lines in its list's hits.
const helpHit = 1 << 16

// adjustButtons follow a number's value: a click on − or + is - or +.
const adjustButtons = "  −  +"

// click selects the clicked setting; a click on its own line changes it,
// as enter does, and on a number's − or + (col is the column in the
// line) as - or + do.
func (l *settingsList) click(m *dash, item, col int) tea.Cmd {
	if item >= helpHit {
		l.sel = moveSel(item-helpHit, 0, len(l.rows))
		return nil
	}
	l.sel = moveSel(item, 0, len(l.rows))
	key := "enter"
	if r := l.rows[l.sel]; r.adjust != nil {
		lw := 0
		for _, r := range l.rows {
			lw = max(lw, len([]rune(r.label)))
		}
		// − is 2 cells after the value, + 3 after −; a cell either side
		// counts.
		minus := lw + 2 + ansi.StringWidth(r.value(m)) + 2
		switch {
		case col >= minus-1 && col <= minus+1:
			key = "-"
		case col >= minus+2 && col <= minus+4:
			key = "+"
		}
	}
	cmd, _ := l.key(m, keyMsg(key))
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

// settingsView is the , popup: the settings of every project, in two
// tabs: General, and All projects (the project settings every project
// follows unless it sets its own).
type settingsView struct {
	tab  int
	tabs [2]settingsList
	// top is each tab's first line shown; shown the selection's line
	// (plus one) it last scrolled to (projectView.scroll).
	top, shown [2]int
}

// settingsTabs are the , popup's tabs.
var settingsTabs = [2]string{"General", "All projects"}

func (m *dash) openSettings() {
	sv := &settingsView{tabs: [2]settingsList{{rows: globalSettings()}, {rows: allProjectsSettings()}}}
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
		{label: "Icons", help: "The glyphs of the sidebar's tree, states and progress: Nerd Font icons, Unicode shapes, or ASCII for any font. Auto picks Nerd Font icons in Ghostty, Unicode elsewhere. Each console decides auto for itself.",
			value: func(m *dash) string {
				v := iconsSetting()
				if v == IconsAuto {
					return v + " (" + ic().name + ")"
				}
				return v
			},
			change: func(m *dash) tea.Cmd {
				cur := iconsSetting()
				next := IconChoices[(slices.Index(IconChoices, cur)+1)%len(IconChoices)]
				setIcons(next)
				return m.setSetting("ui", "icons", next, "icons: "+next+"; consoles started earlier pick them up when they next open")
			}},
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
	switch s := k.String(); s {
	case "esc":
		m.pop()
		return nil
	case "right", "l", "left", "h":
		sv.tab = 1 - sv.tab
		return nil
	case "1", "2":
		sv.tab = int(s[0] - '1')
		return nil
	}
	cmd, _ := sv.tabs[sv.tab].key(m, k)
	return cmd
}

func (sv *settingsView) render(m *dash) string {
	w := m.inner(settingsWidth)
	lines, sel, hits := sv.tabs[sv.tab].lines(m, w)
	lines = append(lines, "")
	keys := "enter change · ↑ ↓ move · ← → tabs · esc back"
	if sv.tab == 0 {
		lines = append(lines, faintLines("A project's own settings (starting threads, yolo mode, remote control, …) are in its popup: a on the dashboard, prefix+a in a session.", w)...)
	} else {
		lines = append(lines, faintLines("Every project follows these, a new one too, unless it sets its own in its popup (a on the dashboard, prefix+a in a session); x there makes it follow these again.", w)...)
		keys = "enter change · + - number · ↑ ↓ move · ← → tabs · esc back"
	}
	head := []string{sv.tabBar(), ""}
	all := append([]int{tabHit, noHit}, hits...)
	for len(all) < len(head)+len(lines) {
		all = append(all, noHit)
	}
	// As tall as the longer tab, so switching tabs doesn't resize it.
	height := 0
	for i := range sv.tabs {
		l, _, _ := sv.tabs[i].lines(m, w)
		height = max(height, len(head)+len(l)+3)
	}
	b := box{title: "settings", head: head, body: lines, sel: -1, hits: all, keys: keys, width: settingsWidth, height: height}
	b.scroll = sv.scroll(m.boxRows(b)-len(head), sel, len(lines))
	return m.popup(b)
}

// scroll is the open tab's first line shown, as projectView.scroll.
func (sv *settingsView) scroll(rows, sel, n int) int {
	t := sv.top[sv.tab]
	if sel >= 0 && sel+1 != sv.shown[sv.tab] {
		sv.shown[sv.tab] = sel + 1
		t = min(t, sel)
		t = max(t, sel-rows+1)
	}
	t = min(max(t, 0), max(n-max(rows, 1), 0))
	sv.top[sv.tab] = t
	return t
}

func (sv *settingsView) tabBar() string {
	var parts []string
	for i, name := range settingsTabs {
		label := fmt.Sprintf(" %d %s ", i+1, name)
		if i == sv.tab {
			parts = append(parts, styleSel.Render(label))
		} else {
			parts = append(parts, styleFaint.Render(label))
		}
	}
	return strings.Join(parts, " ")
}

func (sv *settingsView) click(m *dash, item, col int, _ bool) tea.Cmd {
	if item == tabHit {
		x := 0
		for i, name := range settingsTabs {
			w := len([]rune(fmt.Sprintf("%d %s", i+1, name))) + 2
			if col >= x && col < x+w {
				sv.tab = i
			}
			x += w + 1
		}
		return nil
	}
	return sv.tabs[sv.tab].click(m, item, col)
}

func (sv *settingsView) wheel(m *dash, d int) { sv.tabs[sv.tab].key(m, arrow(d)) }

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
	// no is the footer message on any other key.
	no string
}

func (m *dash) confirm(question string, yes func() tea.Cmd) {
	m.confirmNo(question, "unchanged", yes)
}

// confirmNo is confirm, saying no in the footer on any other key.
func (m *dash) confirmNo(question, no string, yes func() tea.Cmd) {
	m.push(&confirmView{question: question, yes: yes, no: no})
}

func (cv *confirmView) key(m *dash, k tea.KeyPressMsg) tea.Cmd {
	m.pop()
	if k.String() == "y" {
		return cv.yes()
	}
	m.msg = cv.no
	return nil
}

func (cv *confirmView) render(m *dash) string {
	return m.popup(box{body: wrapLines(cv.question, m.inner(promptWidth)), sel: -1, keys: "y yes · any other key no", width: promptWidth})
}

// projectSettings are a project's own settings (§11.2), for its popup.
func projectSettings(slug string) []setting { return safetySettings(slug) }

// allProjectsSettings are the settings every project follows unless it
// sets its own (§11.2 All projects), for the , popup.
func allProjectsSettings() []setting { return safetySettings("") }

// safetySettings are slug's settings, or with slug "" the all-projects
// ones: the same rows, written to the project's table or to [defaults].
func safetySettings(slug string) []setting {
	all := slug == ""
	table, forWho, ofWho := "projects."+slug, "for "+slug, "of "+slug
	if all {
		table, forWho, ofWho = config.DefaultsTable, "for all projects", "of each project"
	}
	safety := func(m *dash) config.Safety {
		if all {
			if m.data.Defaults != nil {
				return *m.data.Defaults
			}
		} else if p := m.projectData(slug); p != nil && p.Safety != nil {
			return *p.Safety
		}
		return config.Defaults
	}
	// set saves a setting and shows it at once, before the next poll, so
	// quick presses of + build on each other.
	set := func(m *dash, key string, value any, msg string, apply func(*config.Safety)) tea.Cmd {
		s := safety(m)
		apply(&s)
		if all {
			m.data.Defaults = &s
		} else if p := m.projectData(slug); p != nil {
			p.Safety = &s
			if !slices.Contains(p.Own, key) {
				p.Own = append(slices.Clip(p.Own), key)
			}
		}
		return m.setSetting(table, key, value, msg)
	}
	toggle := func(key string, get func(config.Safety) bool, put func(*config.Safety, bool), what string) func(m *dash) tea.Cmd {
		return func(m *dash) tea.Cmd {
			on := !get(safety(m))
			return set(m, key, on, what+" "+onOff(on)+" "+forWho, func(s *config.Safety) { put(s, on) })
		}
	}
	// scope fills in where each row's value comes from: on a project, its
	// own value or all projects', which x goes back to; on all projects,
	// the projects that set their own.
	scope := func(rows []setting, keys [][]string, words []func(config.Safety) string) []setting {
		for i := range rows {
			keys := keys[i]
			if all {
				rows[i].note = joinNotes(rows[i].note, func(m *dash) []string { return ownNote(m, keys) })
				continue
			}
			word := words[i]
			rows[i].from = func(m *dash) string {
				if p := m.projectData(slug); p != nil && !ownsAny(p.Own, keys) {
					return config.AllProjectsName
				}
				return ""
			}
			rows[i].unset = func(m *dash) tea.Cmd {
				p := m.projectData(slug)
				if p == nil || !ownsAny(p.Own, keys) {
					m.msg = "this setting of " + slug + " already follows all projects"
					return nil
				}
				msg := slug + " follows all projects in " + strings.ToLower(rows[i].label)
				if m.data.Defaults != nil {
					msg += ": " + word(*m.data.Defaults)
				}
				src := m.src
				return m.act(func() actionMsg {
					for _, k := range keys {
						if err := src.SetSetting(table, k, nil); err != nil {
							return actionMsg{err: settingsErr(err)}
						}
					}
					return actionMsg{msg: msg}
				})
			}
		}
		return rows
	}
	threads := func(m *dash) string {
		if all {
			return ""
		}
		return fmt.Sprintf(" · %d working now", m.workingThreads(slug))
	}
	startWords := func(s config.Safety) string {
		if s.StartThreads == config.StartAuto {
			return "automatically"
		}
		return "ask first"
	}
	yoloQ := "Turn yolo mode on for " + slug + "? Threads started from now on skip the agent's permission prompts (the file rules and the sandbox still hold)."
	rows := []setting{
		{label: "Start threads", help: "Ask first: the coordinator proposes threads and starts them after your go-ahead. Automatically: it starts them itself.",
			value: func(m *dash) string { return startWords(safety(m)) },
			change: func(m *dash) tea.Cmd {
				next, word := config.StartAuto, "automatically"
				if safety(m).StartThreads == config.StartAuto {
					next, word = config.StartPropose, "after asking you"
				}
				return set(m, "start_threads", next, "threads "+ofWho+" start "+word, func(s *config.Safety) { s.StartThreads = next })
			}},
		{label: "Yolo mode", help: "New threads skip the agent's permission prompts; the file rules and the sandbox still hold. Turning it on asks first.",
			value: func(m *dash) string { return onOff(safety(m).Yolo) },
			change: func(m *dash) tea.Cmd {
				if safety(m).Yolo {
					return set(m, "yolo", false, "yolo mode off "+forWho, func(s *config.Safety) { s.Yolo = false })
				}
				q := yoloQ
				if all {
					q = "Turn yolo mode on for all projects? Threads started from now on skip the agent's permission prompts in " + followers(m, []string{"yolo"}) + " (the file rules and the sandbox still hold)."
				}
				m.confirm(q, func() tea.Cmd {
					return set(m, "yolo", true, "yolo mode on "+forWho+": new threads skip permission prompts", func(s *config.Safety) { s.Yolo = true })
				})
				return nil
			}},
		{label: "Coordinator approves", help: "The coordinator may answer its threads' in-scope permission prompts (allow once, never always).",
			value: func(m *dash) string { return onOff(safety(m).CoordinatorApproves) },
			change: toggle("coordinator_approves", func(s config.Safety) bool { return s.CoordinatorApproves },
				func(s *config.Safety, on bool) { s.CoordinatorApproves = on }, "coordinator approvals")},
		{label: "Parallel threads", help: "The most threads working at once; beyond it the coordinator proposes and waits. Idle and done threads don't count. Enter or + and - change it.",
			value: func(m *dash) string {
				return fmt.Sprintf("%d", safety(m).ParallelThreads) + threads(m)
			},
			change: func(m *dash) tea.Cmd {
				n := nextStep(capSteps, safety(m).ParallelThreads)
				return set(m, "parallel_threads", n, fmt.Sprintf("up to %d threads %s work at once", n, ofWho), func(s *config.Safety) { s.ParallelThreads = n })
			},
			adjust: func(m *dash, d int) tea.Cmd {
				n := min(max(safety(m).ParallelThreads+d, 1), config.MaxParallelThreads)
				return set(m, "parallel_threads", n, fmt.Sprintf("up to %d threads %s work at once", n, ofWho), func(s *config.Safety) { s.ParallelThreads = n })
			}},
		{label: "Auto-close finished threads", help: "Close a thread when its pull request merges, or some days after it finishes (done, or its pull request merged); + and - change the days. One with uncommitted or unpushed work stays open, and the coordinator is told.",
			value: func(m *dash) string { return closeWords(safety(m)) },
			change: func(m *dash) tea.Cmd {
				next := map[string]string{config.CloseOff: config.CloseMerged, config.CloseMerged: config.CloseDays}[safety(m).AutoClose]
				if next == "" {
					next = config.CloseOff
				}
				s := safety(m)
				s.AutoClose = next
				return set(m, "auto_close", next, "auto-close "+forWho+": "+closeWords(s), func(x *config.Safety) { x.AutoClose = next })
			},
			adjust: func(m *dash, d int) tea.Cmd {
				s := safety(m)
				n := min(max(s.AutoCloseDays+d, 1), config.MaxAutoCloseDays)
				msg := fmt.Sprintf("threads %s close %s after they finish", ofWho, days(n))
				if s.AutoClose != config.CloseDays {
					msg = fmt.Sprintf("%s once auto-close is set to days after it finishes (enter)", days(n))
				}
				return set(m, "auto_close_days", n, msg, func(x *config.Safety) { x.AutoCloseDays = n })
			}},
		{label: "Complete tasks", help: "By you, or on your standing acceptance: a task in review is done once its pull request merges. x sends it back.",
			value: func(m *dash) string { return completeWords(safety(m).CompleteTasks) },
			change: func(m *dash) tea.Cmd {
				next := config.CompleteMerged
				if safety(m).CompleteTasks == config.CompleteMerged {
					next = config.CompleteUser
				}
				msg := "tasks " + ofWho + " are done " + completeWords(next)
				if next == config.CompleteUser {
					msg = "tasks " + ofWho + " are done when you accept them"
				}
				return set(m, "complete_tasks", next, msg, func(x *config.Safety) { x.CompleteTasks = next })
			}},
		{label: "Pull request follow-up", help: "Prompt a thread when its pull request's checks fail, a reviewer asks for changes, or main moves past it.",
			value: func(m *dash) string { return onOff(safety(m).PRFollowup) },
			change: toggle("pr_followup", func(s config.Safety) bool { return s.PRFollowup },
				func(s *config.Safety, on bool) { s.PRFollowup = on }, "pull request follow-up")},
		{label: "Remote control", help: "Coordinators keep remote control on, so you can continue them from another device: a new one starts with it, and tm turns it back on when it drops. prefix+r changes the running one; your off holds until it is started anew.",
			value: func(m *dash) string { return onOff(safety(m).CoordinatorRemoteControl) },
			change: toggle("coordinator_remote_control", func(s config.Safety) bool { return s.CoordinatorRemoteControl },
				func(s *config.Safety, on bool) { s.CoordinatorRemoteControl = on }, "remote control for coordinators"),
			note: func(m *dash) []string {
				if all {
					return nil
				}
				return remoteNote(safety(m).CoordinatorRemoteControl, m.data.Sessions, slug)
			}},
		{label: "Keep my checkout current", help: "Fast-forward your own checkout of each repository when its default branch is checked out, clean and only behind origin; else the overview says how far behind.",
			value: func(m *dash) string { return onOff(safety(m).FastForwardCheckout) },
			change: toggle("fast_forward_checkout", func(s config.Safety) bool { return s.FastForwardCheckout },
				func(s *config.Safety, on bool) { s.FastForwardCheckout = on }, "keeping your checkout current")},
	}
	onOffOf := func(get func(config.Safety) bool) func(config.Safety) string {
		return func(s config.Safety) string { return onOff(get(s)) }
	}
	return scope(rows,
		[][]string{{"start_threads"}, {"yolo"}, {"coordinator_approves"}, {"parallel_threads"}, {"auto_close", "auto_close_days"},
			{"complete_tasks"}, {"pr_followup"}, {"coordinator_remote_control"}, {"fast_forward_checkout"}},
		[]func(config.Safety) string{startWords, onOffOf(func(s config.Safety) bool { return s.Yolo }),
			onOffOf(func(s config.Safety) bool { return s.CoordinatorApproves }),
			func(s config.Safety) string { return fmt.Sprint(s.ParallelThreads) }, closeWords,
			func(s config.Safety) string { return completeWords(s.CompleteTasks) },
			onOffOf(func(s config.Safety) bool { return s.PRFollowup }),
			onOffOf(func(s config.Safety) bool { return s.CoordinatorRemoteControl }),
			onOffOf(func(s config.Safety) bool { return s.FastForwardCheckout })})
}

// ownsAny reports whether own (a project's own settings) has one of keys.
func ownsAny(own, keys []string) bool {
	for _, k := range keys {
		if slices.Contains(own, k) {
			return true
		}
	}
	return false
}

// ownNote names, under an all-projects setting, the projects that set
// their own value.
func ownNote(m *dash, keys []string) []string {
	var slugs []string
	for _, p := range m.data.Projects {
		if ownsAny(p.Own, keys) {
			slugs = append(slugs, p.Slug)
		}
	}
	switch len(slugs) {
	case 0:
		return nil
	case 1:
		return []string{styleFaint.Render(slugs[0] + " sets its own")}
	}
	return []string{styleFaint.Render(joinAnd(slugs) + " set their own")}
}

// followers names the projects that follow all projects in keys, for
// the yolo question.
func followers(m *dash, keys []string) string {
	var slugs []string
	for _, p := range m.data.Projects {
		if !ownsAny(p.Own, keys) {
			slugs = append(slugs, p.Slug)
		}
	}
	if len(slugs) == 0 {
		return "every project that doesn't set it itself"
	}
	return joinAnd(slugs) + " and every new project"
}

// joinAnd joins words as "a, b and c".
func joinAnd(words []string) string {
	if len(words) < 2 {
		return strings.Join(words, "")
	}
	return strings.Join(words[:len(words)-1], ", ") + " and " + words[len(words)-1]
}

// joinNotes is a note of a's lines, then b's.
func joinNotes(a, b func(m *dash) []string) func(m *dash) []string {
	if a == nil {
		return b
	}
	return func(m *dash) []string { return append(a(m), b(m)...) }
}

// capSteps are the caps enter steps through; + and - fine-tune.
var capSteps = []int{1, 2, 3, 5, 10, 15, 20}

// nextStep is the first step above cur, or the first one.
func nextStep(steps []int, cur int) int {
	for _, s := range steps {
		if s > cur {
			return s
		}
	}
	return steps[0]
}

// closeWords is the auto-close setting as the popup shows it.
func closeWords(s config.Safety) string {
	switch s.AutoClose {
	case config.CloseOff:
		return "off"
	case config.CloseDays:
		return days(s.AutoCloseDays) + " after it finishes"
	}
	return "when its pull request merges"
}

// completeWords is the complete-tasks setting as the popup shows it.
func completeWords(v string) string {
	if v == config.CompleteMerged {
		return "when merged"
	}
	return "by you"
}

func days(n int) string {
	if n == 1 {
		return "1 day"
	}
	return fmt.Sprintf("%d days", n)
}

// workingThreads counts slug's threads that count toward its cap
// (thread.IsWorking).
func (m *dash) workingThreads(slug string) int {
	p := m.projectData(slug)
	if p == nil {
		return 0
	}
	recs := make([]*thread.Record, 0, len(p.Threads))
	for _, t := range p.Threads {
		recs = append(recs, t.Record)
	}
	return thread.Working(recs, m.data.Sessions)
}

// remoteNote says when the running coordinator's remote control differs
// from the setting: prefix+r changed it since it started, or it dropped
// and the ticker turns it back on once the coordinator is idle.
func remoteNote(setting bool, sessions []proto.SessionInfo, slug string) []string {
	for _, s := range sessions {
		if s.Role == proto.RoleCoordinator && s.Project == slug && s.RemoteControl != setting {
			if setting && !s.RemoteHeld {
				return []string{styleWarn.Render("the running coordinator has it off; tm turns it on once the coordinator is idle")}
			}
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
