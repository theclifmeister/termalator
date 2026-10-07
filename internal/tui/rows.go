package tui

import (
	"cmp"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/theclifmeister/terminatr/internal/codehost"
	"github.com/theclifmeister/terminatr/internal/proto"
	"github.com/theclifmeister/terminatr/internal/tasks"
)

// Rows: what the dashboard list shows, as data. Layout and styling
// happen when a row is drawn (text), not when it is built.

// row is one dashboard line: a section header, an unselectable note, or
// an item with columns.
type row struct {
	key  string // selection identity, stable across refreshes
	head string // a section header: NEEDS YOU, the project, SESSIONS
	note string // an unselectable line: task counts, "no projects; …"

	// An item's columns: its marker (" ! ", indent), who (project slug or
	// session id; rows of the project's own section have none), what
	// (coordinator, thread, command), its state word and the rest of the
	// line. lead starts the rest in a colour of its own: a block's
	// reason, or a new report.
	mark, who, what, state, lead, rest string
	leadBad                            bool // lead is a block's reason, else a note in the warning colour
	// pct is the progress bar's percent, -1 for none.
	pct int
	// count is a section header's number of items.
	count int
	// whoW is who's width, 0 for colWho; whatMin is how narrow what may
	// get to leave the lead room, 0 for colWhatMin. The task list's rows
	// set both: an id is short, and the title shows whole when opened.
	whoW, whatMin int

	session string
	project string
	thread  *ThreadRow
	task    *tasks.Task // a needs-you task's row in NEEDS YOU
}

func (r row) selectable() bool { return r.key != "" }

// widths are an item's column widths in a list w cells wide: who (0 for
// none), what and the state. what takes a share of the room, so wide
// lists show whole titles and the rest keeps room for progress.
func (r row) widths(w int) (who, what, state int) {
	state = colState
	if r.who != "" {
		who = cmp.Or(r.whoW, colWho)
	}
	room := w - len([]rune(r.mark)) - state - colGap
	if who > 0 {
		room -= who + colGap
	}
	what = min(max(room*9/20, colWhatMin), colWhatMax)
	if lead := ansi.StringWidth(r.lead); lead > 0 {
		// The lead (a block's reason, waiting on the coordinator) shows
		// whole, and the "…" of a cut rest after it: what gives way,
		// down to its minimum.
		what = max(min(what, room-colGap-lead-2), cmp.Or(r.whatMin, colWhatMin))
	}
	return who, what, state
}

// text is the row laid out in columns for w cells, unstyled.
func (r row) text(w int) string {
	if r.note != "" {
		return r.note
	}
	return r.line(w, r.hasBar(w), func(_ int, s string) string { return s })
}

// line lays the row out for w cells; paint styles each column (0 mark, 1
// who, 2 what, 3 state, 4 lead, 5 bar, 6 rest).
func (r row) line(w int, withBar bool, paint func(col int, s string) string) string {
	who, what, state := r.widths(w)
	gap := strings.Repeat(" ", colGap)
	out := paint(0, r.mark)
	if who > 0 {
		out += paint(1, fit(r.who, who)) + gap
	}
	out += paint(2, fit(r.what, what)) + gap + paint(3, fit(stateText(r.state), state)) + gap
	var rest []string
	if r.lead != "" {
		rest = append(rest, paint(4, r.lead))
	}
	if withBar {
		rest = append(rest, paint(5, bar(r.pct)))
	}
	if r.rest != "" {
		rest = append(rest, paint(6, r.rest))
	}
	return out + strings.Join(rest, "  ")
}

// hasBar says whether the row draws its progress bar: only when it has
// a percent and the whole row still fits in w cells.
func (r row) hasBar(w int) bool {
	if r.pct < 0 {
		return false
	}
	return ansi.StringWidth(r.line(w, true, func(_ int, s string) string { return s })) <= w
}

// styled is text in the theme's colours, w cells wide.
func (r row) styled(w int) string {
	if r.note != "" {
		return styleFaint.Render(fit(r.note, w))
	}
	_, st := stateLook(r.state)
	line := r.line(w, r.hasBar(w), func(col int, s string) string {
		switch col {
		case 0:
			return markStyle(r.mark).Render(s)
		case 1:
			if strings.HasPrefix(r.key, "p:") {
				return styleHead.Render(s)
			}
		case 3:
			return st.Render(s)
		case 4:
			if r.leadBad {
				return styleBad.Render(s)
			}
			return styleWarn.Render(s)
		case 5:
			n := strings.Count(s, "▰")
			return styleGood.Render(strings.Repeat("▰", n)) + styleFaint.Render(strings.Repeat("▱", 5-n))
		}
		return s
	})
	return fit(line, w) + reset
}

