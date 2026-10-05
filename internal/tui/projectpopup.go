package tui

import (
	"cmp"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/theclifmeister/termilator/internal/config"
	"github.com/theclifmeister/termilator/internal/project"
	"github.com/theclifmeister/termilator/internal/proto"
	"github.com/theclifmeister/termilator/internal/tasks"
)

// The project popup (a on the dashboard, prefix+a in a session;
// docs/SPEC.md §4): everything about one project in tabs. Only the
// repositories and the settings change here, on the human's keypress;
// the inbox and the tasks are read-only, since the coordinator handles
// them, save that d, a and x ask it to act on a task (asks.go). Which
// tab is open is this console's own, as every popup.

// The tabs, in order.
const (
	tabOverview = iota
	tabInbox
	tabTasks
	tabSettings
	tabKeys
	tabCount
)

var tabNames = [tabCount]string{"Overview", "Inbox", "Tasks", "Settings", "Keys"}

type projectView struct {
	slug string
	tab  int
	// sel is each tab's selection: a repository, an inbox item, a task,
	// a setting, a line of the keys.
	sel [tabCount]int
	// top is each tab's first content line shown; shown is the
	// selection's line (plus one) it last scrolled to, so the content
	// scrolls to a selection that moved, not back to one scrolled away
	// from; nudge is how far the keys and the wheel scrolled it since,
	// applied when it's drawn (after following the selection).
	top, shown, nudge [tabCount]int
	board             *tasks.Board
	reviews           map[int]Review // the tasks in review, by id
	settings          settingsList
	// pick is the task to select once the board loads, 0 for none.
	pick int
}

func (m *dash) projectPopup(string) tea.Cmd {
	slug := m.needProject()
	if slug == "" {
		return nil
	}
	pv := &projectView{slug: slug, settings: settingsList{rows: projectSettings(slug)}}
	m.push(pv)
	return m.loadBoard(slug)
}

// showTask opens slug's project popup on the Tasks tab with task id
// selected: enter on a NEEDS YOU task. It only shows the task; the
// coordinator acts on it.
func (m *dash) showTask(slug string, id int) tea.Cmd {
	pv := &projectView{slug: slug, tab: tabTasks, pick: id, settings: settingsList{rows: projectSettings(slug)}}
	m.push(pv)
	return m.loadBoard(slug)
}

// setBoard takes a loaded board, selecting the task to pick.
func (pv *projectView) setBoard(b *tasks.Board) {
	pv.board = b
	if pv.pick == 0 {
		return
	}
	for i, t := range pv.tasks() {
		if t.ID == pv.pick {
			pv.sel[tabTasks] = i
		}
	}
	pv.pick = 0
}

// projectPopupView is the open project popup, if any.
func (m *dash) projectPopupView() *projectView {
	for i := len(m.stack) - 1; i >= 0; i-- {
		if p, ok := m.stack[i].(*projectView); ok {
			return p
		}
	}
	return nil
}

func (pv *projectView) data(m *dash) ProjectData {
	if p := m.projectData(pv.slug); p != nil {
		return *p
	}
	return ProjectData{Slug: pv.slug}
}

func (pv *projectView) key(m *dash, k tea.KeyPressMsg) tea.Cmd {
	if pv.tab == tabTasks {
		// The task keys (asks.go).
		if t := pv.selTask(); t != nil {
			switch k.String() {
			case "enter":
				return m.openTask(pv, t)
			case "D", "A", "x", "c":
				return m.taskKey(pv.slug, t, k.String())
			}
		}
	}
	switch s := k.String(); s {
	case "esc":
		m.pop()
		return nil
	case "right", "l":
		pv.tab = (pv.tab + 1) % tabCount
		return nil
	case "left", "h":
		pv.tab = (pv.tab + tabCount - 1) % tabCount
		return nil
	case "1", "2", "3", "4", "5":
		pv.tab = int(s[0] - '1')
		return nil
	}
	s := k.String()
	d := scrollKeys[s]
	if pv.tab == tabSettings {
		before := pv.settings.sel
		cmd, _ := pv.settings.key(m, k)
		if pv.settings.sel == before {
			pv.nudge[tabSettings] += d
		}
		return cmd
	}
	// The arrows move the selection; past the first or last item (or in
	// a tab without one) they scroll the content.
	before := pv.sel[pv.tab]
	if n := pv.count(m); n > 0 {
		pv.sel[pv.tab] = moveSel(before, d, n)
	}
	if pv.sel[pv.tab] == before {
		pv.nudge[pv.tab] += d
	}
	switch {
	case pv.tab == tabOverview:
		return pv.repoKey(m, k)
	}
	return nil
}

