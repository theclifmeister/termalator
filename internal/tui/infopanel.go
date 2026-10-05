package tui

import (
	"cmp"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"

	"github.com/theclifmeister/termilator/internal/emu"
	"github.com/theclifmeister/termilator/internal/project"
	"github.com/theclifmeister/termilator/internal/proto"
	"github.com/theclifmeister/termilator/internal/server"
	"github.com/theclifmeister/termilator/internal/tasks"
	"github.com/theclifmeister/termilator/internal/thread"
	"github.com/theclifmeister/termilator/internal/ticker"
	"github.com/theclifmeister/termilator/internal/view"
)

// The info panel (docs/SPEC.md §4): right of a thread's pane, what the
// thread is doing: its task with the steps, its state and progress, its
// PR and the checks, its last report, its branch. Its layout is the
// view's (view.Info), since it takes columns from the pane; what it shows
// is read here, with the sidebar's poll, from the project folder and the
// ticker's state. It is read-only: a click on the task opens the task
// view over the session, a click on the PR opens it in the browser, and
// a click anywhere else gives it the keyboard, whose arrows scroll it.

// infoData is what the panel shows about one thread.
type infoData struct {
	slug    string
	session proto.SessionInfo
	rec     *thread.Record
	status  *thread.Status
	report  *thread.Report
	task    *tasks.Task
	pr      ticker.PR
}

// loadInfo reads the panel's data for thread session s; nil when s is no
// thread's.
func loadInfo(paths server.Paths, s proto.SessionInfo) *infoData {
	if s.Role != proto.RoleThread || s.Project == "" || !thread.ValidID(s.Thread) {
		return nil
	}
	d := &infoData{slug: s.Project, session: s}
	p, err := project.Open(s.Project)
	if err != nil {
		return d
	}
	d.rec, _ = thread.Load(p, s.Thread)
	d.status, _ = thread.ReadStatus(p, s.Thread)
	d.report, _ = thread.ReadReport(p, s.Thread)
	if d.rec != nil && d.rec.TaskID() > 0 {
		d.task, _ = p.Tasks().Get(d.rec.TaskID())
	}
	if paths.Sessions != "" {
		d.pr = ticker.PRs(ticker.StatePath(paths.Sessions), s.Project)[s.Thread]
	}
	return d
}

// infoHit is what a click on a panel line does.
type infoHit int

const (
	hitNone infoHit = iota
	hitTask         // the task view over the session
	hitPR           // the PR in the browser
)

// prURL is the thread's PR's address: the ticker's, else its report's.
func (d *infoData) prURL() string {
	if d.pr.URL != "" {
		return d.pr.URL
	}
	if d.report != nil && strings.HasPrefix(d.report.PR, "https://") {
		return d.report.PR
	}
	return ""
}

// prLine is the PR's state in the ticker's words ("#70 open, checks
// pending"), with whether its branch is behind its base; "" without one.
func (d *infoData) prLine() string {
	s := d.pr.Summary()
	if s == "" {
		if n := prNumber(d.prURL()); n > 0 {
			s = "#" + strconv.Itoa(n)
		}
	}
	if s != "" && d.pr.State == "OPEN" && d.pr.MergeState == "BEHIND" {
		s += ", behind " + cmp.Or(d.pr.Base, "its base")
	}
	return s
}

