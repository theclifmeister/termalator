package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/theclifmeister/terminatr/internal/project"
	"github.com/theclifmeister/terminatr/internal/proto"
	"github.com/theclifmeister/terminatr/internal/tasks"
)

// Overlays: the views opened on top of the list (help, a prompt, the
// task board, the project switcher, the inbox, the settings, the project
// popup). Each keeps
// its own state and draws as a popup (popup.go); keys go to the topmost,
// and closing it returns to the one below, or to the list.
type overlay interface {
	key(m *dash, k tea.KeyPressMsg) tea.Cmd
	render(m *dash) string
}

func (m *dash) push(o overlay) { m.stack = append(m.stack, o) }

// pop closes the topmost overlay.
func (m *dash) pop() {
	if len(m.stack) > 0 {
		m.stack = m.stack[:len(m.stack)-1]
	}
}

// close removes o wherever it is in the stack.
func (m *dash) close(o overlay) {
	for i := len(m.stack) - 1; i >= 0; i-- {
		if m.stack[i] == o {
			m.stack = append(m.stack[:i], m.stack[i+1:]...)
			return
		}
	}
}

func (m *dash) top() overlay {
	if len(m.stack) == 0 {
		return nil
	}
	return m.stack[len(m.stack)-1]
}

// boardView is the open task board, if any.
func (m *dash) boardView() *boardView {
	for i := len(m.stack) - 1; i >= 0; i-- {
		if b, ok := m.stack[i].(*boardView); ok {
			return b
		}
	}
	return nil
}

// line draws r w cells wide: plain in reverse video when selected,
// styled otherwise.
func line(r row, w int, selected bool) string {
	if selected {
		return styleSel.Render(fit(r.text(w), w))
	}
	return r.styled(w)
}

// moveSel moves a list selection by d within n items.
func moveSel(sel, d, n int) int { return min(max(sel+d, 0), max(n-1, 0)) }

// helpView lists the keys (keymap.go) as wide as the window; the arrows
// scroll, esc closes it.
type helpView struct{ scroll int }

func (h *helpView) key(m *dash, k tea.KeyPressMsg) tea.Cmd {
	if d, ok := scrollKeys[k.String()]; ok {
		h.scroll = clampScroll(h.scroll+d, len(helpLines(m.prefix, m.inner(viewWidth))))
		return nil
	}
	if k.String() == "esc" {
		m.pop()
	}
	return nil
}

func (h *helpView) box(m *dash) box {
	return box{title: "Help", body: helpLines(m.prefix, m.inner(viewWidth)), sel: -1, scroll: h.scroll,
		keys: "↑ ↓ scroll · esc close"}
}

func (h *helpView) render(m *dash) string { return m.popup(h.box(m)) }

func (h *helpView) wheel(m *dash, d int) { h.key(m, arrow(d)) }

// scrollKeys scroll a long popup, or move a list's selection: by a line,
// or by a page.
var scrollKeys = map[string]int{"up": -1, "k": -1, "down": 1, "j": 1, "pgup": -10, "pgdown": 10}

func clampScroll(s, n int) int { return min(max(s, 0), max(n-1, 0)) }

// inputView reads a line of text: its title names what for, its label
// asks for it.
type inputView struct {
	title       string
	label, text string
	submit      func(string) tea.Cmd
	// cancel is the footer message on esc or an empty line; max is the
	// most runes it takes, 0 for no limit.
	cancel string
	max    int
}

func (m *dash) prompt(title, label, initial string, submit func(string) tea.Cmd) {
	m.push(&inputView{title: title, label: label, text: initial, submit: submit})
}

// promptNo is prompt with an empty start, saying cancel in the footer
// when it is cancelled, and taking at most max runes.
func (m *dash) promptNo(title, label, cancel string, max int, submit func(string) tea.Cmd) {
	m.push(&inputView{title: title, label: label, submit: submit, cancel: cancel, max: max})
}

