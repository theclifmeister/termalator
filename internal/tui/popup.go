package tui

import (
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// Popups: an overlay draws as a bordered box over the dashboard, which
// stays in view, dimmed with the sidebar (or over a session's screen:
// Over). The box carries its title in its top border and ends with its
// own keys, an action row inside the frame (docs/STYLE.md, the dialog
// anatomy); the footer under it shows none of them. A popup opened from
// another draws over it, the one under it dimmed.

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
	keys   string // the action row: the popup's keys, as key hints
	// width is a context menu's width (box.at); a popup is a view or,
	// with dialog set, a dialog, each of a fixed size (docs/STYLE.md S7).
	width  int
	dialog bool
	// hits are what a click on each head line, then each body line,
	// picks (the overlay's click), noHit for nothing; nil for a box
	// without clickable lines.
	hits []int
	// at places the box with its top left corner at a cell of the body
	// (a context menu), kept inside it, its size that of its lines; nil
	// centres it on the whole window.
	at *[2]int
}

// The popups' sizes when the window has room (docs/STYLE.md S7): a
// dialog asks or takes one thing, a view lists or shows many. A view is
// four fifths of the window wide and high, with no cap.
const (
	dialogWidth  = 64
	dialogHeight = 12
	// viewWidth is what a view asks inner and boxWidth for: any width
	// past a dialog's; the window decides.
	viewWidth = 1 << 20
)

// noHit is a line that picks nothing.
const noHit = -1