// infoLines draws the panel's content w cells wide (its border not
// included), with what a click on each line does. now dates the ages.
func infoLines(d *infoData, w int, now time.Time) ([]string, []infoHit) {
	pl := &panel{w: w}
	var hits []infoHit
	// hit makes the lines added since line from clickable.
	hit := func(from int, h infoHit) {
		for len(hits) < len(pl.lines) {
			hits = append(hits, hitNone)
		}
		for i := from; i < len(hits); i++ {
			hits[i] = h
		}
	}
	if d == nil {
		pl.add(styleFaint.Render("no thread here"))
		return pl.lines, nil
	}
	s := d.session
	id := s.Thread
	// The task, with its steps; the one under way marked.
	switch t := d.task; {
	case t != nil:
		pl.wrap(styleHead.Render(oneLine(t.Ref() + " " + t.Title)))
		g, st := stateLook(string(t.Status))
		pl.add(st.Render(strings.TrimSpace(g+" "+string(t.Status))) + styleFaint.Render(" · click to open"))
		hit(0, hitTask)
		if len(t.Steps) > 0 {
			pl.gap()
			pl.add(styleFaint.Render("steps ") + progressLine(thread.Progress{Percent: pctOf(t.StepsDone(), len(t.Steps)), Done: t.StepsDone(), Total: len(t.Steps)}))
			cur := true
			for _, step := range t.Steps {
				glyph, text := todoGlyph("pending"), oneLine(step.Text)
				switch {
				case step.Done:
					glyph = todoGlyph("done")
					text = styleFaint.Render(text)
				case cur:
					glyph, text, cur = todoGlyph("in_progress"), styleAccent.Render(text), false
				}
				hang(pl, glyph+" ", text)
			}
		}
	case d.rec != nil:
		pl.wrap(styleHead.Render(oneLine(d.rec.Title)))
		pl.add(styleFaint.Render("no task"))
	}
	// The thread: its state, progress, what it does now, what it waits on.
	pl.gap()
	state := stateWord(s)
	if s.State == "blocked" && s.Reason != "" {
		state += " " + s.Reason
	}
	g, st := stateLook(s.State)
	pl.field("thread", id+" "+st.Render(strings.TrimSpace(g+" "+oneLine(state))))
	if ts := d.status; ts != nil {
		pl.field("progress", progressLine(ts.Progress()))
		switch {
		case ts.Current != "":
			pl.field("now", "▸ "+oneLine(ts.Current))
		case ts.Activity != "":
			pl.field("now", `"`+oneLine(ts.Activity)+`"`)
		}
		if ts.NeedsYou != "" {
			hang(pl, styleFaint.Render(fmt.Sprintf("%-9s", "needs you"))+" ", styleWarn.Render(oneLine(ts.NeedsYou)))
		}
	}
	// The PR and its checks.
	if l := d.prLine(); l != "" {
		look := styleAccent
		switch {
		case d.pr.Checks == "fail" || d.pr.Mergeable == "CONFLICTING":
			look = styleBad
		case d.pr.State == "MERGED":
			look = styleGood
		}
		from := len(pl.lines)
		hang(pl, styleFaint.Render(fmt.Sprintf("%-9s", "PR"))+" ", look.Render(l))
		if d.prURL() != "" {
			hit(from, hitPR)
		}
	}
	// The last report: its first lines and its Next items.
	if r := d.report; r != nil {
		pl.gap()
		head := "Last report"
		if d.rec != nil && !d.rec.ReportAt.IsZero() {
			head += styleFaint.Render(" · " + age(now.Sub(d.rec.ReportAt)) + " ago")
		}
		pl.add(styleHead.Render(head))
		for _, l := range reportHead(r.Text, 3) {
			pl.wrap(oneLine(l))
		}
		if len(r.Next) > 0 {
			pl.add(styleFaint.Render("Next"))
			for _, n := range r.Next {
				hang(pl, "• ", oneLine(n))
			}
		}
	}
	// Where it works.
	pl.gap()
	if rec := d.rec; rec != nil {
		pl.field("branch", oneLine(rec.Branch))
		pl.field("worktree", oneLine(homeShort(rec.Worktree)))
	}
	if at := d.lastActive(); !at.IsZero() {
		pl.field("active", age(now.Sub(at))+" ago")
	}
	for len(hits) < len(pl.lines) {
		hits = append(hits, hitNone)
	}
	return pl.lines, hits
}

// lastActive is when the thread last did something it tells tm about:
// its status or its last report.
func (d *infoData) lastActive() time.Time {
	var at time.Time
	if d.status != nil {
		at = d.status.Updated
	}
	if d.rec != nil && d.rec.ReportAt.After(at) {
		at = d.rec.ReportAt
	}
	return at
}

// hang adds lead then text wrapped to the panel, its next lines under
// the text.
func hang(pl *panel, lead, text string) {
	lw := ansi.StringWidth(lead)
	for i, l := range strings.Split(ansi.Wordwrap(text, max(pl.w-2-lw, 8), ""), "\n") {
		if i == 0 {
			pl.add(lead + l)
		} else {
			pl.add(strings.Repeat(" ", lw) + l)
		}
	}
}