// reset ends any style a truncated line left open.
const reset = "\x1b[m"

const (
	colWho     = 12 // project slug, or session id
	colWhatMin = 20 // coordinator, thread, command: at least,
	colWhatMax = 40 // and at most
	colState   = 10 // a glyph, a space and the word
	colGap     = 2  // between columns (docs/STYLE.md S2)
)

// Row markers: none but the gutter. A row's section and its state word
// say why it shows (NEEDS YOU, ▲ blocked), so every section's rows share
// one column grid.
const (
	markBlocked = markTop
	markNeeds   = markTop // a task that waits on the user
	markTop     = "  "
)

// threadState is a thread's state as rows and the sidebar show it: its
// live session's (with the block's reason), done once it called tm done
// and isn't working or blocked, else its record's. session is its live
// session, "" without one.
func threadState(t *ThreadRow, byID map[string]proto.SessionInfo) (state, reason, session string) {
	state = t.State
	if s, ok := byID[t.Session]; ok && t.Session != "" {
		session, state = s.ID, stateWord(s)
		if s.State == "blocked" {
			reason = blockReason(s)
		}
	}
	if (t.Done || t.Status != nil && t.Status.Done) && state != "blocked" && state != "working" {
		state = "done"
	}
	return state, reason, session
}

// listProject is the project the dashboard lists: current when it is
// one, else the first.
func listProject(d Data, current string) string {
	for _, p := range d.Projects {
		if p.Slug == current {
			return current
		}
	}
	if len(d.Projects) > 0 {
		return d.Projects[0].Slug
	}
	return ""
}

