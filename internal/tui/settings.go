package tui

import (
	"fmt"
	"regexp"
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/theclifmeister/terminatr/internal/config"
	"github.com/theclifmeister/terminatr/internal/proto"
	"github.com/theclifmeister/terminatr/internal/thread"
	"github.com/theclifmeister/terminatr/internal/version"
)

// Settings (docs/SPEC.md §4, §11.2): plain labels, each with a line on
// what it does, changed in place with enter or space. The , popup has the
// settings of every project (the prefix key, the layout, and the project
// settings all projects follow); a project's own are the Settings tab of
// its popup (a). The UI never names the file or its keys.
//
// Where they live: the prefix key and the projects' settings are the human's, in the settings file, shared by every
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
	// unset runs on x: a project's own value goes,
	// so it follows all projects again.
	unset func(m *dash) tea.Cmd
}

// settingsList is a list of settings with a selection.
type settingsList struct {
	rows []setting
	sel  int
}

func (l *settingsList) key(m *dash, k tea.KeyPressMsg) (tea.Cmd, bool) {
	var run func(m *dash) tea.Cmd
	if l.sel < len(l.rows) {
		r := l.rows[l.sel]
		switch k.String() {
		case "enter", "space", " ":
			if r.change != nil {
				run = r.change
			}
		case "+", "-":
			if r.adjust != nil {
				d := map[bool]int{true: -1, false: 1}[k.String() == "-"]
				run = func(m *dash) tea.Cmd { return r.adjust(m, d) }
			}
		case "x":
			if r.unset != nil {
				run = r.unset
			}
		}
	}
	switch k.String() {
	case "up", "k", "down", "j", "pgup", "pgdown":
		l.sel = moveSel(l.sel, scrollKeys[k.String()], len(l.rows))
	case "enter", "space", " ", "+", "-", "x":
		if run == nil {
			break
		}
		if m.busy {
			// A save is in flight: the key waits its turn, on the row it
			// was pressed on, and runs on top of the saved value.
			m.queued = append(m.queued, run)
			return nil, true
		}
		return run(m), true
	default:
		return nil, false
	}
	return nil, true
}

