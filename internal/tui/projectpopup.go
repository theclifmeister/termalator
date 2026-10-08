package tui

import (
	"cmp"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/theclifmeister/terminatr/internal/config"
	"github.com/theclifmeister/terminatr/internal/project"
	"github.com/theclifmeister/terminatr/internal/proto"
	"github.com/theclifmeister/terminatr/internal/tasks"
	"github.com/theclifmeister/terminatr/internal/thread"
)

// The project popup (a on the dashboard, prefix+a in a session;
// docs/SPEC.md §4): everything about one project in tabs. Only the
// repositories and the settings change here, on the human's keypress;
// the inbox, the tasks and the memory are read-only, since the
// coordinator keeps them, save that D, A and x ask it to act on a task
// (asks.go). Which tab is open is this console's own, as every popup.

// The tabs, in order.
const (
	tabOverview = iota
	tabInbox
	tabTasks
	tabSettings
	tabKeys
	tabMemory
	tabLibrary
	tabCount
)

var tabNames = [tabCount]string{"overview", "inbox", "tasks", "settings", "keys", "memory", "library"}

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
	// memory is the project's memory (the Memory tab), nil until it
	// loads; memErr is why it didn't.
	memory *project.Memory
	memErr error
	// library is every thread's library files, newest first (the
	// Library tab), libOK once loaded; libErr is why it didn't.
	library []thread.LibFile
	libOK   bool
	libErr  error
	// pick is the task to select once the board loads, 0 for none.
	pick int
	// doneAll lists the done tasks on the Tasks tab; they are collapsed
	// until b (the backlog, open tasks, is always listed; T135).
	doneAll bool
}

func (m *dash) projectPopup(string) tea.Cmd {
	slug := m.needProject()
	if slug == "" {
		return nil
	}
	pv := &projectView{slug: slug, settings: settingsList{rows: projectSettings(slug)}}
	m.push(pv)
	return m.loadPopup(slug)
}

// showTask opens slug's project popup on the Tasks tab, task id
// selected when it isn't 0: t, and enter on a NEEDS YOU task. It only
// shows the task; the coordinator acts on it.
func (m *dash) showTask(slug string, id int) tea.Cmd {
	return m.openTab(slug, tabTasks, id)
}

// showTab opens slug's project popup on tab: i opens the Inbox tab.
func (m *dash) showTab(slug string, tab int) tea.Cmd { return m.openTab(slug, tab, 0) }

func (m *dash) openTab(slug string, tab, pick int) tea.Cmd {
	pv := &projectView{slug: slug, tab: tab, pick: pick, settings: settingsList{rows: projectSettings(slug)}}
	m.push(pv)
	return m.loadPopup(slug)
}