// buildRows lays out NEEDS YOU, the project's own section and SESSIONS.
// The sidebar's tree lists every project; the list shows one, project
// (listProject): its coordinator, threads, sessions and task counts.
// NEEDS YOU, across projects, holds only what waits on the user: blocked
// coordinators, the tasks in each project's Needs you group (review or
// blocked) under its project, and blocked sessions of the user's own
// outside the projects. The task rows are to see, not to act on: enter
// shows the task in the project popup, and the coordinator accepts the
// work. Everything about threads goes to their coordinator, the user's
// single point of contact (§4).
func buildRows(d Data, project string) []row {
	var projs, other []row
	bySlug := map[string]bool{}
	for _, p := range d.Projects {
		bySlug[p.Slug] = true
	}
	byID := map[string]proto.SessionInfo{}
	for _, s := range d.Sessions {
		byID[s.ID] = s
	}
	now := time.Now()
	// NEEDS YOU by project, in the projects' order, then the rest.
	needsOf := map[string][]row{}
	var loose []row
	for _, s := range d.Sessions {
		if s.State == "blocked" && s.Role != proto.RoleThread {
			who := s.Project
			if who == "" {
				who = s.ID
			}
			r := row{key: "n:" + s.ID, session: s.ID, project: s.Project,
				mark: markBlocked, who: who, what: sessionName(s), state: "blocked", lead: oneLine(s.Reason), leadBad: true,
				rest: progressOnly(s), pct: -1}
			if bySlug[s.Project] {
				needsOf[s.Project] = append(needsOf[s.Project], r)
			} else {
				loose = append(loose, r)
			}
		}
	}
	var needs []row
	for _, p := range d.Projects {
		needs = append(needs, needsOf[p.Slug]...)
		for _, t := range p.NeedsYou {
			r := row{key: fmt.Sprintf("nt:%s:%d", p.Slug, t.ID), project: p.Slug, task: t,
				mark: markNeeds, who: p.Slug, what: t.Ref() + " " + oneLine(t.Title), state: string(t.Status), rest: t.Thread, pct: -1}
			if len(t.Steps) > 0 {
				r.pct = pctOf(t.StepsDone(), len(t.Steps))
				r.rest = joinSp(fmt.Sprintf("%d/%d", t.StepsDone(), len(t.Steps)), t.Thread)
			}
			needs = append(needs, r)
		}
	}
	needs = append(needs, loose...)
	var head string
	for _, p := range d.Projects {
		if p.Slug != project {
			continue
		}
		head = p.Slug
		if p.Name != "" && !strings.EqualFold(p.Name, p.Slug) {
			head = p.Slug + " · " + oneLine(p.Name)
		}
		var coord *proto.SessionInfo
		var members []proto.SessionInfo
		threadOf := map[string]bool{}
		for _, t := range p.Threads {
			threadOf[t.ID] = true
		}
		for i, s := range d.Sessions {
			if s.Project != p.Slug {
				continue
			}
			if s.Role == proto.RoleCoordinator && coord == nil {
				coord = &d.Sessions[i]
				continue
			}
			if s.Role == proto.RoleThread && threadOf[s.Thread] {
				continue // shown as its thread's row
			}
			members = append(members, s)
		}
		r := row{key: "p:" + p.Slug, project: p.Slug, mark: markTop, what: "coordinator", state: "—",
			rest: "not running; enter starts it", pct: -1}
		if coord != nil {
			r.session, r.pct, r.state = coord.ID, sessionPct(*coord), stateWord(*coord)
			if coord.State == "blocked" {
				r.lead, r.leadBad = oneLine(coord.Reason), true
			}
			r.rest = progressOnly(*coord)
		}
		if p.Err != "" {
			r.rest = "error: " + oneLine(p.Err)
		}
		if p.Unread > 0 {
			r.rest = joinSp(r.rest, fmt.Sprintf("%d inbox", p.Unread))
		}
		projs = append(projs, r)
		threads := append([]ThreadRow(nil), p.Threads...)
		sort.SliceStable(threads, func(i, j int) bool {
			return threadGroup(threads[i], byID) < threadGroup(threads[j], byID)
		})
		for i := range threads {
			t := &threads[i]
			tr := row{key: "th:" + p.Slug + ":" + t.ID, project: p.Slug, thread: t, mark: markTop, pct: -1}
			tr.pct = t.Status.Progress().Percent
			var reason string
			tr.state, reason, tr.session = threadState(t, byID)
			switch {
			case reason != "":
				tr.lead, tr.leadBad = oneLine(reason), true
			case t.ReportState() == "new":
				tr.lead = "report new"
			}
			// A thread leads with its task: its progress and step first,
			// so a narrow row cuts its own id (threadName), last.
			rest := threadProgress(t.Status)
			if t.Status != nil && !t.Status.Updated.IsZero() {
				rest = joinSp(rest, age(now.Sub(t.Status.Updated)))
			}
			if t.Report != nil && t.Report.PR != "" {
				rest = joinSp(rest, "PR "+prRef(t.Report.PR))
			}
			if t.Task != "" {
				rest = joinSp(rest, t.ID)
			}
			tr.what, tr.rest = threadName(t.Task, t.ID)+" "+oneLine(t.Title), rest
			projs = append(projs, tr)
		}
		sort.SliceStable(members, func(i, j int) bool { return members[i].Thread < members[j].Thread })
		for _, s := range members {
			sr := row{key: "s:" + s.ID, session: s.ID, project: p.Slug, mark: markTop, what: s.ID + " " + sessionName(s),
				state: stateWord(s), rest: joinSp(progressOnly(s), age(now.Sub(s.Created))), pct: sessionPct(s)}
			if s.State == "blocked" {
				sr.lead, sr.leadBad = oneLine(s.Reason), true
			}
			projs = append(projs, sr)
		}
		c := p.Counts
		projs = append(projs, row{note: "  tasks: " + taskCounts(c)})
	}
	home, _ := os.UserHomeDir()
	for _, s := range d.Sessions {
		if s.Project != "" && bySlug[s.Project] {
			continue
		}
		where := s.Cwd
		if home != "" && strings.HasPrefix(where, home) {
			where = "~" + where[len(home):]
		}
		sr := row{key: "s:" + s.ID, session: s.ID,
			mark: markTop, who: s.ID, what: sessionName(s), state: stateWord(s), rest: joinSp(progressOnly(s), age(now.Sub(s.Created)), where), pct: sessionPct(s)}
		if s.State == "blocked" {
			sr.lead, sr.leadBad = oneLine(s.Reason), true
		}
		other = append(other, sr)
	}

	var rows []row
	if len(needs) > 0 {
		rows = append(rows, row{head: "NEEDS YOU", count: len(needs)})
		rows = append(rows, needs...)
	}
	if head != "" {
		rows = append(rows, row{head: head})
		rows = append(rows, projs...)
	} else {
		rows = append(rows, row{note: "  no projects; n creates one"})
	}
	// The header's count is every session; this section has those outside
	// the projects, so it says so.
	rows = append(rows, row{head: "OTHER SESSIONS", count: len(other)})
	if len(other) == 0 {
		note := "  no sessions; s starts a shell"
		if n := len(d.Sessions); n > 0 {
			note = fmt.Sprintf("  none outside the projects (%d in them); s starts a shell", n)
		}
		other = []row{{note: note}}
	}
	return append(rows, other...)
}

