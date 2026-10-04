package tui

import (
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// Popups: an overlay draws as a bordered box over the dashboard, which
// stays in view, dimmed. The header and footer stay as they are: the
// footer lists the popup's keys and keeps showing messages.

// box is a popup's content.
type box struct {
	title string
	body  []string // styled lines
	sel   int      // the line to keep in view, -1 for none
	// scroll is the first line shown when sel is -1 (help, the keys).
	scroll int
	keys   string // the footer's key list
	width  int    // the box's width when the window has room
}

// popup draws b over the list.
func (m *dash) popup(b box) string {
	room := m.bodyRows()
	base := m.listBody()
	// A small window: the box takes the whole body.
	bw := min(b.width, m.w-4)
	if m.w < 44 {
		bw = m.w
	}
	bw = max(bw, 8)
	inner := bw - 4 // border and a space each side
	rows := min(len(b.body), max(room-4, 1))
	if room < 8 {
		rows = max(room-2, 1)
	}
	rows = max(rows, 1)
	top := scrollTop(b.sel, rows, len(b.body))
	if b.sel < 0 {
		top = min(max(b.scroll, 0), max(len(b.body)-rows, 0))
	}

	border := styleAccent
	lines := make([]string, 0, rows+2)
	title := ""
	if b.title != "" {
		title = " " + styleHead.Render(oneLine(b.title)) + " "
	}
	fill := max(bw-2-1-ansi.StringWidth(title), 0)
	lines = append(lines, border.Render("╭─")+title+border.Render(strings.Repeat("─", fill)+"╮"))
	for i := range rows {
		l := ""
		if top+i < len(b.body) {
			l = b.body[top+i]
		}
		lines = append(lines, border.Render("│")+" "+fit(l, inner)+reset+" "+border.Render("│"))
	}
	more := ""
	if top+rows < len(b.body) {
		more = " more ↓ "
	}
	lines = append(lines, border.Render("╰"+strings.Repeat("─", max(bw-2-len([]rune(more)), 0))+more+"╯"))

	x := max((m.w-bw)/2, 0)
	y := max((room-len(lines))/2, 0)
	body := make([]string, room)
	for i := range body {
		plain := ansi.Strip(base[i])
		if i < y || i >= y+len(lines) {
			body[i] = styleFaint.Render(plain)
			continue
		}
		left := ansi.Cut(plain, 0, x)
		right := ansi.Cut(plain, x+bw, m.w)
		body[i] = styleFaint.Render(fit(left, x)) + lines[i-y] + reset + styleFaint.Render(right)
	}
	return m.frame("", body, -1, b.keys)
}