// reportHead is the first n non-empty lines of a report's ## Report
// section.
func reportHead(text string, n int) []string {
	var out []string
	in := false
	for _, l := range strings.Split(text, "\n") {
		t := strings.TrimSpace(l)
		switch {
		case strings.HasPrefix(t, "## "):
			if in {
				return out
			}
			in = strings.EqualFold(t, "## Report")
		case in && t != "" && len(out) < n:
			out = append(out, t)
		}
	}
	return out
}

// homeShort writes a path under the home directory with ~.
func homeShort(p string) string {
	if home, _ := os.UserHomeDir(); home != "" && strings.HasPrefix(p, home) {
		return "~" + p[len(home):]
	}
	return p
}

// infoPanel is the attach client's info panel: what it shows and this
// console's own state of it. Guarded by client.mu.
type infoPanel struct {
	data  *infoData
	lines []string  // the content, as last laid out
	hits  []infoHit // what a click on each content line does
	top   int       // the first content line shown
	drag  bool      // the mouse is moving its border
	drawn []string  // the lines on screen; nil repaints them all
}

// infoX is the panel's first column (its border). c.mu held.
func (c *client) infoX() int { return c.cols - c.infoW }

// infoLayout lays the panel's content out at its width: when its data
// or its width changes, not with every frame. c.mu held.
func (c *client) infoLayout() {
	ip := c.info
	ip.lines, ip.hits = infoLines(ip.data, max(c.infoW-1, 1), time.Now())
	ip.top = min(ip.top, max(len(ip.lines)-c.infoRows(), 0))
}

// infoScroll moves the panel's content by d lines. c.mu held.
func (c *client) infoScroll(d int) {
	ip := c.info
	ip.top = min(max(ip.top+d, 0), max(len(ip.lines)-c.infoRows(), 0))
}

// infoRows is the panel's height: the window less the status bar, which
// runs under the pane and the panel. c.mu held.
func (c *client) infoRows() int {
	if c.statusBar {
		return max(c.rows-1, 1)
	}
	return c.rows
}

// appendInfo draws the panel's changed lines: its border, then its
// content from top. c.mu held.
func (c *client) appendInfo(b []byte, wrote bool) ([]byte, bool) {
	ip := c.info
	border := styleFaint.Render("│")
	if c.infoFocus {
		border = styleAccent.Render("│")
	}
	x, w := c.infoX(), c.infoW-1
	lines := make([]string, c.infoRows())
	for y := range lines {
		l := ""
		if i := ip.top + y; i < len(ip.lines) {
			l = ip.lines[i]
		}
		lines[y] = border + fit(l, w) + reset
		if y < len(ip.drawn) && ip.drawn[y] == lines[y] {
			continue
		}
		b = append(b, fmt.Sprintf("\x1b[%d;%dH\x1b[0m", y+1, x+1)...)
		b = append(b, lines[y]...)
		wrote = true
	}
	ip.drawn = lines
	return b, wrote
}

// infoHint is the status bar's note while the panel has the keyboard.
const infoHint = "info panel ▸ ↑ ↓ scroll · enter task · esc back to the pane"

// setInfo changes the view's info panel: a layout change, so every
// console's pane follows. save also keeps it in ui.json, as the default
// of new views.
func (c *client) setInfo(l view.Info, save bool) {
	c.act(proto.MethodViewInfo, proto.ViewParams{Info: &l})
	if !save || !c.lock() {
		return
	}
	path := ""
	if c.side != nil {
		path = c.side.uiFile
	}
	c.mu.Unlock()
	if path == "" {
		return
	}
	if err := SaveInfo(path, l); err != nil && c.lock() {
		c.flash = "ui.json: " + err.Error()
		c.status()
		c.mu.Unlock()
		c.poke()
	}
}

// infoToggle shows or hides the panel (prefix+|).
func (c *client) infoToggle() {
	if !c.lock() {
		return
	}
	if !c.v.Thread {
		c.flash = "the info panel shows beside a thread's pane"
		c.status()
		c.mu.Unlock()
		return
	}
	l, msg := c.v.Info.Toggle(c.viewCols(), c.geo.SideW)
	if l.Off {
		c.infoFocus = false
	}
	c.flash = msg
	c.status()
	c.mu.Unlock()
	c.setInfo(l, true)
}

