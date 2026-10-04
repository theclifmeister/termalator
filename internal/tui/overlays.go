package tui

import (
	"cmp"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/theclifmeister/termalator/internal/config"
	"github.com/theclifmeister/termalator/internal/project"
	"github.com/theclifmeister/termalator/internal/proto"
	"github.com/theclifmeister/termalator/internal/tasks"
)

// Overlays: the views opened on top of the list (help, a prompt, the
// task board, the project switcher, the inbox, the settings). Each keeps
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

// popupWidth is the width of the list-like popups.
const popupWidth = 104

// inner is the text width inside a popup of the given width.
func (m *dash) inner(width int) int {
	if m.w < 44 {
		return max(m.w-4, 4)
	}
	return max(min(width, m.w-4)-4, 4)
}

// helpView lists the keys; any key closes it.
type helpView struct{}

func (helpView) key(m *dash, _ tea.KeyPressMsg) tea.Cmd { m.pop(); return nil }

func (helpView) render(m *dash) string {
	return m.popup(box{title: "keys", body: helpLines(m.agentName, m.prefix), sel: -1, keys: "any key returns", width: 96})
}

// inputView reads a line of text.
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

func (in *inputView) render(m *dash) string {
	// A long text wraps onto more lines, so all of it shows.
	return m.popup(box{body: wrapInput(in.label, in.text+"█", m.inner(promptWidth)), sel: -1,
		keys: "enter ok · ctrl+u clear · esc cancel", width: promptWidth})
}

// promptWidth is the prompt popup's width when the window has room.
const promptWidth = 96

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
	title := b.slug + " tasks"
	if b.board == nil {
		return m.popup(box{title: title, body: []string{styleFaint.Render("loading…")}, sel: -1, keys: "esc back", width: popupWidth})
	}
	if b.open && b.sel < len(b.list) {
		t := b.list[b.sel]
		d := &panel{w: m.inner(88) + 1} // panel lines start with a space
		taskPanel(d, t, "")
		for i, l := range d.lines {
			d.lines[i] = strings.TrimPrefix(l, " ")
		}
		return m.popup(box{title: b.slug + " " + t.Ref(), body: d.lines, sel: -1, keys: "d mark done (review) · esc back", width: 88})
	}
	w := m.inner(popupWidth)
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
			lines = append(lines, m.ruleIn(strings.ToUpper(string(g)), st, w))
		}
		r := row{who: t.Ref(), what: oneLine(t.Title), state: string(t.Status), rest: t.Thread, pct: -1}
		if len(t.Steps) > 0 {
			r.pct = pctOf(t.StepsDone(), len(t.Steps))
			r.rest = joinSp(fmt.Sprintf("%d/%d", t.StepsDone(), len(t.Steps)), t.Thread)
		}
		if i == b.sel {
			sel = len(lines)
		}
		lines = append(lines, line(r, w, i == b.sel))
	}
	done := 0
	for _, t := range b.board.Tasks {
		if t.Status == tasks.Done {
			done++
		}
	}
	if len(b.list) == 0 {
		lines = append(lines, styleFaint.Render("no open tasks"))
	}
	lines = append(lines, styleFaint.Render(fmt.Sprintf("done: %d", done)))
	return m.popup(box{title: title, body: lines, sel: sel, keys: "enter show · d mark done (review) · r refresh · esc back", width: popupWidth})
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
	w := m.inner(popupWidth)
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
		lines = append(lines, line(r, w, i == sw.sel))
	}
	return m.popup(box{title: "projects", body: lines, sel: sw.sel, keys: "enter open its coordinator · esc back", width: popupWidth})
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
	w := m.inner(popupWidth)
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
		if i == in.sel {
			sel = len(lines)
			lines = append(lines, styleSel.Render(fit(fmt.Sprintf("%s %s %s %s", flag, fit(it.Kind, 16), when, oneLine(it.Summary)), w)))
			continue
		}
		lines = append(lines, fit(fmt.Sprintf("%s %s %s %s", styleBad.Bold(true).Render(flag), styleWarn.Render(fit(it.Kind, 16)),
			styleFaint.Render(when), oneLine(it.Summary)), w)+reset)
	}
	if len(items) == 0 {
		lines = append(lines, styleFaint.Render("inbox empty"))
	}
	lines = append(lines, "", styleFaint.Render("The coordinator handles these (tm inbox done); ! marks the ones for you."))
	return m.popup(box{title: in.slug + " inbox", body: lines, sel: sel, keys: "r refresh · esc back", width: popupWidth})
}