func (in *inputView) key(m *dash, k tea.KeyPressMsg) tea.Cmd {
	switch k.String() {
	case "esc":
		m.pop()
		in.cancelled(m)
	case "enter":
		m.pop()
		if t := strings.TrimSpace(in.text); t != "" {
			return in.submit(t)
		}
		in.cancelled(m)
	case "backspace":
		if r := []rune(in.text); len(r) > 0 {
			in.text = string(r[:len(r)-1])
		}
	case "ctrl+u":
		in.text = ""
	default:
		if k.Text != "" {
			in.text += oneLine(k.Text)
			if r := []rune(in.text); in.max > 0 && len(r) > in.max {
				in.text = string(r[:in.max])
			}
		}
	}
	return nil
}

func (in *inputView) cancelled(m *dash) {
	if in.cancel != "" {
		m.msg = in.cancel
	}
}

func (in *inputView) render(m *dash) string {
	// The label above the field; a long text wraps onto more lines, so
	// all of it shows.
	w := m.inner(dialogWidth)
	body := wrapLines(in.label, w)
	body = append(body, wrapInput("", in.text+"█", w)...)
	if n := len([]rune(in.text)); in.max > 0 && n*10 >= in.max*8 {
		// Near the limit: how much is left.
		body = append(body, styleFaint.Render(fmt.Sprintf("%d of at most %d characters", n, in.max)))
	}
	return m.popup(box{title: in.title, body: body, sel: -1, keys: "enter ok · ctrl+u clear · esc cancel", dialog: true})
}

// wrapInput lays out a prompt's label and text in lines of w cells,
// breaking anywhere (a path has no spaces to break at); the label is in
// the accent colour.
func wrapInput(label, text string, w int) []string {
	r := []rune(label + text)
	var lines []string
	for len(r) > 0 {
		n := min(len(r), max(w, 1))
		lines = append(lines, string(r[:n]))
		r = r[n:]
	}
	if l := len([]rune(label)); l <= len([]rune(lines[0])) {
		first := []rune(lines[0])
		lines[0] = styleAccent.Render(label) + string(first[l:])
	}
	return lines
}

// boardView is a project's task board: its live tasks in board order
// (needs you, in motion, on deck), or one of them when open. The
// coordinator changes tasks (tm task); d, a and x ask it to (asks.go).
type boardView struct {
	slug    string
	board   *tasks.Board
	reviews map[int]Review // the tasks in review, by id
	list    []*tasks.Task
	sel     int
	open    bool // showing the selected task
	// backlog lists the open tasks, collapsed until b (T108).
	backlog bool
	// back: opened on one task from the project popup's Tasks tab, esc
	// goes back there.
	back bool
}

// openBoard opens slug's board, showing task id when it isn't 0.
func (m *dash) openBoard(slug string, id int) tea.Cmd {
	b := &boardView{slug: slug, open: id != 0}
	b.selectID(id)
	m.push(b)
	return m.loadBoard(slug)
}

func (b *boardView) setBoard(t *tasks.Board) {
	var keep int
	if b.sel < len(b.list) {
		keep = b.list[b.sel].ID
	}
	b.board, b.list = t, listed(t, b.backlog)
	b.selectID(keep)
}

func (b *boardView) selectID(id int) {
	for i, t := range b.list {
		if t.ID == id {
			b.sel = i
			return
		}
	}
	if b.board != nil && !b.backlog && id != 0 {
		// A backlog task is in no list while the backlog is collapsed:
		// show the backlog rather than another task.
		for _, t := range b.board.Tasks {
			if t.ID == id && tasks.GroupOf(t.Status) == tasks.Backlog {
				b.backlog = true
				b.list = listed(b.board, true)
				b.selectID(id)
				return
			}
		}
	}
	b.sel = min(b.sel, max(len(b.list)-1, 0))
	if b.board == nil && id != 0 {
		// The board isn't loaded yet: select once it is.
		b.list = []*tasks.Task{{ID: id}}
	}
}