// selTask is the Tasks tab's selected task, nil before the board loads.
func (pv *projectView) selTask() *tasks.Task {
	if ts := pv.tasks(); pv.board != nil && pv.sel[tabTasks] < len(ts) {
		return ts[pv.sel[tabTasks]]
	}
	return nil
}

// openTask shows task t of the popup's project over it, as the t list
// shows one; esc comes back to the popup.
func (m *dash) openTask(pv *projectView, t *tasks.Task) tea.Cmd {
	b := &boardView{slug: pv.slug, reviews: pv.reviews, open: true, back: true}
	b.setBoard(pv.board)
	b.selectID(t.ID)
	m.push(b)
	return nil
}

// count is how many things the tab selects among.
func (pv *projectView) count(m *dash) int {
	switch pv.tab {
	case tabOverview:
		return len(pv.data(m).Repos)
	case tabInbox:
		return len(pv.data(m).Items)
	case tabTasks:
		return len(pv.tasks())
	}
	return 0
}

// repoKey adds (+) or removes (x) a repository.
func (pv *projectView) repoKey(m *dash, k tea.KeyPressMsg) tea.Cmd {
	slug, repos := pv.slug, pv.data(m).Repos
	switch k.String() {
	case "+", "n":
		m.prompt("add a repository (its directory): ", m.cwd, func(path string) tea.Cmd {
			path = absPath(path, m.cwd)
			return m.setRepo(slug, path, true, "added "+path)
		})
	case "x", "delete", "backspace":
		i := pv.sel[tabOverview]
		if i >= len(repos) {
			return nil
		}
		path := repos[i]
		m.confirm("Remove "+path+" from "+slug+"'s repositories? The directory itself stays.", func() tea.Cmd {
			return m.setRepo(slug, path, false, "removed "+path)
		})
	}
	return nil
}

func (m *dash) setRepo(slug, path string, add bool, msg string) tea.Cmd {
	src := m.src
	return m.act(func() actionMsg {
		if err := src.SetRepo(slug, path, add); err != nil {
			return actionMsg{err: err}
		}
		return actionMsg{msg: msg}
	})
}

// absPath reads a typed path: ~ is the home directory, a relative path
// is from cwd, links are resolved.
func absPath(p, cwd string) string {
	if rest, ok := strings.CutPrefix(p, "~"); ok && (rest == "" || rest[0] == '/') {
		if h, err := os.UserHomeDir(); err == nil {
			p = h + rest
		}
	}
	if !filepath.IsAbs(p) {
		p = filepath.Join(cwd, p)
	}
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return filepath.Clean(p)
}

// tasks are the live tasks in board order: needs you, in motion, on deck.
func (pv *projectView) tasks() []*tasks.Task {
	if pv.board == nil {
		return nil
	}
	return listed(pv.board)
}

// listed are a board's tasks as the task lists show them, by group:
// done ones last, so x can still send one back.
func listed(b *tasks.Board) []*tasks.Task {
	var out []*tasks.Task
	for _, g := range tasks.Groups {
		for _, t := range b.Tasks {
			if tasks.GroupOf(t.Status) == g {
				out = append(out, t)
			}
		}
	}
	return out
}

func (pv *projectView) render(m *dash) string { return m.popup(pv.box(m)) }

