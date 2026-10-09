package tui

import (
	"cmp"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"

	"github.com/theclifmeister/terminatr/internal/emu"
	"github.com/theclifmeister/terminatr/internal/plat/fsx"
	"github.com/theclifmeister/terminatr/internal/plat/open"
	"github.com/theclifmeister/terminatr/internal/project"
	"github.com/theclifmeister/terminatr/internal/proto"
	"github.com/theclifmeister/terminatr/internal/server"
	"github.com/theclifmeister/terminatr/internal/tasks"
	"github.com/theclifmeister/terminatr/internal/thread"
	"github.com/theclifmeister/terminatr/internal/ticker"
	"github.com/theclifmeister/terminatr/internal/view"
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
	// attached: the names of the files its reports attached for the
	// user. Names only: the UI shows no file paths (§4).
	attached []string
	// refused are the guard's latest refusals of the thread, oldest
	// first, from the journal.
	refused []refusal
	// watch is a coordinator's project as the /tm pane shows it
	// (project.watch); set only beside a coordinator, which has none of
	// the above but slug and session.
	watch *proto.ProjectWatch
}

// loadInfo reads the panel's data for session s, one of sessions: a
// thread's, or a coordinator's (its project, coordpanel.go); nil for any
// other.
func loadInfo(paths server.Paths, s proto.SessionInfo, sessions []proto.SessionInfo) *infoData {
	if s.Role == proto.RoleCoordinator && s.Project != "" {
		return loadCoordInfo(paths, s, sessions)
	}
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
	d.attached = thread.Attachments(p, s.Thread)
	d.refused = guardRefusals(p, s.Thread, maxRefusals)
	if d.rec != nil && d.rec.TaskID() > 0 {
		d.task, _ = p.Tasks().Get(d.rec.TaskID())
	}
	if paths.Sessions != "" {
		d.pr = ticker.PRs(ticker.StatePath(paths.Sessions), s.Project)[s.Thread]
	}
	return d
}

// infoHit is what a click on a panel line does: its kind, and the task,
// session or address it opens.
type infoHit struct {
	kind    hitKind
	task    int    // hitTask: the task's number
	session string // hitSession: the session to show
	url     string // hitPR: the PR's address
}

// hitKind is the kind of an infoHit.
type hitKind int