func (b *boardView) key(m *dash, k tea.KeyPressMsg) tea.Cmd {
	switch k.String() {
	case "esc":
		if b.open && !b.back {
			b.open = false
		} else {
			m.pop()
		}
	case "up", "k", "down", "j", "pgup", "pgdown":
		if !b.back {
			b.sel = moveSel(b.sel, scrollKeys[k.String()], len(b.list))
		}
	case "enter":
		if len(b.list) > 0 {
			b.open = true
		}
	case "b":
		if !b.open && b.board != nil && (b.backlog || backlogCount(b.board) > 0) {
			b.backlog = !b.backlog
			b.setBoard(b.board)
		}
	case "D", "A", "x", "c":
		if b.board != nil && b.sel < len(b.list) {
			return m.taskKey(b.slug, b.list[b.sel], k.String())
		}
	}
	return nil
}

func (b *boardView) render(m *dash) string {
	title := "Tasks · " + b.slug
	if b.board == nil {
		return m.popup(box{title: title, body: []string{styleFaint.Render("loading…")}, sel: -1, keys: "esc close"})
	}
	if b.open && b.sel < len(b.list) {
		t := b.list[b.sel]
		d := &panel{w: m.inner(viewWidth) + 2} // panel lines start with a space, and keep one at the end
		var rv *Review
		if r, ok := b.reviews[t.ID]; ok {
			rv = &r
		}
		taskPanelWith(d, t, rv, m.asked(b.slug, t), m.taskUsage(b.slug, t.ID))
		for i, l := range d.lines {
			// The box has its own gutters.
			d.lines[i] = strings.TrimRight(strings.TrimSuffix(strings.TrimPrefix(l, " "), reset), " ") + reset
		}
		keys := joinKeys(taskKeys(t), "esc back")
		return m.popup(box{title: "Task · " + b.slug, body: d.lines, sel: -1, keys: keys})
	}
	w := m.inner(viewWidth)
	var lines []string
	var hits []int
	sel := -1
	var group tasks.Group
	collapsed := !b.backlog && backlogCount(b.board) > 0
	// note puts the collapsed backlog where its group would be.
	note := func() {
		if collapsed {
			collapsed = false
			if len(lines) > 0 {
				lines = append(lines, "")
				hits = append(hits, noHit)
			}
			for _, l := range backlogNote(backlogCount(b.board), w) {
				lines = append(lines, l)
				hits = append(hits, noHit)
			}
		}
	}
	for i, t := range b.list {
		if g := tasks.GroupOf(t.Status); g != group {
			if g == tasks.DoneG {
				note()
			}
			if group != "" {
				lines = append(lines, "")
				hits = append(hits, noHit)
			}
			group = g
			title := strings.ToUpper(string(g))
			lines = append(lines, sectionRule(title, sectionStyle(title), w))
			hits = append(hits, noHit)
		}
		hits = append(hits, i)
		if i == b.sel {
			sel = len(lines)
		}
		lines = append(lines, line(taskRow(t, m.asked(b.slug, t) != ""), w, i == b.sel))
	}
	note()
	if len(lines) == 0 {
		lines = append(lines, styleFaint.Render("no tasks"))
	}
	keys := "esc close"
	if b.sel < len(b.list) {
		keys = joinKeys("enter show", taskKeys(b.list[b.sel]), keys)
	}
	switch {
	case b.backlog:
		keys = joinKeys("b hide backlog", keys)
	case backlogCount(b.board) > 0:
		keys = joinKeys("b backlog", keys)
	}
	return m.popup(box{title: title, body: lines, sel: sel, hits: hits, keys: keys})
}

// taskTitleCol is where a task row's title starts: after its id and
// the gap (taskRow).
const taskTitleCol = 5 + colGap

// taskRow is a task as every task list draws it (the t list, the
// project popup's Tasks tab): id, title, state, progress, thread.
func taskRow(t *tasks.Task, asked bool) row {
	r := row{who: t.Ref(), what: oneLine(t.Title), state: string(t.Status), rest: t.Thread, pct: -1, whoW: 5, whatMin: 10}
	if len(t.Steps) > 0 {
		r.pct = pctOf(t.StepsDone(), len(t.Steps))
		r.rest = joinSp(fmt.Sprintf("%d/%d", t.StepsDone(), len(t.Steps)), t.Thread)
	}
	if asked {
		r.lead = askedRow // first, so it is never cut
	}
	return r
}