// boxGeo is where the topmost popup was drawn, for the mouse: its top
// left corner in the window (sidebar and header included), its
// size, its head lines, the first body line shown, how many show and
// what each line picks, and its action rows.
type boxGeo struct {
	x, y, w, h int
	head       int
	top, rows  int
	hits       []int
	// acts are the action rows' key lists, the first at row actY of the
	// box.
	acts []string
	actY int
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

// action is the key of the action row's button at body cell (x, y), ""
// for none.
func (g *boxGeo) action(x, y int) string {
	i := y - g.y - g.actY
	if i < 0 || i >= len(g.acts) {
		return ""
	}
	return hintAt(hints(g.acts[i], g.x+2), x)
}

// inside says whether body cell (x, y) is in the box.
func (g *boxGeo) inside(x, y int) bool {
	return x >= g.x && x < g.x+g.w && y >= g.y && y < g.y+g.h
}

// sliver is the narrowest margin beside a popup that still shows what
// is under it.
const sliver = 4

// boxSize is the size of the popup b in this window: a view or a dialog
// of fixed size, shrunk to fit with a cell of margin on a small window;
// a context menu takes the width it asks for and the height of its
// lines, kept inside the body.
func (m *dash) boxSize(b box) (int, int) {
	if b.at != nil {
		bw := max(min(b.width, m.w), 8)
		n := len(b.head) + len(b.body)
		acts := len(actionRows(b.keys, bw-4))
		if acts > 0 {
			acts++ // the row above them
		}
		return bw, max(min(n+2+acts, m.bodyRows()), 3)
	}
	w, h := m.winW, m.h
	if b.dialog {
		return max(min(dialogWidth, w-2), 8), max(min(dialogHeight, h-2), 5)
	}
	return max(min(w*8/10, w-2), 8), max(min(h*8/10, h-2), 5)
}

// boxWidth is the width of a popup asking for width: a dialog's if it is
// no wider, else a view's.
func (m *dash) boxWidth(width int) int {
	w, _ := m.boxSize(box{dialog: width <= dialogWidth})
	return w
}

// inner is the text width inside a popup asking for width: the box less
// its borders and a gutter each side.
func (m *dash) inner(width int) int { return max(m.boxWidth(width)-4, 4) }

// actionRows are a key list laid out in rows of at most w cells, broken
// between hints.
func actionRows(keys string, w int) []string {
	if keys == "" {
		return nil
	}
	var rows []string
	cur := ""
	for _, h := range strings.Split(keys, " · ") {
		switch {
		case cur == "":
			cur = h
		case ansi.StringWidth(cur+" · "+h) <= w:
			cur += " · " + h
		default:
			rows = append(rows, cur)
			cur = h
		}
	}
	return append(rows, cur)
}

// boxRows is how many lines (head and body) box b shows: the box's
// height less its borders and action rows.
func (m *dash) boxRows(b box) int {
	bw, bh := m.boxSize(b)
	acts := len(actionRows(b.keys, bw-4))
	if acts > 0 {
		acts++ // the row above them
	}
	return max(bh-2-acts, 1)
}

// drawBox draws a bordered box bw cells wide: the title in its top
// border, the lines, a blank row and the action rows, and more (a
// scroll mark) in its bottom border. A line is fitted to the inner width.
func drawBox(title string, bw int, lines, acts []string, more string) []string {
	border := styleAccent
	inner := max(bw-4, 1)
	if title != "" {
		title = " " + styleTitle.Render(oneLine(title)) + " "
	}
	fill := max(bw-3-ansi.StringWidth(title), 0)
	out := []string{border.Render("╭─") + title + border.Render(strings.Repeat("─", fill)+"╮")}
	row := func(l string) string {
		return border.Render("│") + " " + fit(l, inner) + reset + " " + border.Render("│")
	}
	for _, l := range lines {
		out = append(out, row(l))
	}
	if len(acts) > 0 {
		out = append(out, row(""))
		for _, a := range acts {
			out = append(out, row(keysLine(a)))
		}
	}
	if more != "" {
		more = " " + more + " "
	}
	out = append(out, border.Render("╰"+strings.Repeat("─", max(bw-3-ansi.StringWidth(more), 0)))+styleFaint.Render(more)+border.Render("─╯"))
	return out
}

// popup draws b over what is under it: the list, a session's screen or
// the popup it was opened from. It returns the whole window, the box
// centred on it (or at b.at in the body).
func (m *dash) popup(b box) string {
	base := m.baseWindow()
	bw, _ := m.boxSize(b)
	inner := bw - 4
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
	var shown []string
	shown = append(shown, head...)
	for i := range rows {
		l := ""
		if top+i < len(b.body) {
			l = b.body[top+i]
		}
		shown = append(shown, l)
	}
	more := ""
	switch {
	case top+rows < len(b.body) && top > 0:
		more = "more ↑ ↓"
	case top+rows < len(b.body):
		more = "more ↓"
	case top > 0:
		more = "more ↑"
	}
	acts := actionRows(b.keys, inner)
	lines := drawBox(b.title, bw, shown, acts, more)

	x := max((m.winW-bw)/2, 0)
	y := max((m.h-len(lines))/2, 0)
	if b.at != nil {
		x = max(min(b.at[0]+m.sideW(), m.winW-bw), 0)
		y = max(min(b.at[1]+1, m.h-footRows-len(lines)), 1)
	}
	hits := b.hits
	if len(head) < len(b.head) && len(hits) >= len(b.head) {
		hits = hits[len(b.head):] // no room for the head
	}
	m.geo = &boxGeo{x: x, y: y, w: bw, h: len(lines), head: len(head), top: top, rows: rows, hits: hits,
		acts: acts, actY: 1 + len(shown) + 1}
	out := make([]string, m.h)
	for i := range out {
		line := ""
		if i < len(base) {
			line = base[i]
		}
		if i < y || i >= y+len(lines) {
			out[i] = dimRow(line, m.sideW(), i >= 1 && i < m.h-footRows)
			continue
		}
		// Beside the box, a margin too narrow to read is cleared, so no
		// cut-off letters of what is under it show.
		plain := ansi.Strip(line)
		left := fit(ansi.Cut(plain, 0, x), x)
		right := fit(ansi.Cut(plain, x+bw, m.winW), max(m.winW-x-bw, 0))
		if x < sliver {
			left = strings.Repeat(" ", x)
		}
		if m.winW-x-bw < sliver {
			right = strings.Repeat(" ", max(m.winW-x-bw, 0))
		}
		out[i] = styleFaint.Render(left) + lines[i-y] + reset + styleFaint.Render(right)
	}
	return strings.Join(out, "\n")
}

// dimRow dims a window row under a popup: all of it in the body, else
// its sidebar part (the header and the footer stay as they are).
func dimRow(line string, side int, body bool) string {
	if body {
		return styleFaint.Render(ansi.Strip(line))
	}
	return styleFaint.Render(ansi.Strip(ansi.Cut(line, 0, side))) + ansi.Cut(line, side, ansi.StringWidth(line)+1)
}