// settingsView shows the settings that apply here: config.toml's keys
// and the project's safety settings (§11.2), and the layout. tm never
// writes config.toml; e opens it in the user's editor.
type settingsView struct {
	slug  string
	lines []string
}

// editedMsg is the editor's exit.
type editedMsg struct{ err error }

func (m *dash) openSettings(slug string) {
	sv := &settingsView{slug: slug}
	sv.load(m)
	m.push(sv)
}

func (sv *settingsView) load(m *dash) {
	var l []string
	add := func(label, value, note string) {
		s := styleFaint.Render(fmt.Sprintf("%-22s", label)) + " " + value
		if note != "" {
			s += "  " + styleFaint.Render(note)
		}
		l = append(l, s)
	}
	def := func(isDefault bool) string {
		if isDefault {
			return "default"
		}
		return ""
	}
	path, _ := config.Path()
	where := path
	if _, err := os.Stat(path); err != nil {
		where += styleFaint.Render("  (not created yet)")
	}
	l = append(l, styleHead.Render("config.toml"), where, "", styleHead.Render("[keys]"))
	if _, err := prefixKey(); err != nil {
		l = append(l, styleBad.Render(oneLine(err.Error())))
	}
	add("prefix", m.prefix, def(m.prefix == DefaultPrefixKey))
	l = append(l, "")

	cfg, err := config.Load()
	switch {
	case err != nil:
		l = append(l, styleBad.Render(oneLine(err.Error())), "")
	case sv.slug == "":
		l = append(l, styleFaint.Render("Select a project to see its safety settings."), "")
	default:
		s, err := cfg.Safety(sv.slug)
		l = append(l, styleHead.Render("[projects."+sv.slug+"]"))
		if err != nil {
			l = append(l, styleBad.Render(oneLine(err.Error())))
		}
		d := config.Defaults
		add("start_threads", s.StartThreads, def(s.StartThreads == d.StartThreads))
		add("yolo", fmt.Sprint(s.Yolo), def(s.Yolo == d.Yolo))
		add("coordinator_approves", fmt.Sprint(s.CoordinatorApproves), def(s.CoordinatorApproves == d.CoordinatorApproves))
		add("auto_resolve", fmt.Sprint(s.AutoResolve), def(s.AutoResolve == d.AutoResolve))
		add("pr_followup", fmt.Sprint(s.PRFollowup), def(s.PRFollowup == d.PRFollowup))
		l = append(l, "")
	}

	l = append(l, styleHead.Render("layout")+styleFaint.Render("  ui.json, changed with < > | and the mouse"))
	add("details panel", map[bool]string{true: "on", false: "off"}[m.layout.Details], "")
	add("list width", fmt.Sprintf("%d%%", int(m.layout.Split*100+0.5)), "")
	l = append(l, "", styleFaint.Render("tm never writes config.toml; e opens it in $VISUAL or $EDITOR."))
	sv.lines = l
}

func (sv *settingsView) key(m *dash, k tea.KeyPressMsg) tea.Cmd {
	switch k.String() {
	case "esc", "q", ",":
		m.pop()
	case "e":
		path, err := config.Path()
		if err != nil {
			m.fail(err)
			return nil
		}
		args := strings.Fields(cmp.Or(os.Getenv("VISUAL"), os.Getenv("EDITOR"), "vi"))
		cmd := exec.Command(args[0], append(args[1:], path)...)
		return tea.ExecProcess(cmd, func(err error) tea.Msg { return editedMsg{err} })
	}
	return nil
}

func (sv *settingsView) render(m *dash) string {
	title := "settings"
	if sv.slug != "" {
		title += " · " + sv.slug
	}
	return m.popup(box{title: title, body: sv.lines, sel: -1, keys: "e edit config.toml · esc back", width: 88})
}