// projectSize is the project popup's width and lines (tab bar and
// content) in a window w×h: from the window alone, never from a tab's
// content, so switching tabs doesn't move or resize it.
func projectSize(w, h int) (int, int) {
	return min(max(w*9/10, 72), 120), min(max(h*9/10, 14), 48)
}

func (pv *projectView) box(m *dash) box {
	width, height := projectSize(m.w, m.bodyRows())
	w := m.inner(width)
	p := pv.data(m)
	var body []string
	var hits []int
	sel := -1
	keys := "← → tabs · esc close"
	switch pv.tab {
	case tabOverview:
		body, sel, hits = pv.overview(m, p, w)
		keys = "+ add repository · x remove it · " + keys
	case tabInbox:
		body, sel, hits = inboxLines(p.Items, pv.sel[tabInbox], w)
		body = append(body, "", styleFaint.Render("Read-only: the coordinator handles these."))
	case tabTasks:
		body, sel, hits = pv.taskLines(m, w)
		// Short, so the task's keys fit beside a 32-column sidebar.
		keys = joinKeys(taskKeys(pv.selTask()), "enter show · esc close")
	case tabSettings:
		body, sel, hits = pv.settings.lines(m, w)
		keys = "enter change · + - number · ↑ ↓ move · " + keys
	case tabKeys:
		// The same list as the help.
		body = keyLines(w)
		keys = "↑ ↓ scroll · " + keys
	}
	head := []string{pv.tabBar(p), ""}
	all := append([]int{tabHit, noHit}, hits...)
	for len(all) < len(head)+len(body) {
		all = append(all, noHit)
	}
	b := box{title: pv.slug, head: head, body: body, sel: -1, hits: all, keys: keys, width: width, height: height}
	b.scroll = pv.scroll(m.boxRows(b)-len(head), sel, len(body))
	return b
}

// scroll is the open tab's first content line shown, rows at a time of
// n lines: where it was, moved just enough to show the line sel when the
// selection moved there, then by the nudges since.
func (pv *projectView) scroll(rows, sel, n int) int {
	t := pv.top[pv.tab]
	if sel >= 0 && sel+1 != pv.shown[pv.tab] {
		pv.shown[pv.tab] = sel + 1
		t = min(t, sel)
		t = max(t, sel-rows+1)
	}
	t += pv.nudge[pv.tab]
	pv.nudge[pv.tab] = 0
	t = min(max(t, 0), max(n-max(rows, 1), 0))
	pv.top[pv.tab] = t
	return t
}

// tabHit is the tab bar's line: the column picks the tab.
const tabHit = -2

// click picks a tab on the tab bar, else the clicked line's repository,
// inbox item, task or setting (which it changes, as enter does).
func (pv *projectView) click(m *dash, item, col int, _ bool) tea.Cmd {
	if item == tabHit {
		if t := pv.tabAt(pv.data(m), col); t >= 0 {
			pv.tab = t
		}
		return nil
	}
	if pv.tab == tabSettings {
		return pv.settings.click(m, item, col)
	}
	pv.sel[pv.tab] = item
	return nil
}

// tabAt is the tab at column col of the tab bar, -1 for none.
func (pv *projectView) tabAt(p ProjectData, col int) int {
	x := 0
	for i := range tabNames {
		w := len([]rune(pv.tabLabel(p, i))) + 2
		if col >= x && col < x+w {
			return i
		}
		x += w + 1
	}
	return -1
}

// wheel moves the selection as the arrows do, or scrolls a tab without
// one by three lines.
func (pv *projectView) wheel(m *dash, d int) {
	if pv.tab == tabKeys {
		pv.nudge[tabKeys] += 3 * d
		return
	}
	pv.key(m, arrow(d))
}

// tabBar names the tabs, the open one in reverse video, with the inbox's
// count.
func (pv *projectView) tabBar(p ProjectData) string {
	var parts []string
	for i := range tabNames {
		label := pv.tabLabel(p, i)
		if i == pv.tab {
			parts = append(parts, styleSel.Render(" "+label+" "))
		} else {
			parts = append(parts, styleFaint.Render(" "+label+" "))
		}
	}
	return strings.Join(parts, " ")
}

