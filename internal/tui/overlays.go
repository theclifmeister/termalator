package tui

import (
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/theclifmeister/termalator/internal/project"
	"github.com/theclifmeister/termalator/internal/proto"
	"github.com/theclifmeister/termalator/internal/tasks"
)

// Overlays: the views opened on top of the list (help, a prompt, the
// task board, the project switcher, the inbox). Each keeps its own
// state; keys go to the topmost, and closing it returns to the one
// below, or to the list.
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
func (m *dash) line(r row, selected bool) string {
	if selected {
		return styleSel.Render(fit(r.text(m.w), m.w))
	}
	return r.styled(m.w)
}

// moveSel moves a list selection by d within n items.
func moveSel(sel, d, n int) int { return min(max(sel+d, 0), max(n-1, 0)) }

// helpView lists the keys; any key closes it.
type helpView struct{}

func (helpView) key(m *dash, _ tea.KeyPressMsg) tea.Cmd { m.pop(); return nil }

func (helpView) render(m *dash) string {
	return m.frame("help", helpLines(m.agentName), -1, "any key returns")
}

// inputView reads a line of text in the footer, over the list.
type inputView struct {
	label, text string
	submit      func(string) tea.Cmd
}

func (m *dash) prompt(label, initial string, submit func(string) tea.Cmd) {
	m.push(&inputView{label: label, text: initial, submit: submit})
}

func (in *inputView) key(m *dash, k tea.KeyPressMsg) tea.Cmd {
	switch k.String() {
	case "esc":
		m.pop()
	case "enter":
		m.pop()
		if t := strings.TrimSpace(in.text); t != "" {
			return in.submit(t)
		}
	case "backspace":
		if r := []rune(in.text); len(r) > 0 {
			in.text = string(r[:len(r)-1])
		}
	case "ctrl+u":
		in.text = ""
	default:
		if k.Text != "" {
			in.text += oneLine(k.Text)
		}
	}
	return nil
}

func (in *inputView) render(m *dash) string { return m.renderList() }

// boardView is a project's task board: its live tasks in board order
// (needs you, in motion, on deck), or one of them when open.
type boardView struct {
	slug  string
	board *tasks.Board
	list  []*tasks.Task
	sel   int
	open  bool // showing the selected task
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
	b.board, b.list = t, nil
	for _, g := range []tasks.Group{tasks.NeedsYou, tasks.InMotion, tasks.OnDeck} {
		for _, task := range t.Tasks {
			if tasks.GroupOf(task.Status) == g {
				b.list = append(b.list, task)
			}
		}
	}
	b.selectID(keep)
}

