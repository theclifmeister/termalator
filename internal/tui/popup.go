package tui

import (
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// Popups: an overlay draws as a bordered box over the dashboard, which
// stays in view, dimmed (or over a session's screen: Over). The header and footer stay as they are: the
// footer lists the popup's keys and keeps showing messages.

// box is a popup's content.
type box struct {
	title string
	// head are lines that stay at the top (a tab bar); the body scrolls
	// under them.
	head []string
	body []string // styled lines
	sel  int      // the body line to keep in view, -1 for none
	// scroll is the first line shown when sel is -1 (help, the keys).
	scroll int
	keys   string // the footer's key list
	width  int    // the box's width when the window has room
	// height is how many lines (head and body) the box shows when the
	// window has room, whatever the body's length; 0 fits the body.
	height int
	// hits are what a click on each head line, then each body line,
	// picks (the overlay's click), noHit for nothing; nil for a box
	// without clickable lines.
	hits []int
	// at places the box with its top left corner at a cell of the body
	// (a context menu), kept inside it; nil centres it.
	at *[2]int
}

// noHit is a line that picks nothing.
const noHit = -1

// boxGeo is where the topmost popup was drawn, for the mouse: its top
// left corner in the body (row 0 is the first row under the header), its
// size, its head lines, the first body line shown, how many show and
// what each line picks.
type boxGeo struct {
	x, y, w, h int
	head       int
	top, rows  int
	hits       []int
}

// line is the line under body row y (head lines first, then the box's
// body lines, as hits counts them), or -1.
func (g *boxGeo) line(y int) int {
	r := y - g.y - 1
	if r < 0 || r >= g.head+g.rows {
		return -1
	}
	if r < g.head {
		return r
	}
	return g.head + g.top + r - g.head
}

// inside says whether body cell (x, y) is in the box.
func (g *boxGeo) inside(x, y int) bool {
	return x >= g.x && x < g.x+g.w && y >= g.y && y < g.y+g.h
}

// sliver is the narrowest margin beside a popup that still shows what
// is under it.
const sliver = 4

// boxRows is how many lines (head and body) box b shows in this
// window: its height, else its lines, as many as the window has room
// for.
func (m *dash) boxRows(b box) int {
	room := m.bodyRows()
	n := len(b.head) + len(b.body)
	if b.height > 0 {
		n = b.height
	}
	rows := min(n, max(room-4, 1))
	if room < 8 {
		rows = max(room-2, 1)
	}
	return max(rows, 1)
}

// popup draws b over the list.
func (m *dash) popup(b box) string {
	room := m.bodyRows()
	base := m.base()
	// A small window: the box takes the whole body.
	bw := min(b.width, m.w-4)
	switch {
	case m.w < 44:
		bw = m.w
	case b.at == nil && bw >= m.w-4:
		// No room for a margin worth showing: the box takes the width
		// but a cleared column each side.
		bw = m.w - 2
	}
	bw = max(bw, 8)
	inner := bw - 4 // border and a space each side
	head := b.head
	rows := m.boxRows(b)
	if len(head) >= rows {
		head = nil
	}
	rows -= len(head)
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
	for _, l := range head {
		lines = append(lines, border.Render("│")+" "+fit(l, inner)+reset+" "+border.Render("│"))
	}
	for i := range rows {
		l := ""
		if top+i < len(b.body) {
			l = b.body[top+i]
		}
		lines = append(lines, border.Render("│")+" "+fit(l, inner)+reset+" "+border.Render("│"))
	}
	more := ""
	switch {
	case top+rows < len(b.body) && top > 0:
		more = " more ↑ ↓ "
	case top+rows < len(b.body):
		more = " more ↓ "
	case top > 0:
		more = " more ↑ "
	}
	lines = append(lines, border.Render("╰"+strings.Repeat("─", max(bw-2-len([]rune(more)), 0))+more+"╯"))

	x := max((m.w-bw)/2, 0)
	y := max((room-len(lines))/2, 0)
	if b.at != nil {
		x = max(min(b.at[0], m.w-bw), 0)
		y = max(min(b.at[1], room-len(lines)), 0)
	}
	hits := b.hits
	if len(head) < len(b.head) && len(hits) >= len(b.head) {
		hits = hits[len(b.head):] // no room for the head
	}
	m.geo = &boxGeo{x: x, y: y, w: bw, h: len(lines), head: len(head), top: top, rows: rows, hits: hits}
	body := make([]string, room)
	for i := range body {
		plain := ansi.Strip(base[i])
		if i < y || i >= y+len(lines) {
			body[i] = styleFaint.Render(plain)
			continue
		}
		// Beside the box, a margin too narrow to read is cleared, so no
		// cut-off letters of what is under it show.
		left := fit(ansi.Cut(plain, 0, x), x)
		right := fit(ansi.Cut(plain, x+bw, m.w), max(m.w-x-bw, 0))
		if x < sliver {
			left = strings.Repeat(" ", x)
		}
		if m.w-x-bw < sliver {
			right = strings.Repeat(" ", max(m.w-x-bw, 0))
		}
		body[i] = styleFaint.Render(left) + lines[i-y] + reset + styleFaint.Render(right)
	}
	return m.frame("", body, -1, b.keys)
}