// tabLabel is tab i's label: its number and name, with the inbox's
// count.
func (pv *projectView) tabLabel(p ProjectData, i int) string {
	label := fmt.Sprintf("%d %s", i+1, tabNames[i])
	if i == tabInbox && len(p.Items) > 0 {
		label += fmt.Sprintf(" %d", len(p.Items))
	}
	return label
}

// overview is the project's name, goal, repositories, machine and
// agents.
func (pv *projectView) overview(m *dash, p ProjectData, w int) ([]string, int, []int) {
	field := func(label string) string { return styleFaint.Render(fmt.Sprintf("%-14s", label)) }
	var out []string
	repoAt := map[int]int{} // a repository's line: its index
	out = append(out, field("Project")+styleHead.Render(cmp.Or(oneLine(p.Name), p.Slug))+styleFaint.Render("  "+p.Slug))
	if g := strings.TrimSpace(p.Goal); g == "" {
		out = append(out, field("Goal")+styleFaint.Render("none set; ask the coordinator to set one"))
	} else {
		for i, l := range wrapLines(oneLine(g), w-14) {
			label := field("")
			if i == 0 {
				label = field("Goal")
			}
			out = append(out, label+l)
		}
	}
	out = append(out, "")
	sel := -1
	if len(p.Repos) == 0 {
		out = append(out, field("Repositories")+styleFaint.Render("none; + adds one"))
	}
	for i, r := range p.Repos {
		label := field("")
		if i == 0 {
			label = field("Repositories")
		}
		repoAt[len(out)] = i
		if i == pv.sel[tabOverview] {
			sel = len(out)
			out = append(out, label+styleSel.Render(fit(r, w-14)))
		} else {
			out = append(out, label+r)
		}
		if n := p.Checkouts[r]; n != "" {
			out = append(out, field("")+styleWarn.Render(fit(n, w-14)))
		}
	}
	out = append(out, "")
	host, _ := os.Hostname()
	out = append(out, field("Machines")+"this one"+styleFaint.Render("  "+host))
	out = append(out, "")
	out = append(out, field("Coordinator")+pv.coordinatorLine(m, p))
	threads := map[string][]string{}
	for _, t := range p.Threads {
		a := cmp.Or(t.Agent, DefaultAgent)
		threads[a] = append(threads[a], t.ID)
	}
	if len(threads) == 0 {
		out = append(out, field("Threads")+styleFaint.Render("none open"))
	}
	agents := make([]string, 0, len(threads))
	for a := range threads {
		agents = append(agents, a)
	}
	slices.Sort(agents)
	for i, a := range agents {
		label := field("")
		if i == 0 {
			label = field("Threads")
		}
		out = append(out, label+a+styleFaint.Render(" · "+strings.Join(threads[a], " ")))
	}
	s := config.Defaults
	if p.Safety != nil {
		s = *p.Safety
	}
	modes := []string{map[bool]string{true: "start after asking you", false: "start automatically"}[s.StartThreads != config.StartAuto]}
	if s.Yolo {
		modes = append(modes, "yolo mode")
	}
	out = append(out, field("")+styleFaint.Render(strings.Join(modes, " · ")+" (Settings tab)"))
	c := p.Counts
	needs := fmt.Sprintf("%d needs you", c["needs_you"])
	if c["needs_you"] > 0 {
		needs += " (3 → Tasks)"
	}
	out = append(out, "", field("Tasks")+fmt.Sprintf("%s · %d in motion · %d on deck · %d done", needs, c["in_motion"], c["on_deck"], c["done"]))
	if p.Err != "" {
		out = append(out, "", styleBad.Render("error: "+oneLine(p.Err)))
	}
	return out, sel, lineHits(len(out), repoAt)
}

// lineHits are n lines' hits: at's, noHit elsewhere.
func lineHits(n int, at map[int]int) []int {
	hits := make([]int, n)
	for i := range hits {
		hits[i] = noHit
		if v, ok := at[i]; ok {
			hits[i] = v
		}
	}
	return hits
}