func (b *boardView) selectID(id int) {
	for i, t := range b.list {
		if t.ID == id {
			b.sel = i
			return
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
	case "esc", "q", "t":
		if b.open {
			b.open = false
		} else {
			m.pop()
		}
	case "up", "k":
		b.sel = moveSel(b.sel, -1, len(b.list))
	case "down", "j":
		b.sel = moveSel(b.sel, 1, len(b.list))
	case "enter":
		if len(b.list) > 0 {
			b.open = true
		}
	case "d":
		if b.board != nil && b.sel < len(b.list) && !m.busy {
			return m.markDone(b.slug, b.list[b.sel], false)
		}
	case "r":
		return m.loadBoard(b.slug)
	}
	return nil
}

func (b *boardView) render(m *dash) string {
	if b.board == nil {
		return m.frame(b.slug+" tasks", []string{" loading…"}, -1, "esc back")
	}
	if b.open && b.sel < len(b.list) {
		return b.renderTask(m, b.list[b.sel])
	}
	var lines []string
	sel := -1
	var group tasks.Group
	for i, t := range b.list {
		if g := tasks.GroupOf(t.Status); g != group {
			group = g
			st := styleTitle
			if g == tasks.NeedsYou {
				st = styleWarn.Bold(true)
			}
			lines = append(lines, m.ruleIn(strings.ToUpper(string(g)), st))
		}
		r := row{mark: markTop, who: t.Ref(), what: oneLine(t.Title), state: string(t.Status), rest: t.Thread, pct: -1}
		if len(t.Steps) > 0 {
			r.pct = pctOf(t.StepsDone(), len(t.Steps))
			r.rest = joinSp(fmt.Sprintf("%d/%d", t.StepsDone(), len(t.Steps)), t.Thread)
		}
		if i == b.sel {
			sel = len(lines)
		}
		lines = append(lines, m.line(r, i == b.sel))
	}
	done := 0
	for _, t := range b.board.Tasks {
		if t.Status == tasks.Done {
			done++
		}
	}
	if len(b.list) == 0 {
		lines = append(lines, styleFaint.Render(" no open tasks"))
	}
	lines = append(lines, styleFaint.Render(fmt.Sprintf("  done: %d", done)))
	return m.frame(b.slug+" tasks", lines, sel, "enter show · d mark done (review) · r refresh · esc back")
}

func (b *boardView) renderTask(m *dash, t *tasks.Task) string {
	g, st := stateLook(string(t.Status))
	status := st.Render(strings.TrimSpace(g + " " + string(t.Status)))
	if t.Thread != "" {
		status += styleFaint.Render(" · thread ") + t.Thread
	}
	lines := []string{
		fit(" "+styleHead.Render(t.Ref()+" "+oneLine(t.Title)), m.w) + reset,
		fit(" "+status, m.w) + reset,
		"",
	}
	for _, l := range strings.Split(strings.TrimSpace(t.Notes), "\n") {
		if l != "" {
			lines = append(lines, fit(" "+oneLine(l), m.w))
		}
	}
	if len(t.Steps) > 0 {
		lines = append(lines, "", styleFaint.Render(fmt.Sprintf(" steps %d/%d", t.StepsDone(), len(t.Steps))))
		for _, s := range t.Steps {
			box := todoGlyph(map[bool]string{true: "done"}[s.Done])
			lines = append(lines, fit(fmt.Sprintf("  %s %d %s", box, s.N, oneLine(s.Text)), m.w)+reset)
		}
	}
	return m.frame(b.slug+" "+t.Ref(), lines, -1, "d mark done (review) · esc back")
}

// switchView is the project switcher: enter opens the selected
// project's coordinator.
type switchView struct{ sel int }

func (sw *switchView) key(m *dash, k tea.KeyPressMsg) tea.Cmd {
	switch k.String() {
	case "esc", "q", "p":
		m.pop()
	case "up", "k":
		sw.sel = moveSel(sw.sel, -1, len(m.data.Projects))
	case "down", "j":
		sw.sel = moveSel(sw.sel, 1, len(m.data.Projects))
	case "enter":
		if sw.sel < len(m.data.Projects) && !m.busy {
			m.pop()
			return m.openProject(m.data.Projects[sw.sel].Slug)
		}
	}
	return nil
}

func (sw *switchView) render(m *dash) string {
	var lines []string
	for i, p := range m.data.Projects {
		r := row{key: "p:" + p.Slug, mark: "  ", who: p.Slug, what: oneLine(p.Name), state: "—", rest: "no coordinator", pct: -1}
		for _, s := range m.data.Sessions {
			if s.Role == proto.RoleCoordinator && s.Project == p.Slug {
				r.state, r.rest, r.pct = stateWord(s), progress(s), sessionPct(s)
				break
			}
		}
		if p.Slug == m.current {
			r.mark = "* "
		}
		lines = append(lines, m.line(r, i == sw.sel))
	}
	return m.frame("projects", lines, sw.sel, "enter open its coordinator · esc back")
}

// inboxView is a project's unhandled inbox items.
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
	case "esc", "q", "i":
		m.pop()
	case "up", "k":
		in.sel = moveSel(in.sel, -1, len(in.items(m)))
	case "down", "j":
		in.sel = moveSel(in.sel, 1, len(in.items(m)))
	case "r":
		return m.load()
	}
	return nil
}

func (in *inboxView) render(m *dash) string {
	items := in.items(m)
	var lines []string
	sel := -1
	now := time.Now()
	for i, it := range items {
		flag := " "
		if it.NeedsUser {
			flag = "!"
		}
		when := fmt.Sprintf("%-6s", age(now.Sub(it.Created)))
		text := fmt.Sprintf(" %s %s %s %s", flag, fit(it.Kind, 16), when, oneLine(it.Summary))
		if i == in.sel {
			sel = len(lines)
			text = styleSel.Render(fit(text, m.w))
		} else {
			text = fit(fmt.Sprintf(" %s %s %s %s", styleBad.Bold(true).Render(flag), styleWarn.Render(fit(it.Kind, 16)),
				styleFaint.Render(when), oneLine(it.Summary)), m.w) + reset
		}
		lines = append(lines, text)
	}
	if len(items) == 0 {
		lines = append(lines, styleFaint.Render(" inbox empty"))
	}
	lines = append(lines, "", styleFaint.Render(fit(" The coordinator handles these (tm inbox done); ! marks the ones for you.", m.w)))
	return m.frame(in.slug+" inbox", lines, sel, "r refresh · esc back")
}