// setBoard takes a loaded board, selecting the task to pick.
func (pv *projectView) setBoard(b *tasks.Board) {
	pv.board = b
	if pv.pick == 0 {
		return
	}
	if b != nil && !pv.doneAll {
		// A done task is in no list while the done tasks are collapsed:
		// show them rather than another task.
		for _, t := range b.Tasks {
			if t.ID == pv.pick && tasks.GroupOf(t.Status) == tasks.DoneG {
				pv.doneAll = true
			}
		}
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
		if k.String() == "b" && (pv.doneAll || doneCount(pv.board) > 0) {
			pv.doneAll = !pv.doneAll
			pv.sel[tabTasks] = min(pv.sel[tabTasks], max(len(pv.tasks())-1, 0))
			return nil
		}
	}
	switch s := k.String(); s {
	case "esc":
		m.pop()
		return nil
	case "right":
		pv.tab = (pv.tab + 1) % tabCount
		return nil
	case "left":
		pv.tab = (pv.tab + tabCount - 1) % tabCount
		return nil
	case "1", "2", "3", "4", "5", "6", "7":
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
	switch pv.tab {
	case tabOverview:
		return pv.repoKey(m, k)
	case tabLibrary:
		return pv.libraryKey(m, k)
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

// openTask shows task t of the popup's project over it; esc comes back
// to the popup.
func (m *dash) openTask(pv *projectView, t *tasks.Task) tea.Cmd {
	m.push(&taskView{pv: pv, id: t.ID})
	return nil
}

// taskView is one task of a project popup's Tasks tab, in full. It
// follows the popup's board, which reloads while it is open.
type taskView struct {
	pv *projectView
	id int
}

func (tv *taskView) task() *tasks.Task {
	if tv.pv.board != nil {
		for _, t := range tv.pv.board.Tasks {
			if t.ID == tv.id {
				return t
			}
		}
	}
	return nil
}

func (tv *taskView) key(m *dash, k tea.KeyPressMsg) tea.Cmd {
	switch k.String() {
	case "esc":
		m.pop()
	case "D", "A", "x", "c":
		if t := tv.task(); t != nil {
			return m.taskKey(tv.pv.slug, t, k.String())
		}
	}
	return nil
}

func (tv *taskView) render(m *dash) string {
	title := "Task · " + tv.pv.slug
	t := tv.task()
	if t == nil {
		body := styleFaint.Render("loading…")
		if tv.pv.board != nil {
			body = styleFaint.Render("this task is gone")
		}
		return m.popup(box{title: title, body: []string{body}, sel: -1, keys: "esc back"})
	}
	d := &panel{w: m.inner(viewWidth) + 2} // panel lines start with a space, and keep one at the end
	var rv *Review
	if r, ok := tv.pv.reviews[t.ID]; ok {
		rv = &r
	}
	taskPanelWith(d, t, rv, m.asked(tv.pv.slug, t), m.taskUsage(tv.pv.slug, t.ID))
	for i, l := range d.lines {
		// The box has its own gutters.
		d.lines[i] = strings.TrimRight(strings.TrimSuffix(strings.TrimPrefix(l, " "), reset), " ") + reset
	}
	return m.popup(box{title: title, body: d.lines, sel: -1, keys: joinKeys(taskKeys(t), "esc back")})
}

// count is how many things the tab selects among.
func (pv *projectView) count(m *dash) int {
	switch pv.tab {
	case tabOverview:
		return len(pv.data(m).Repos)
	case tabInbox:
		return len(project.Rows(pv.data(m).Items))
	case tabTasks:
		return len(pv.tasks())
	case tabLibrary:
		return len(pv.library)
	}
	return 0
}

// repoKey adds (+) or removes (x) a repository.
func (pv *projectView) repoKey(m *dash, k tea.KeyPressMsg) tea.Cmd {
	slug, repos := pv.slug, pv.data(m).Repos
	switch k.String() {
	case "+":
		m.prompt("Add a repository", "Its directory:", m.cwd, func(path string) tea.Cmd {
			path = absPath(path, m.cwd)
			return m.setRepo(slug, path, true, "added "+path)
		})
	case "x":
		i := pv.sel[tabOverview]
		if i >= len(repos) {
			return nil
		}
		path := repos[i]
		m.confirm("Remove a repository", "Remove "+path+" from "+slug+"'s repositories? The directory itself stays.", func() tea.Cmd {
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

// tasks are the live tasks the Tasks tab lists.
func (pv *projectView) tasks() []*tasks.Task {
	if pv.board == nil {
		return nil
	}
	return shown(pv.board, pv.doneAll)
}

// shown are the tasks a task list shows, in board order: needs you, in
// motion, on deck, the backlog, then the done ones (newest first) after
// b. The project popup's Tasks tab lists these.
func shown(b *tasks.Board, doneAll bool) []*tasks.Task { return listed(b, doneAll) }

// doneCount is how many done tasks a board has.
func doneCount(b *tasks.Board) int {
	n := 0
	if b != nil {
		for _, t := range b.Tasks {
			if tasks.GroupOf(t.Status) == tasks.DoneG {
				n++
			}
		}
	}
	return n
}

// doneNote is the collapsed DONE group's one line, with its rule.
func doneNote(n, w int) []string {
	return []string{
		sectionRule("DONE", sectionStyle("DONE"), w),
		styleFaint.Render(fmt.Sprintf("… %d done (b shows them)", n)),
	}
}

// listed are a board's tasks as the task lists show them, by group:
// done ones last, newest first (by updated date, then id), so x can
// still send one back. The done ones are left out unless done.
func listed(b *tasks.Board, done bool) []*tasks.Task {
	var out []*tasks.Task
	for _, g := range tasks.Groups {
		if g == tasks.DoneG && !done {
			continue
		}
		start := len(out)
		for _, t := range b.Tasks {
			if tasks.GroupOf(t.Status) == g {
				out = append(out, t)
			}
		}
		if g == tasks.DoneG {
			slices.SortStableFunc(out[start:], func(a, b *tasks.Task) int {
				return cmp.Or(-strings.Compare(a.Updated, b.Updated), b.ID-a.ID)
			})
		}
	}
	return out
}

func (pv *projectView) render(m *dash) string { return m.popup(pv.box(m)) }

func (pv *projectView) box(m *dash) box {
	w := m.inner(viewWidth)
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
		body = append(body, "", styleFaint.Render("The coordinator handles these (tm inbox done)."))
	case tabTasks:
		body, sel, hits = pv.taskLines(m, w)
		keys = taskListKeys(pv.selTask(), pv.board, pv.doneAll, "esc close")
		if pv.selTask() != nil {
			keys = joinKeys("enter show", keys)
		}
	case tabSettings:
		body, sel, hits = pv.settings.lines(m, w)
		keys = "enter change · + - number · ↑ ↓ move · " + keys
		if l := pv.settings; l.sel < len(l.rows) && l.rows[l.sel].from != nil && l.rows[l.sel].from(m) == "" {
			// The project sets it itself: x follows all projects again.
			keys = "enter change · + - number · x follow all projects · ↑ ↓ move · esc close"
		}
	case tabKeys:
		// The same list as the help.
		body = keyLines(w)
		keys = "↑ ↓ scroll · " + keys
	case tabMemory:
		body = pv.memoryLines(m, w)
		keys = "↑ ↓ scroll · " + keys
	case tabLibrary:
		body, sel, hits = pv.libraryLines(w)
		if len(pv.library) > 0 {
			keys = "enter read · d delete · D delete the thread's · " + keys
		}
	}
	head := []string{pv.tabBar(p), ""}
	all := append([]int{tabHit, noHit}, hits...)
	for len(all) < len(head)+len(body) {
		all = append(all, noHit)
	}
	b := box{title: pv.slug, head: head, body: body, sel: -1, hits: all, keys: keys}
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
func (pv *projectView) click(m *dash, item, col int, double bool) tea.Cmd {
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
	if double && pv.tab == tabTasks {
		if t := pv.selTask(); t != nil {
			return m.openTask(pv, t)
		}
	}
	if double && pv.tab == tabLibrary {
		if f, ok := pv.selLib(); ok {
			return m.openLibFile(pv, f)
		}
	}
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
	if pv.tab == tabKeys || pv.tab == tabMemory {
		pv.nudge[pv.tab] += 3 * d
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

// overview is the project's slug, goal, repositories, machine and
// agents.
func (pv *projectView) overview(m *dash, p ProjectData, w int) ([]string, int, []int) {
	field := func(label string) string { return styleFaint.Render(fmt.Sprintf("%-14s", label)) }
	var out []string
	repoAt := map[int]int{} // a repository's line: its index
	out = append(out, field("Project")+styleHead.Render(p.Slug))
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
		a := cmp.Or(t.Agent, config.DefaultAgent)
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
	needs := needsYouWords(c["needs_you"])
	if c["needs_you"] > 0 {
		needs += " (Tasks tab)"
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
			line := cmp.Or(s.Agent, config.DefaultAgent) + styleFaint.Render(" · "+s.ID+" "+stateWord(s))
			if s.RemoteControl {
				line += styleFaint.Render(" · remote control on")
			}
			return line
		}
	}
	s := config.Defaults
	if p.Safety != nil {
		s = *p.Safety
	}
	return s.CoordinatorAgent + styleFaint.Render(" · not running; enter on the project starts it")
}

// taskLines are the Tasks tab's lines.
func (pv *projectView) taskLines(m *dash, w int) ([]string, int, []int) {
	if pv.board == nil {
		return []string{styleFaint.Render("loading…")}, -1, nil
	}
	return taskList(m, pv.slug, pv.board, pv.tasks(), pv.sel[tabTasks], pv.doneAll, w)
}

// taskList is how the project popup's Tasks tab draws a project's tasks:
// grouped, only the selected task
// that isn't done listing its steps (the others show n/n), the collapsed
// backlog and the older done tasks left out each in a line. It returns the
// lines, the selected task's line and each line's task (noHit for none).
func taskList(m *dash, slug string, b *tasks.Board, list []*tasks.Task, selected int, doneAll bool, w int) ([]string, int, []int) {
	var out []string
	var hits []int
	add := func(hit int, l ...string) {
		for _, s := range l {
			out = append(out, s)
			hits = append(hits, hit)
		}
	}
	sel := -1
	var group tasks.Group
	// The collapsed done tasks are one line at the end.
	collapsed := !doneAll && doneCount(b) > 0
	for i, t := range list {
		if g := tasks.GroupOf(t.Status); g != group {
			group = g
			if len(out) > 0 {
				add(noHit, "")
			}
			title := strings.ToUpper(string(g))
			add(noHit, sectionRule(title, sectionStyle(title), w))
		}
		steps := t.Steps
		if i != selected || t.Status == tasks.Done {
			steps = nil
		}
		if i == selected {
			sel = len(out)
		}
		add(i, line(taskRow(t, m.asked(slug, t) != ""), w, i == selected))
		// The selected task's steps, under its title.
		for _, s := range steps {
			add(i, fit(strings.Repeat(" ", taskTitleCol)+todoGlyph(map[bool]string{true: "done"}[s.Done])+" "+oneLine(s.Text), w))
		}
	}
	if collapsed {
		if len(out) > 0 {
			add(noHit, "")
		}
		add(noHit, doneNote(doneCount(b), w)...)
	}
	if len(out) == 0 {
		add(noHit, styleFaint.Render("no tasks"))
	}
	return out, sel, hits
}

// taskListKeys are a task list's keys: the selected task's, then b for
// the done tasks, then close.
func taskListKeys(t *tasks.Task, b *tasks.Board, doneAll bool, close string) string {
	done := ""
	switch {
	case doneAll:
		done = "b hide done"
	case doneCount(b) > 0:
		done = "b done"
	}
	return joinKeys(taskKeys(t), done, close)
}

// kindStyle is the style of an inbox item's kind: red for what went
// wrong, green for what finished, yellow for the rest.
// kindWords are the inbox kinds in words, where the kind's own name is a
// code (docs/STYLE.md T3).
var kindWords = map[string]string{
	"thread-resolved":  "resolved",
	"takeover":         "you typed",
	"server-restart":   "server restart",
	"pr-opened":        "PR opened",
	"pr-checks-failed": "checks failed",
	"pr-review":        "PR review",
	"pr-merged":        "PR merged",
	"pr-closed":        "PR closed",
	"pr-conflict":      "PR conflict",
	"close-held":       "kept open",
	"gh-failing":       "PR host failing",
	"guard":            "guard refused",
}

// kindWord is an inbox kind (or an ask, "send-back") in words.
func kindWord(kind string) string {
	if w, ok := kindWords[kind]; ok {
		return w
	}
	return strings.ReplaceAll(kind, "-", " ")
}

func kindStyle(kind string) lipgloss.Style {
	switch kind {
	case "pr-checks-failed", "pr-conflict", "exited", "gh-failing", "blocked", "needs-you", "guard":
		return styleBad
	case "report", "pr-merged", "pr-opened", "task-done", "thread-resolved":
		return styleGood
	}
	return styleWarn
}

// inboxLines are a project's unhandled inbox items, one row for the
// items of one kind for one subject, sel selected, and the row on each
// line. A row reads kind, age, ×N when it stands for more, task, what
// happened, then the task's title, which is what a narrow row cuts.
func inboxLines(items []project.Item, sel, w int) ([]string, int, []int) {
	var lines []string
	var hits []int
	at := -1
	now := time.Now()
	rows := project.Rows(items)
	for i, r := range rows {
		// Columns two cells apart (docs/STYLE.md S2): kind, age, then
		// what happened.
		kind := fit(kindWord(r.Kind), 14)
		when := fmt.Sprintf("%-4s", age(now.Sub(r.Created)))
		rest := oneLine(strings.TrimSpace(strings.Join([]string{r.Task, r.What}, " ")))
		if r.Count > 1 {
			rest = fmt.Sprintf("×%d %s", r.Count, rest)
		}
		hits = append(hits, i)
		if i == sel {
			at = len(lines)
			if r.Title != "" {
				rest += " " + oneLine(r.Title)
			}
			lines = append(lines, styleSel.Render(fit(kind+"  "+when+"  "+rest, w)))
			continue
		}
		if r.Title != "" {
			rest += " " + styleFaint.Render(oneLine(r.Title))
		}
		lines = append(lines, fit(kindStyle(r.Kind).Render(kind)+"  "+styleFaint.Render(when)+"  "+rest, w)+reset)
	}
	if len(rows) == 0 {
		lines = append(lines, styleFaint.Render("inbox empty"))
		hits = append(hits, noHit)
	}
	return lines, at, hits
}

// memoryLines are the project's memory, read-only: CONTEXT.md, MEMORY.md
// and the memory notes' titles, as text (a link shows its text only, so
// no file path shows).
func (pv *projectView) memoryLines(m *dash, w int) []string {
	switch {
	case pv.memErr != nil:
		return []string{styleBad.Render("error: " + oneLine(pv.memErr.Error()))}
	case pv.memory == nil:
		return []string{styleFaint.Render("loading…")}
	}
	mem := pv.memory
	var out []string
	section := func(title, text, none string) {
		if len(out) > 0 {
			out = append(out, "")
		}
		out = append(out, sectionRule(title, sectionStyle(title), w))
		if lines := mdLines(text, w); len(lines) > 0 {
			out = append(out, lines...)
		} else {
			out = append(out, styleFaint.Render(none))
		}
	}
	section("CONTEXT", mem.Context, "no context yet")
	section("MEMORY", mem.Index, "no memory yet")
	notes := ""
	for _, n := range mem.Notes {
		notes += "- " + n + "\n"
	}
	section("NOTES", notes, "no notes yet")
	out = append(out, "", styleFaint.Render("Read-only: the coordinator keeps these; every thread is told them."))
	return out
}

// mdLink is a markdown link: its text and its target.
var mdLink = regexp.MustCompile(`\[([^\]]*)\]\([^)]*\)`)

// mdLines is a markdown file as text w cells wide: a heading in bold
// (its first one, the file's title, left out), a link as its text, a
// list item wrapped under its text.
func mdLines(text string, w int) []string {
	var out []string
	first := true
	for _, l := range strings.Split(strings.TrimSpace(text), "\n") {
		l = mdLink.ReplaceAllString(strings.TrimRight(l, " \t"), "$1")
		if h, ok := strings.CutPrefix(l, "#"); ok {
			h = strings.TrimSpace(strings.TrimLeft(h, "#"))
			if !first || !strings.HasPrefix(l, "# ") {
				out = append(out, styleHead.Render(fit(h, w)))
			}
			first = false
			continue
		}
		first = false
		if strings.TrimSpace(l) == "" {
			if len(out) > 0 && out[len(out)-1] != "" {
				out = append(out, "")
			}
			continue
		}
		// A list item's lines hang under its text.
		body := strings.TrimLeft(l, " ")
		indent := len(l) - len(body)
		for _, b := range []string{"- ", "* ", "+ "} {
			if strings.HasPrefix(body, b) {
				indent += len(b)
				break
			}
		}
		lead := l[:indent]
		for i, wl := range wrapLines(l[indent:], w-indent) {
			if i > 0 {
				lead = strings.Repeat(" ", indent)
			}
			out = append(out, lead+wl)
		}
	}
	for len(out) > 0 && out[len(out)-1] == "" {
		out = out[:len(out)-1]
	}
	return out
}