// infoKeyboard runs a key while the panel has the keyboard: the arrows
// scroll it, enter shows the task, esc and tab give the keyboard back to
// the pane; any other key is dropped. c.mu held; released here.
func (c *client) infoKeyboard(k uv.Key) {
	c.flash = ""
	task := false
	switch keyName(k) {
	case "up", "k":
		c.infoScroll(-1)
	case "down", "j":
		c.infoScroll(1)
	case "pgup":
		c.infoScroll(-c.rows / 2)
	case "pgdown":
		c.infoScroll(c.rows / 2)
	case "home":
		c.infoScroll(-len(c.info.lines))
	case "end":
		c.infoScroll(len(c.info.lines))
	case "enter":
		task = true
	case "esc", "tab":
		c.infoFocus = false
	}
	c.status()
	c.mu.Unlock()
	c.poke()
	if task {
		c.infoTask()
	}
}

// infoTask opens the task view on the thread's task over the session.
func (c *client) infoTask() {
	if !c.lock() {
		return
	}
	d := c.info.data
	if d == nil || d.task == nil || !c.dashboard {
		if d != nil && d.task != nil {
			c.flash = d.task.Ref() + ": tm shows tasks on the dashboard"
		} else {
			c.flash = "no task"
		}
		c.status()
		c.mu.Unlock()
		c.poke()
		return
	}
	slug, id := d.slug, d.task.ID
	c.infoFocus = false
	c.mu.Unlock()
	c.popupTask(slug, id)
}

// openURL opens a web address in the browser (a variable for tests).
var openURL = func(url string) error {
	name := "xdg-open"
	if runtime.GOOS == "darwin" {
		name = "open"
	}
	return exec.Command(name, url).Start()
}

// infoMouse handles the mouse over the panel or dragging its border: the
// border drags, a click on the task or the PR opens it, any other click
// gives the panel the keyboard; the wheel scrolls it. c.mu held;
// released here.
func (c *client) infoMouse(m emu.Mouse) {
	ip := c.info
	switch {
	case ip.drag && m.Action == emu.MouseMotion:
		// The border's column in the view's window, which may be another
		// size than this one.
		x := c.viewCols() - (c.cols - m.X)
		l := c.v.Info.DragTo(x, c.viewCols(), c.geo.SideW)
		same := l == c.v.Info
		c.mu.Unlock()
		if !same {
			c.setInfo(l, false)
		}
		c.poke()
		return
	case ip.drag && m.Action == emu.MouseRelease:
		ip.drag = false
		l := c.v.Info
		c.mu.Unlock()
		c.setInfo(l, true)
		return
	case m.Action == emu.MousePress && (m.Button == emu.MouseWheelUp || m.Button == emu.MouseWheelDown):
		c.infoScroll(map[bool]int{true: -3, false: 3}[m.Button == emu.MouseWheelUp])
		c.mu.Unlock()
		c.poke()
		return
	case m.Action != emu.MousePress:
		c.mu.Unlock()
		return
	case m.Button == emu.MouseRight:
		c.openMenu("", c.sessionItems(), m.X, m.Y, false)
		c.mu.Unlock()
		c.poke()
		return
	case m.Button != emu.MouseLeft:
		c.mu.Unlock()
		return
	}
	if m.X == c.infoX() {
		ip.drag = true
		c.mu.Unlock()
		return
	}
	h := hitNone
	if i := ip.top + m.Y; i >= 0 && i < len(ip.hits) {
		h = ip.hits[i]
	}
	// A click in the panel gives it the keyboard (and takes it from the
	// sidebar).
	c.infoFocus, c.sideFocus = true, false
	url := ""
	if ip.data != nil {
		url = ip.data.prURL()
	}
	c.status()
	c.mu.Unlock()
	c.poke()
	switch h {
	case hitTask:
		c.infoTask()
	case hitPR:
		if err := openURL(url); err != nil && c.lock() {
			c.flash = "can't open the browser: " + err.Error() + "; the PR is " + url
			c.status()
			c.mu.Unlock()
			c.poke()
		}
	}
}

// pollInfo reads the panel's data for the focused session, outside c.mu.
func (c *client) pollInfo(sessions []proto.SessionInfo) {
	if !c.lock() {
		return
	}
	id, on := c.v.Focus, c.info != nil
	c.mu.Unlock()
	if !on {
		return
	}
	var d *infoData
	for _, s := range sessions {
		if s.ID == id {
			d = loadInfo(c.paths, s)
		}
	}
	if c.lock() {
		c.info.data = d
		c.infoLayout()
		c.mu.Unlock()
	}
}