// joinKeys joins key lists, skipping empty ones.
func joinKeys(keys ...string) string {
	var out []string
	for _, k := range keys {
		if k != "" {
			out = append(out, k)
		}
	}
	return strings.Join(out, " · ")
}

// click selects a task; a double-click shows it.
func (b *boardView) click(_ *dash, item, _ int, double bool) tea.Cmd {
	if b.back {
		return nil
	}
	if item < len(b.list) {
		b.sel, b.open = item, double
	}
	return nil
}

func (b *boardView) wheel(m *dash, d int) {
	if !b.open {
		b.key(m, arrow(d))
	}
}

// switchView is the project switcher: enter opens the selected
// project's coordinator.
type switchView struct{ sel int }

func (sw *switchView) key(m *dash, k tea.KeyPressMsg) tea.Cmd {
	switch k.String() {
	case "esc":
		m.pop()
	case "up", "k", "down", "j", "pgup", "pgdown":
		sw.sel = moveSel(sw.sel, scrollKeys[k.String()], len(m.data.Projects))
	case "enter":
		if sw.sel < len(m.data.Projects) && !m.busy {
			m.pop()
			return m.openProject(m.data.Projects[sw.sel].Slug)
		}
	}
	return nil
}

func (sw *switchView) render(m *dash) string {
	w := m.inner(viewWidth)
	var lines []string
	var hits []int
	for i, p := range m.data.Projects {
		hits = append(hits, i)
		r := row{key: "p:" + p.Slug, who: p.Slug, what: oneLine(p.Name), state: "—", rest: "no coordinator running", pct: -1}
		for _, s := range m.data.Sessions {
			if s.Role == proto.RoleCoordinator && s.Project == p.Slug {
				r.state, r.rest, r.pct = stateWord(s), progress(s, nil), sessionPct(s)
				break
			}
		}
		if p.Slug == m.current {
			r.rest = joinSp(r.rest, "· the current project")
		}
		lines = append(lines, line(r, w, i == sw.sel))
	}
	return m.popup(box{title: "Project switcher", body: lines, sel: sw.sel, hits: hits, keys: "enter open its coordinator · esc close"})
}

// click selects a project; a double-click opens its coordinator.
func (sw *switchView) click(m *dash, item, _ int, double bool) tea.Cmd {
	sw.sel = item
	if double {
		return sw.key(m, keyMsg("enter"))
	}
	return nil
}

func (sw *switchView) wheel(m *dash, d int) { sw.key(m, arrow(d)) }

// inboxView is a project's unhandled inbox items, read-only: the
// coordinator handles them.
type inboxView struct {
	slug string
	sel  int
}

func (in *inboxView) items(m *dash) []project.Item {
	for _, p := range m.data.Projects {
		if p.Slug == in.slug {
			return p.Items
		}
	}
	return nil
}

func (in *inboxView) key(m *dash, k tea.KeyPressMsg) tea.Cmd {
	switch k.String() {
	case "esc":
		m.pop()
	case "up", "k", "down", "j", "pgup", "pgdown":
		in.sel = moveSel(in.sel, scrollKeys[k.String()], len(project.Rows(in.items(m))))
	}
	return nil
}

func (in *inboxView) render(m *dash) string {
	lines, sel, hits := inboxLines(in.items(m), in.sel, m.inner(viewWidth))
	lines = append(lines, "", styleFaint.Render("The coordinator handles these (tm inbox done)."))
	return m.popup(box{title: "Inbox · " + in.slug, body: lines, sel: sel, hits: hits, keys: "esc close"})
}

func (in *inboxView) click(_ *dash, item, _ int, _ bool) tea.Cmd {
	in.sel = item
	return nil
}

func (in *inboxView) wheel(m *dash, d int) { in.key(m, arrow(d)) }