const (
	hitNone    hitKind = iota
	hitTask            // the task view over the session
	hitPR              // the PR in the browser
	hitSession         // that session in the pane (a coordinator's thread row)
	hitQuestions       // the coordinator opens its questions (tm ask open)
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
		if n := ticker.PRNumber(d.prURL()); n > 0 {
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
			hits = append(hits, infoHit{})
		}
		for i := from; i < len(hits); i++ {
			hits[i] = h
		}
	}
	if d == nil {
		pl.add(styleFaint.Render("no thread here"))
		return pl.lines, nil
	}
	if d.watch != nil {
		return coordLines(d.watch, w, now)
	}
	s := d.session
	id := s.Thread
	// The task, with its steps; the one under way marked.
	switch t := d.task; {
	case t != nil:
		pl.wrap(styleTitle.Render(oneLine(t.Ref() + " " + t.Title)))
		g, st := stateLook(string(t.Status))
		pl.add(st.Render(strings.TrimSpace(g+" "+string(t.Status))) + styleFaint.Render(" · click to open"))
		hit(0, infoHit{kind: hitTask, task: t.ID})
		if len(t.Steps) > 0 {
			pl.section("STEPS", "")
			pl.add(progressLine(thread.Progress{Percent: pctOf(t.StepsDone(), len(t.Steps)), Done: t.StepsDone(), Total: len(t.Steps)}))
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
		pl.wrap(styleTitle.Render(oneLine(d.rec.Title)))
		pl.add(styleFaint.Render("no task"))
	}
	// The thread: its state, progress, what it does now, what it waits on.
	pl.section("THREAD", "")
	state := stateWord(s)
	if s.State == "blocked" && s.Reason != "" {
		state += " " + blockReason(s)
	}
	g, st := stateLook(s.State)
	pl.field("thread", id+" "+st.Render(strings.TrimSpace(g+" "+oneLine(state))))
	if d.rec != nil && d.rec.Model != "" {
		pl.field("model", oneLine(d.rec.Model))
	}
	if d.rec != nil {
		pl.field("usage", d.rec.Usage.String())
	}
	if ts := d.status; ts != nil {
		if d.task == nil || len(d.task.Steps) == 0 {
			// A task with steps shows its progress above, once.
			pl.field("progress", progressLine(ts.Progress()))
		}
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
	if q := s.Question; q != nil {
		pl.section("QUESTION OPEN", age(now.Sub(q.Since))+" ago")
		for _, l := range questionLines(q) {
			pl.wrap(l)
		}
		pl.gap()
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
			hit(from, infoHit{kind: hitPR, url: d.prURL()})
		}
	}
	// The last report: its first lines. Its Next items are the
	// coordinator's and never show in the TUI.
	if r := d.report; r != nil {
		when := ""
		if d.rec != nil && !d.rec.ReportAt.IsZero() {
			when = age(now.Sub(d.rec.ReportAt)) + " ago"
		}
		pl.section("LAST REPORT", when)
		for _, l := range reportHead(r.Text, 3) {
			pl.wrap(oneLine(l))
		}
	}
	if len(d.attached) > 0 {
		names := make([]string, len(d.attached))
		for i, n := range d.attached {
			names[i] = oneLine(n)
		}
		hang(pl, styleFaint.Render(fmt.Sprintf("%-9s", "attached"))+" ", strings.Join(names, ", "))
	}
	// What the guard refused: journaled, not sent to the inbox.
	if len(d.refused) > 0 {
		pl.gap()
		pl.add(styleHead.Render("Guard refused"))
		for _, r := range d.refused {
			hang(pl, styleFaint.Render(age(now.Sub(r.at))+" ago")+" ", styleWarn.Render(oneLine(r.what)))
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
		hits = append(hits, infoHit{})
	}
	return pl.lines, hits
}

// maxRefusals is how many of the guard's refusals the panel lists.
const maxRefusals = 3

// refusal is one guard.deny line of the journal.
type refusal struct {
	at   time.Time
	what string // "<rule> <tool>: <summary>"
}

// guardRefusals is the last n guard.deny lines the journal holds for
// thread id, oldest first.
func guardRefusals(p *project.Project, id string, n int) []refusal {
	lines, _, err := p.JournalTail(500)
	if err != nil {
		return nil
	}
	var out []refusal
	for _, l := range lines {
		// "<time> <who> guard.deny <ref> <detail>"
		f := strings.SplitN(l, " ", 5)
		if len(f) < 5 || f[2] != "guard.deny" || f[3] != id {
			continue
		}
		at, err := time.Parse(time.RFC3339, f[0])
		if err != nil {
			continue
		}
		out = append(out, refusal{at: at, what: f[4]})
	}
	return out[max(len(out)-n, 0):]
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
	home, _ := os.UserHomeDir()
	return shortHome(p, home)
}

// shortHome writes p with ~ for home when p is home or under it.
func shortHome(p, home string) string {
	rel, ok := fsx.Rel(home, p)
	switch {
	case !ok:
		return p
	case rel == ".":
		return "~"
	}
	return "~" + string(filepath.Separator) + rel
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
	if c.kb == areaInfo {
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

// infoHintNow is infoHint, naming a when a coordinator's panel has open
// questions to answer. c.mu held.
func (c *client) infoHintNow() string {
	if c.info == nil {
		return infoHint
	}
	if d := c.info.data; d != nil && d.watch != nil && d.watch.Questions > 0 {
		return strings.Replace(infoHint, " · esc", " · a answers questions · esc", 1)
	}
	return infoHint
}

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
	if !c.v.Panel {
		c.flash = "the info panel shows beside a thread's or a coordinator's pane"
		c.status()
		c.mu.Unlock()
		return
	}
	l, msg := c.v.Info.Toggle(c.viewCols(), c.geo.SideW)
	if l.Off && c.kb == areaInfo {
		c.kb = areaMain
	}
	c.flash = msg
	c.status()
	c.mu.Unlock()
	c.setInfo(l, true)
}

// infoKeyboard runs a key while the panel has the keyboard: the arrows
// scroll it, enter shows the task, a has a coordinator open its
// questions, esc and tab give the keyboard back to the pane (tab, as
// prefix+tab, to the next area: the pane); any other key is dropped.
// c.mu held; released here.
func (c *client) infoKeyboard(k uv.Key) {
	c.flash = ""
	task, answer := false, false
	switch keyName(k) {
	case "a":
		answer = true
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
		c.kb = areaMain
	}
	c.status()
	c.mu.Unlock()
	c.poke()
	if task {
		c.infoTask(0)
	}
	if answer {
		c.infoQuestions()
	}
}

// openQuestions has project slug's coordinator open its questions
// (questions.open); a variable for tests.
var openQuestions = func(paths server.Paths, slug string) (proto.QuestionsOpenResult, error) {
	var res proto.QuestionsOpenResult
	cl, err := server.Connect(paths, false)
	if err != nil {
		return res, err
	}
	defer cl.Close()
	err = cl.Call(proto.MethodQuestionsOpen, proto.QuestionsOpenParams{Project: slug}, &res)
	return res, err
}

// infoQuestions has the coordinator whose panel shows open its questions
// in its question dialog, and says so in the status bar.
func (c *client) infoQuestions() {
	if !c.lock() {
		return
	}
	d, paths := c.info.data, c.paths
	c.mu.Unlock()
	slug, n := "", 0
	if d != nil && d.watch != nil {
		slug, n = d.slug, d.watch.Questions
	}
	flash := "no open questions"
	if n > 0 {
		flash = questionsFlash(openQuestions(paths, slug))
	}
	if c.lock() {
		c.flash = flash
		c.status()
		c.mu.Unlock()
		c.poke()
	}
}

// questionsFlash says what came of asking the coordinator to open its
// questions.
func questionsFlash(res proto.QuestionsOpenResult, err error) string {
	if err != nil {
		var perr *proto.Error
		if errors.As(err, &perr) {
			return "not asked: " + perr.Message
		}
		return "not asked: " + err.Error()
	}
	return "asked the coordinator: your questions open in its dialog once it is free"
}

// infoTask opens the task view on task id over the session; 0 is the
// thread's own task (enter).
func (c *client) infoTask(id int) {
	if !c.lock() {
		return
	}
	d := c.info.data
	if id == 0 && d != nil && d.task != nil {
		id = d.task.ID
	}
	if d == nil || id == 0 || !c.dashboard {
		switch {
		case id != 0:
			c.flash = "T" + strconv.Itoa(id) + ": tm shows tasks on the dashboard"
		case d != nil && d.watch != nil:
			c.flash = "click a task to open it"
		default:
			c.flash = "no task"
		}
		c.status()
		c.mu.Unlock()
		c.poke()
		return
	}
	slug := d.slug
	c.kb = areaMain
	c.mu.Unlock()
	c.popupTask(slug, id)
}

// openURL opens a web address in the browser (a variable for tests).
var openURL = open.URL

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
	var h infoHit
	if i := ip.top + m.Y; i >= 0 && i < len(ip.hits) {
		h = ip.hits[i]
	}
	// A click in the panel gives it the keyboard.
	c.kb = areaInfo
	slug := ""
	if ip.data != nil {
		slug = ip.data.slug
	}
	c.status()
	c.mu.Unlock()
	c.poke()
	switch h.kind {
	case hitTask:
		c.infoTask(h.task)
	case hitSession:
		c.sideGo(Target{Project: slug, Session: h.session})
	case hitQuestions:
		c.infoQuestions()
	case hitPR:
		if err := openURL(h.url); err != nil && c.lock() {
			c.flash = "can't open the browser: " + err.Error() + "; the PR is " + h.url
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
			d = loadInfo(c.paths, s, sessions)
		}
	}
	if c.lock() {
		c.info.data = d
		c.infoLayout()
		c.mu.Unlock()
	}
}