// runQueued runs the next key that waited for a save, if any: nil when
// none did. The rest stay queued until its save is done.
func (m *dash) runQueued() tea.Cmd {
	for len(m.queued) > 0 {
		run := m.queued[0]
		m.queued = m.queued[1:]
		if cmd := run(m); cmd != nil {
			return cmd
		}
	}
	return nil
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

// click selects the clicked setting; a click on the selected setting's
// own line changes it, as enter does, and on a number's − or + (col is
// the column in the line) as - or + do (docs/SPEC.md §4).
func (l *settingsList) click(m *dash, item, col int) tea.Cmd {
	if item >= helpHit {
		l.sel = moveSel(item-helpHit, 0, len(l.rows))
		return nil
	}
	// A click selects a setting; a click on the selected one changes it,
	// as enter does (or steps a number with its − +).
	if i := moveSel(item, 0, len(l.rows)); i != l.sel {
		l.sel = i
		return nil
	}
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
	m.catalogs = m.src.Catalogs()
	sv := &settingsView{tabs: [2]settingsList{{rows: globalSettings(m.src.ModsNote())}, {rows: allProjectsSettings()}}}
	m.push(sv)
}

func globalSettings(note string) []setting {
	return []setting{
		{label: "Prefix key", help: "Starts the session commands, written prefix+<key> in hints. Inside tmux, which takes ctrl+b, pick another. Enter, then press the new one.",
			value:  func(m *dash) string { return m.prefix },
			change: func(m *dash) tea.Cmd { m.push(&captureView{}); return nil }},
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
		{label: "Context hint", help: "Tell a coordinator to consider /clear once its context window is this full: a toast in its pane, a bell and a yellow percent on its sidebar row (red from 80%). Its context lives in files, so a clear loses nothing. Off never says it.",
			value: func(m *dash) string {
				if m.data.ContextHint <= 0 {
					return "off"
				}
				return fmt.Sprintf("%d%%", m.data.ContextHint)
			},
			change: func(m *dash) tea.Cmd {
				next := ContextHintChoices[(slices.Index(ContextHintChoices, m.data.ContextHint)+1)%len(ContextHintChoices)]
				m.data.ContextHint = next
				return m.setSetting("ui", "context_hint", next, "context hint "+contextHintWords(next))
			}},
		{label: "Models", help: "Each agent's models a thread may run, a line on when each fits, and the default. Agents release new ones often: add, change or remove them here. Enter lists them.",
			value:  func(m *dash) string { return catalogWords(m.catalogs) },
			change: func(m *dash) tea.Cmd { m.openCatalog(); return nil }},
		{label: "Mods", help: "Load terminatr's mod in agent panes (early access). Needs " + note + " or newer. Applies to sessions launched after the change.",
			value: func(m *dash) string { return onOff(m.data.Mods) },
			change: func(m *dash) tea.Cmd {
				on := !m.data.Mods
				m.data.Mods = on
				msg := "mods off; sessions launched from now on load no mod"
				if on {
					msg = "mods on; sessions launched from now on load the mod"
				}
				return m.setSetting("mods", "enabled", on, msg)
			}},
		{label: "Mods band", help: "The band, status entry and toast in the pane; the feed still runs. Only matters while Mods is on. Applies to sessions launched after the change.",
			value: func(m *dash) string { return onOff(m.data.ModsBand) },
			from: func(m *dash) string {
				if !m.data.Mods {
					return "needs Mods on"
				}
				return ""
			},
			change: func(m *dash) tea.Cmd {
				on := !m.data.ModsBand
				m.data.ModsBand = on
				return m.setSetting("mods", "band", on, "mods band "+onOff(on)+"; sessions launched from now on follow it")
			}},
		{label: "Mods pane", help: "Open the /tm dashboard pane beside the coordinator when it starts, where it docks as a sidebar (fullscreen, 144+ columns). Off by default: in tm the info panel beside the coordinator shows the same; /tm is for Claude desktop and Remote Control, and opens or closes it either way. Only matters while Mods is on. Applies to coordinators launched after the change.",
			value: func(m *dash) string { return onOff(m.data.ModsPane) },
			from: func(m *dash) string {
				if !m.data.Mods {
					return "needs Mods on"
				}
				return ""
			},
			change: func(m *dash) tea.Cmd {
				on := !m.data.ModsPane
				m.data.ModsPane = on
				return m.setSetting("mods", "pane", on, "mods pane "+onOff(on)+"; coordinators launched from now on follow it")
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
	w := m.inner(viewWidth)
	lines, sel, hits := sv.tabs[sv.tab].lines(m, w)
	lines = append(lines, "")
	keys := "enter change · ↑ ↓ move · ← → tabs · esc close"
	if sv.tab == 0 {
		lines = append(lines, faintLines("A project's own settings (starting threads, yolo mode, remote control, …) are in its popup: a on the dashboard, prefix+a in a session.", w)...)
	} else {
		lines = append(lines, faintLines("Every project follows these, a new one too, unless it sets its own in its popup (a on the dashboard, prefix+a in a session); x there makes it follow these again.", w)...)
		keys = "enter change · + - number · ↑ ↓ move · ← → tabs · esc close"
	}
	about := aboutLines(m, w)
	head := append([]string{sv.tabBar()}, about...)
	head = append(head, "")
	all := []int{tabHit}
	for range len(head) - 1 {
		all = append(all, noHit)
	}
	all = append(all, hits...)
	for len(all) < len(head)+len(lines) {
		all = append(all, noHit)
	}
	// As tall as the longer tab, so switching tabs doesn't resize it.
	height := 0
	for i := range sv.tabs {
		l, _, _ := sv.tabs[i].lines(m, w)
		height = max(height, len(head)+len(l)+3)
	}
	b := box{title: "Settings", head: head, body: lines, sel: -1, hits: all, keys: keys}
	b.scroll = sv.scroll(m.boxRows(b)-len(head), sel, len(lines))
	return m.popup(b)
}

// aboutLines is the popup's faint About line: this tm, the server it
// talks to (and when that is another build, which a restart fixes) and
// whether the daily check for a newer release is on.
func aboutLines(m *dash, w int) []string {
	text := "About: tm " + version.Version + " · server "
	switch s := m.data.Server; {
	case !m.loaded || !m.data.ServerOK || s.Version == "":
		text += "unknown"
	case s.Build != version.BuildID():
		text += s.Version + " (another build)"
	default:
		text += s.Version
	}
	text += " · update check " + onOff(m.data.UpdateCheck)
	return faintLines(text, w)
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
	return m.popup(box{title: "Prefix key", body: lines, sel: -1, keys: "esc cancel", dialog: true})
}

// confirmView asks a yes/no question: y runs yes, n or esc says no, and
// no other key does anything (enter included: it is no answer).
type confirmView struct {
	title, question string
	yes             func() tea.Cmd
	// no is the footer message on n or esc.
	no string
}

// confirmKeys are a yes/no question's action row, the same everywhere.
const confirmKeys = "y yes · n no · esc cancel"

func (m *dash) confirm(title, question string, yes func() tea.Cmd) {
	m.confirmNo(title, question, "unchanged", yes)
}

// confirmNo is confirm, saying no in the footer on n or esc.
func (m *dash) confirmNo(title, question, no string, yes func() tea.Cmd) {
	m.push(&confirmView{title: title, question: question, yes: yes, no: no})
}

func (cv *confirmView) key(m *dash, k tea.KeyPressMsg) tea.Cmd {
	switch k.String() {
	case "y":
		m.pop()
		return cv.yes()
	case "n", "esc":
		m.pop()
		m.msg = cv.no
	}
	return nil
}

func (cv *confirmView) render(m *dash) string {
	return m.popup(box{title: cv.title, body: wrapLines(cv.question, m.inner(dialogWidth)), sel: -1, keys: confirmKeys, dialog: true})
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
	// agentRow is an agent setting: enter picks the next agent tm knows
	// (tm agent list), so the file only ever names a known one.
	agentRow := func(label, help, key string, get func(config.Safety) string, put func(*config.Safety, string), msg string) setting {
		return setting{label: label, help: help,
			value: func(m *dash) string { return get(safety(m)) },
			change: func(m *dash) tea.Cmd {
				names := m.src.Agents()
				cur := get(safety(m))
				if len(names) == 0 || len(names) == 1 && names[0] == cur {
					m.msg = "tm knows one agent; add others to choose between them (tm agent)"
					return nil
				}
				next := names[(slices.Index(names, cur)+1)%len(names)]
				return set(m, key, next, msg+next, func(s *config.Safety) { put(s, next) })
			},
			note: func(m *dash) []string {
				if names := m.src.Agents(); len(names) > 0 && !slices.Contains(names, get(safety(m))) {
					return []string{styleWarn.Render("tm knows no agent " + get(safety(m)) + " (tm agent list); enter picks one it knows")}
				}
				return nil
			}}
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
				m.confirm("Turn on yolo mode", q, func() tea.Cmd {
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
		{label: "Auto-clear coordinator", help: "Clear the coordinator's conversation once its context reaches the hint (context_hint), only while it is idle with nothing waiting: no inbox item, queued prompt, open question or blocked thread. The project lives in files, so it reads tm context again. Off by default; each clear is journaled.",
			value: func(m *dash) string { return onOff(safety(m).AutoClear) },
			change: toggle("auto_clear", func(s config.Safety) bool { return s.AutoClear },
				func(s *config.Safety, on bool) { s.AutoClear = on }, "auto-clear for coordinators")},
		{label: "Coordinator may merge", help: "Let the coordinator run the pull request merge command (gh pr merge, or the Azure DevOps equivalent) without the agent's auto mode refusing it: tm adds a permission allow rule to the coordinator only. Threads never get it, and the guard still refuses their merges. Only has an effect while merge is coordinator (the default). Off by default.",
			value: func(m *dash) string { return onOff(safety(m).CoordinatorMerges) },
			change: toggle("coordinator_merges", func(s config.Safety) bool { return s.CoordinatorMerges },
				func(s *config.Safety, on bool) { s.CoordinatorMerges = on }, "the coordinator merging")},
		{label: "Keep my checkout current", help: "Fast-forward your own checkout of each repository when its default branch is checked out, clean and only behind origin; else the overview says how far behind.",
			value: func(m *dash) string { return onOff(safety(m).FastForwardCheckout) },
			change: toggle("fast_forward_checkout", func(s config.Safety) bool { return s.FastForwardCheckout },
				func(s *config.Safety, on bool) { s.FastForwardCheckout = on }, "keeping your checkout current")},
		agentRow("Thread agent", "The agent new threads run; the coordinator may still name another for one thread. Enter picks the next one tm knows.",
			"thread_agent", func(s config.Safety) string { return s.ThreadAgent }, func(s *config.Safety, a string) { s.ThreadAgent = a }, "new threads "+ofWho+" run "),
		agentRow("Coordinator agent", "The agent a new coordinator runs; a running one keeps its agent until it is started anew. Enter picks the next one tm knows.",
			"coordinator_agent", func(s config.Safety) string { return s.CoordinatorAgent }, func(s *config.Safety, a string) { s.CoordinatorAgent = a }, "new coordinators "+ofWho+" run "),
		{label: "Thread models", help: "The models the coordinator may pick for a thread. Enter lists them to allow or leave out; with none left out it chooses from all. A model it may not pick is refused (haiku has no auto mode, so its threads stop at every permission prompt).",
			value: func(m *dash) string { return modelWords(safety(m).Models) },
			change: func(m *dash) tea.Cmd {
				names := catalogNames(m.src.Catalogs())
				m.push(&modelsView{all: names, allow: slices.Clone(safety(m).Models),
					save: func(m *dash, list []string) tea.Cmd {
						// Every model allowed: all projects drops the line,
						// a project keeps the explicit list (x follows all).
						if all && len(list) == len(names) && !slices.ContainsFunc(list, func(n string) bool { return !slices.Contains(names, n) }) {
							s := safety(m)
							s.Models = nil
							m.data.Defaults = &s
							return m.setSetting(table, "models", nil, "the coordinator may pick any model "+forWho)
						}
						return set(m, "models", list, "the coordinator may pick "+strings.Join(list, ", ")+" "+forWho, func(s *config.Safety) { s.Models = list })
					}})
				return nil
			}},
	}
	onOffOf := func(get func(config.Safety) bool) func(config.Safety) string {
		return func(s config.Safety) string { return onOff(get(s)) }
	}
	// Keep history opens the retention ages (archiveSettings), each its
	// own setting with its own scope.
	var ages []setting
	for _, a := range archiveAges {
		ages = append(ages, setting{label: a.label, help: a.help,
			value: func(m *dash) string { return days(safety(m).ArchiveDays(a.key)) },
			change: func(m *dash) tea.Cmd {
				n := nextStep(archiveSteps, safety(m).ArchiveDays(a.key))
				return set(m, a.key, n, fmt.Sprintf("%s %s after %s", a.verb, ofWho, days(n)), func(s *config.Safety) { s.SetArchiveDays(a.key, n) })
			},
			adjust: func(m *dash, d int) tea.Cmd {
				n := min(max(safety(m).ArchiveDays(a.key)+d, 1), config.MaxArchiveDays)
				return set(m, a.key, n, fmt.Sprintf("%s %s after %s", a.verb, ofWho, days(n)), func(s *config.Safety) { s.SetArchiveDays(a.key, n) })
			}})
	}
	var ageKeys [][]string
	var ageWords []func(config.Safety) string
	for _, a := range archiveAges {
		ageKeys = append(ageKeys, []string{a.key})
		ageWords = append(ageWords, func(s config.Safety) string { return days(s.ArchiveDays(a.key)) })
	}
	ages = scope(ages, ageKeys, ageWords)
	rows = append(rows, setting{label: "Keep history", help: "How long done tasks, resolved threads, handled inbox items and journal lines stay before they move to compressed archives beside them; nothing is deleted, and a thread with unsaved work stays. Enter lists them.",
		value: func(m *dash) string { return historyWords(safety(m)) },
		change: func(m *dash) tea.Cmd {
			m.push(&historyView{title: "keep history " + forWho, list: settingsList{rows: ages}, all: all})
			return nil
		}})
	rows = scope(rows,
		[][]string{{"start_threads"}, {"yolo"}, {"coordinator_approves"}, {"parallel_threads"}, {"auto_close", "auto_close_days"},
			{"complete_tasks"}, {"pr_followup"}, {"coordinator_remote_control"}, {"auto_clear"}, {"coordinator_merges"}, {"fast_forward_checkout"}, {"thread_agent"}, {"coordinator_agent"}, {"models"}, config.ArchiveKeys},
		[]func(config.Safety) string{startWords, onOffOf(func(s config.Safety) bool { return s.Yolo }),
			onOffOf(func(s config.Safety) bool { return s.CoordinatorApproves }),
			func(s config.Safety) string { return fmt.Sprint(s.ParallelThreads) }, closeWords,
			func(s config.Safety) string { return completeWords(s.CompleteTasks) },
			onOffOf(func(s config.Safety) bool { return s.PRFollowup }),
			onOffOf(func(s config.Safety) bool { return s.CoordinatorRemoteControl }),
			onOffOf(func(s config.Safety) bool { return s.AutoClear }),
			onOffOf(func(s config.Safety) bool { return s.CoordinatorMerges }),
			onOffOf(func(s config.Safety) bool { return s.FastForwardCheckout }),
			func(s config.Safety) string { return s.ThreadAgent }, func(s config.Safety) string { return s.CoordinatorAgent },
			func(s config.Safety) string { return modelWords(s.Models) }, historyWords})
	if all {
		return rows
	}
	// The project's own state, never all projects': activate, pause,
	// archive, delete.
	return append(rows, []setting{
		{label: "Active", help: "Only an active project's coordinator and threads run, and only they come back when the server restarts. Turning it off stops them now, to resume when you turn it on again; asks first while they run. Space in the sidebar does the same.",
			value:  func(m *dash) string { return onOff(safety(m).Active) },
			change: func(m *dash) tea.Cmd { return m.toggleActive(slug, !safety(m).Active) }},
		{label: "Paused", help: "While paused the coordinator gets no nudges, threads get no pull request follow-up, and no new thread starts; the dashboard still follows their state.",
			value: func(m *dash) string { return onOff(safety(m).Paused) },
			change: func(m *dash) tea.Cmd {
				verb := "pause"
				if safety(m).Paused {
					verb = "resume"
				}
				if p := m.projectData(slug); p != nil {
					s := safety(m)
					s.Paused = verb == "pause"
					p.Safety = &s
				}
				return m.lifecycle(slug, verb)
			}},
		{label: "Archive", help: "Hide the project from the sidebar, and stop all background work for it; tm project unarchive brings it back. Not while its coordinator or threads run. Asks first.",
			value: func(m *dash) string { return "enter archives" },
			change: func(m *dash) tea.Cmd {
				m.confirmNo("Archive "+slug, "Archive "+slug+"? It leaves the sidebar, and nothing runs for it until tm project unarchive "+slug+".", "not archived", func() tea.Cmd {
					m.pop() // the project popup
					return m.lifecycle(slug, "archive")
				})
				return nil
			}},
		{label: "Delete", help: "Move the project's folder to the trash; its worktrees and branches stay. Not while its coordinator or threads run. Asks first.",
			value: func(m *dash) string { return "enter deletes" },
			change: func(m *dash) tea.Cmd {
				m.confirmNo("Delete "+slug, "Delete "+slug+"? Its folder (tasks, memory, threads' reports) moves to the trash; its worktrees and branches stay.", "not deleted", func() tea.Cmd {
					m.pop() // the project popup
					return m.lifecycle(slug, "delete")
				})
				return nil
			}},
	}...)
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

// lifecycle pauses, resumes, archives or deletes a project in the
// background, then reloads.
// toggleActive activates or deactivates a project (the sidebar's space,
// the Active row): deactivating asks first while its coordinator or
// threads run, which it stops, to resume at its activation (§5.1).
func (m *dash) toggleActive(slug string, on bool) tea.Cmd {
	set := func() tea.Cmd {
		if p := m.projectData(slug); p != nil && p.Safety != nil {
			s := *p.Safety
			s.Active = on
			p.Safety = &s
			m.rebuild()
		}
		return m.lifecycle(slug, map[bool]string{true: "activate", false: "deactivate"}[on])
	}
	if busy := runningIn(m.data.Sessions, slug); !on && len(busy) > 0 {
		m.confirmNo("Deactivate "+slug, DeactivateQuestion(slug, busy), slug+" stays active", set)
		return nil
	}
	return set()
}

func (m *dash) lifecycle(slug, verb string) tea.Cmd {
	src := m.src
	return m.act(func() actionMsg {
		msg, err := src.Lifecycle(slug, verb)
		return actionMsg{msg: msg, err: err}
	})
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

// modelWords is the models setting as the popup shows it.
func modelWords(allow []string) string {
	if len(allow) == 0 {
		return "any"
	}
	return strings.Join(allow, ", ")
}

// modelsView lists the models a thread may use, one to allow or leave
// out with enter or space; each change is saved at once. At least one
// stays allowed. An allowed name no agent's catalog lists any more is
// shown stale, never dropped unasked: enter leaves it out.
type modelsView struct {
	all   []string // every model the agents' catalogs offer
	allow []string // the allowed ones; empty is every model
	sel   int
	err   string
	// save writes the new list (never empty).
	save func(m *dash, list []string) tea.Cmd
}

func (v *modelsView) allowed(name string) bool {
	return len(v.allow) == 0 || slices.Contains(v.allow, name)
}

// rows are the catalogs' models, then the stale allowed names.
func (v *modelsView) rows() []string {
	out := slices.Clone(v.all)
	for _, n := range v.allow {
		if !slices.Contains(out, n) {
			out = append(out, n)
		}
	}
	return out
}

func (v *modelsView) stale(name string) bool { return !slices.Contains(v.all, name) }

func (v *modelsView) key(m *dash, k tea.KeyPressMsg) tea.Cmd {
	rows := v.rows()
	switch k.String() {
	case "esc", "q":
		m.pop()
	case "up", "k", "down", "j":
		v.sel = moveSel(v.sel, scrollKeys[k.String()], len(rows))
	case "enter", "space", " ":
		if v.sel >= len(rows) {
			break
		}
		name := rows[v.sel]
		var next []string
		for _, n := range rows {
			if (n == name) != v.allowed(n) { // flip name, keep the others
				next = append(next, n)
			}
		}
		if !slices.ContainsFunc(next, func(n string) bool { return !v.stale(n) }) {
			v.err = "at least one model stays allowed"
			break
		}
		v.err = ""
		v.allow = next
		if len(next) == len(v.all) && !slices.ContainsFunc(next, v.stale) {
			v.allow = nil
		}
		v.sel = min(v.sel, max(len(v.rows())-1, 0))
		return v.save(m, next)
	}
	return nil
}

func (v *modelsView) render(m *dash) string {
	lines := []string{styleFaint.Render("The models the coordinator may pick for a thread.")}
	w := m.inner(dialogWidth)
	for i, n := range v.rows() {
		mark, word := ic().todoOpen, "left out"
		if v.allowed(n) {
			mark, word = ic().todoDone, "allowed"
		}
		l := fit(mark+" "+n, 16) + "  " + styleFaint.Render(word)
		if v.stale(n) {
			l = fit(mark+" "+n, 16) + "  " + styleBad.Render("stale: no agent lists it")
		}
		if i == v.sel {
			l = styleSel.Render(fit(ansi.Strip(l), w))
		}
		lines = append(lines, l)
	}
	if len(v.all) == 0 {
		lines = append(lines, styleFaint.Render("no agent lists models"))
	}
	if v.err != "" {
		lines = append(lines, "", styleBad.Render(v.err))
	}
	return m.popup(box{title: "Thread models", body: lines, sel: -1, keys: "enter allow or leave out · ↑ ↓ move · esc back", dialog: true})
}

// archiveAges are the retention settings, as Keep history lists them.
var archiveAges = []struct{ key, label, help, verb string }{
	{"archive_tasks_days", "Done tasks", "Days a done task stays on the board before it moves to the task archive (tm task list --archived).", "done tasks move to the archive"},
	{"archive_threads_days", "Resolved threads", "Days a resolved thread's folder stays before it is packed into the threads archive; tm thread show still reads it.", "resolved threads are packed"},
	{"archive_inbox_days", "Handled inbox items", "Days a handled inbox item stays a file of its own before it is bundled into its month's archive.", "handled inbox items are bundled"},
	{"archive_journal_days", "Journal", "Days a line stays in the journal before it moves to its month's compressed file.", "journal lines move out"},
}

// archiveSteps are the ages enter steps through; + and - fine-tune.
var archiveSteps = []int{7, 14, 30, 60, 90, 180, 365}

// historyWords is Keep history's value: one age when all four agree.
func historyWords(s config.Safety) string {
	n := s.ArchiveTasksDays
	if s.ArchiveThreadsDays == n && s.ArchiveInboxDays == n && s.ArchiveJournalDays == n {
		return days(n)
	}
	return fmt.Sprintf("%d · %d · %d · %d days", s.ArchiveTasksDays, s.ArchiveThreadsDays, s.ArchiveInboxDays, s.ArchiveJournalDays)
}

// historyView lists the retention ages: enter steps one, + and - change
// it by a day, x makes a project's follow all projects again.
type historyView struct {
	title string
	list  settingsList
	all   bool
}

func (v *historyView) key(m *dash, k tea.KeyPressMsg) tea.Cmd {
	if s := k.String(); s == "esc" || s == "q" {
		m.pop()
		return nil
	}
	cmd, _ := v.list.key(m, k)
	return cmd
}

func (v *historyView) render(m *dash) string {
	lines, sel, hits := v.list.lines(m, m.inner(viewWidth))
	keys := "enter step · + - a day · x follow all projects · ↑ ↓ move · esc back"
	if v.all {
		keys = "enter step · + - a day · ↑ ↓ move · esc back"
	}
	b := box{title: v.title, body: lines, sel: sel, hits: hits, keys: keys}
	return m.popup(b)
}

func (v *historyView) click(m *dash, item, col int, _ bool) tea.Cmd {
	if item == noHit {
		return nil
	}
	return v.list.click(m, item, col)
}

func (v *historyView) wheel(m *dash, d int) { v.list.key(m, arrow(d)) }

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

// ContextHintChoices are the percents the Context hint setting steps
// through; 0 is never.
var ContextHintChoices = []int{0, 30, 40, 50, 60, 70, 80}

func contextHintWords(n int) string {
	if n <= 0 {
		return "off"
	}
	return fmt.Sprintf("at %d%% of the context window", n)
}