// taskCounts are a project's task counts; tasks that need the user say
// where to see them: the task list (t).
func taskCounts(c map[string]int) string {
	needs := needsYouWords(c["needs_you"])
	if c["needs_you"] > 0 {
		needs += " (t lists them)"
	}
	return fmt.Sprintf("%s · %d in motion · %d on deck", needs, c["in_motion"], c["on_deck"])
}

// needsYouWords is "1 needs you", "2 need you".
func needsYouWords(n int) string {
	if n == 1 {
		return "1 needs you"
	}
	return fmt.Sprintf("%d need you", n)
}

// sessionPct is a session's todo percent, -1 without todos.
func sessionPct(s proto.SessionInfo) int { return sessionProgress(s).Percent }

// threadGroup orders a project's threads as §7.4 does: waiting on you,
// ready for review, working, idle, then the rest.
func threadGroup(t ThreadRow, byID map[string]proto.SessionInfo) int {
	s, live := byID[t.Session]
	live = live && t.Session != ""
	switch {
	case live && s.State == "blocked", t.Status != nil && t.Status.NeedsYou != "":
		return 0
	case t.ReportState() == "new":
		return 1
	case live && s.State == "working":
		return 2
	case live:
		return 3
	}
	return 4
}

// prRef shortens a PR URL to "#12"; anything else is shown cut short.
func prRef(url string) string {
	if n, _, ok := codehost.ParsePRURL(url); ok {
		return "#" + strconv.Itoa(n)
	}
	return fit(oneLine(url), 30)
}

// threadDetail is what shows under a selected thread row (§4): its full
// todo list and its task's steps, styled and indented by ind, and, with
// report set, its latest report's state. The coordinator alone acts on
// reports, so their ## Next lines never show here; the user only reads.
func threadDetail(t *ThreadRow, ind string, report bool) []string {
	var out []string
	if t.Status != nil && len(t.Status.Todos) > 0 {
		out = append(out, ind+styleFaint.Render("todos"))
		for _, td := range t.Status.Todos {
			out = append(out, ind+"  "+todoGlyph(string(td.Status))+" "+oneLine(td.Text))
		}
	}
	if t.TaskRec != nil && len(t.TaskRec.Steps) > 0 {
		out = append(out, ind+styleFaint.Render(fmt.Sprintf("steps %d/%d", t.TaskRec.StepsDone(), len(t.TaskRec.Steps))))
		for _, st := range t.TaskRec.Steps {
			out = append(out, fmt.Sprintf("%s  %s %d %s", ind, todoGlyph(map[bool]string{true: "done"}[st.Done]), st.N, oneLine(st.Text)))
		}
	}
	if report && t.Reports > 0 {
		out = append(out, ind+styleFaint.Render("report ")+reportState(t, false))
	}
	if len(out) == 0 {
		out = append(out, ind+styleFaint.Render("no todos, steps or report yet"))
	}
	return out
}

// reportState is a thread's latest report as the user sees it: whether
// the coordinator has read it, "new · for the coordinator" or "read",
// after how many there are with count.
func reportState(t *ThreadRow, count bool) string {
	rs := "read"
	if t.ReportState() == "new" {
		rs = "new"
	}
	if count {
		rs = fmt.Sprintf("%d, %s", t.Reports, rs)
	}
	if t.ReportState() == "new" {
		return styleWarn.Render(rs) + styleFaint.Render(" · for the coordinator")
	}
	return rs
}

func joinSp(parts ...string) string {
	var out []string
	for _, p := range parts {
		if p != "" {
			out = append(out, p)
		}
	}
	return strings.Join(out, "  ")
}