// coordinatorLine is the coordinator's agent and state, or the agent a
// new one runs.
func (pv *projectView) coordinatorLine(m *dash, p ProjectData) string {
	for _, s := range m.data.Sessions {
		if s.Role == proto.RoleCoordinator && s.Project == p.Slug {
			line := cmp.Or(s.Agent, DefaultAgent) + styleFaint.Render(" · "+s.ID+" "+stateWord(s))
			if s.RemoteControl {
				line += styleFaint.Render(" · remote control on")
			}
			return line
		}
	}
	return config.DefaultAgent(DefaultAgent) + styleFaint.Render(" · not running; enter on the project starts it")
}

// taskLines are the live tasks, grouped, each with its steps.
func (pv *projectView) taskLines(m *dash, w int) ([]string, int, []int) {
	if pv.board == nil {
		return []string{styleFaint.Render("loading…")}, -1, nil
	}
	var out []string
	taskAt := map[int]int{} // a task's lines, its steps' too: its index
	sel := -1
	var group tasks.Group
	for i, t := range pv.tasks() {
		if g := tasks.GroupOf(t.Status); g != group {
			group = g
			st := styleTitle
			if g == tasks.NeedsYou {
				st = styleWarn.Bold(true)
			}
			if len(out) > 0 {
				out = append(out, "")
			}
			out = append(out, st.Render(strings.ToUpper(string(g))))
		}
		head := fmt.Sprintf("%-5s %s", t.Ref(), oneLine(t.Title))
		tail := string(t.Status)
		var more []string
		if len(t.Steps) > 0 {
			more = append(more, fmt.Sprintf("%d/%d", t.StepsDone(), len(t.Steps)))
		}
		if t.Thread != "" {
			more = append(more, t.Thread)
		}
		if m.asked(pv.slug, t) != "" {
			// Right after the status, and the title gives way, so it
			// shows whole; the steps and thread follow if they fit.
			tail += " · " + delegateWaiting
			head = ansi.Truncate(head, max(w-2-ansi.StringWidth(tail), 12), "…")
			for _, x := range more {
				if ansi.StringWidth(head+"  "+tail+" · "+x) <= w {
					tail += " · " + x
				}
			}
		} else {
			for _, x := range more {
				tail += " · " + x
			}
		}
		for j := range 1 + len(t.Steps) {
			taskAt[len(out)+j] = i
		}
		if i == pv.sel[tabTasks] {
			sel = len(out)
			out = append(out, styleSel.Render(fit(head+"  "+tail, w)))
		} else {
			out = append(out, fit(head+"  "+styleFaint.Render(tail), w))
		}
		for _, s := range t.Steps {
			out = append(out, fit("      "+todoGlyph(map[bool]string{true: "done"}[s.Done])+" "+oneLine(s.Text), w))
		}
	}
	if len(out) == 0 {
		out = append(out, styleFaint.Render("no tasks"))
	}
	out = append(out, "", styleFaint.Render("the coordinator changes tasks"))
	return out, sel, lineHits(len(out), taskAt)
}

// inboxLines are a project's unhandled inbox items, sel selected, and
// the item on each line.
func inboxLines(items []project.Item, sel, w int) ([]string, int, []int) {
	var lines []string
	var hits []int
	at := -1
	now := time.Now()
	for i, it := range items {
		when := fmt.Sprintf("%-6s", age(now.Sub(it.Created)))
		hits = append(hits, i)
		if i == sel {
			at = len(lines)
			lines = append(lines, styleSel.Render(fit(fmt.Sprintf("%s %s %s", fit(it.Kind, 16), when, oneLine(it.Summary)), w)))
			continue
		}
		lines = append(lines, fit(fmt.Sprintf("%s %s %s", styleWarn.Render(fit(it.Kind, 16)),
			styleFaint.Render(when), oneLine(it.Summary)), w)+reset)
	}
	if len(items) == 0 {
		lines = append(lines, styleFaint.Render("inbox empty"))
		hits = append(hits, noHit)
	}
	return lines, at, hits
}
