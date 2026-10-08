package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/theclifmeister/terminatr/internal/tasks"
)

// Overlays: the views opened on top of the list (help, a prompt, the
// project switcher, the settings, the project popup, a task).
// Each keeps its own state and draws as a popup (popup.go); keys go to the topmost,
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

// taskTitleCol is where a task row's title starts: after its id and
// the gap (taskRow).
const taskTitleCol = 5 + colGap

// taskRow is a task as every task list draws it (the
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
