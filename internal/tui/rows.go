package tui

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/theclifmeister/termalator/internal/proto"
	"github.com/theclifmeister/termalator/internal/tasks"
)

// Rows: what the dashboard list shows, as data. Layout and styling
// happen when a row is drawn (text), not when it is built.

// row is one dashboard line: a section header, an unselectable note, or
// an item with columns.
type row struct {
	key  string // selection identity, stable across refreshes
	head string // a section header: NEEDS YOU, PROJECTS, SESSIONS
	note string // an unselectable line: task counts, "no projects; …"

	// An item's columns: its marker (" ! ", " ? ", indent), who (project
	// slug or session id), what (coordinator, thread, task, command), its
	// state and the rest of the line.
	mark, who, what, state, rest string

	session string
	project string
	task    *tasks.Task
	confirm bool // a done confirmation: d completes it whatever the status
	thread  *ThreadRow
}

func (r row) selectable() bool { return r.key != "" }

// text is the row laid out in columns.
func (r row) text() string {
	if r.note != "" {
		return r.note
	}
	return cols(r.mark, r.who, r.what, r.state, r.rest)
}

const (
	colWho   = 12 // project slug, or session id
	colWhat  = 30 // coordinator, thread, task, command
	colState = 9
)

func cols(prefix, who, what, state, rest string) string {
	return prefix + fit(who, colWho) + " " + fit(what, colWhat) + " " + fit(state, colState) + " " + rest
}

// Row markers.
const (
	markBlocked = " ! "
	markAsk     = " ? "
	markTop     = "  "
	markNested  = "    "
)

// buildRows lays out NEEDS YOU, PROJECTS and SESSIONS.
func buildRows(d Data) []row {
	var needs, projs, other []row
	bySlug := map[string]bool{}
	for _, p := range d.Projects {
		bySlug[p.Slug] = true
	}
	now := time.Now()
	for _, s := range d.Sessions {
		if s.State == "blocked" {
			who := s.Project
			if who == "" {
				who = s.ID
			}
			needs = append(needs, row{key: "n:" + s.ID, session: s.ID, project: s.Project,
				mark: markBlocked, who: who, what: sessionName(s), state: "blocked", rest: progress(s)})
		}
	}
	for _, p := range d.Projects {
		for _, t := range p.NeedsYou {
			rest := ""
			if t.Thread != "" {
				rest = "← " + t.Thread
			}
			needs = append(needs, row{key: fmt.Sprintf("t:%s:%d", p.Slug, t.ID), project: p.Slug, task: t,
				mark: markAsk, who: p.Slug, what: t.Ref() + " " + oneLine(t.Title), state: string(t.Status), rest: rest})
		}
		for _, it := range p.Inbox {
			r := row{key: "i:" + p.Slug + ":" + it.ID, project: p.Slug,
				mark: markAsk, who: p.Slug, what: oneLine(it.Summary), state: "inbox", rest: it.Kind}
			if it.Task != nil {
				r.task, r.confirm = it.Task, true
				r.what, r.state = it.Task.Ref()+" "+oneLine(it.Task.Title), "confirm"
				r.rest = "d marks it done · " + oneLine(it.Summary)
			}
			needs = append(needs, r)
		}

		var coord *proto.SessionInfo
		var members []proto.SessionInfo
		byID := map[string]proto.SessionInfo{}
		threadOf := map[string]bool{}
		for _, t := range p.Threads {
			threadOf[t.ID] = true
		}
		for i, s := range d.Sessions {
			byID[s.ID] = s
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
		state, rest := "—", "enter starts the coordinator"
		if coord != nil {
			state, rest = stateWord(*coord), progress(*coord)
		}
		if p.Err != "" {
			rest = "error: " + oneLine(p.Err)
		}
		if p.Unread > 0 {
			rest = strings.TrimSpace(rest + fmt.Sprintf("  %d inbox", p.Unread))
		}
		r := row{key: "p:" + p.Slug, project: p.Slug, mark: markTop, who: p.Slug, what: "coordinator", state: state, rest: rest}
		if coord != nil {
			r.session = coord.ID
		}
		projs = append(projs, r)
		threads := append([]ThreadRow(nil), p.Threads...)
		sort.SliceStable(threads, func(i, j int) bool {
			return threadGroup(threads[i], byID) < threadGroup(threads[j], byID)
		})
		for i := range threads {
			t := &threads[i]
			tr := row{key: "th:" + p.Slug + ":" + t.ID, project: p.Slug, thread: t, mark: markNested}
			state := t.State
			if s, ok := byID[t.Session]; ok && t.Session != "" {
				tr.session, state = s.ID, stateWord(s)
				if s.State == "blocked" && s.Reason != "" {
					state += " " + s.Reason
				}
			}
			what := t.ID + " " + oneLine(t.Title)
			rest := threadProgress(t.Status)
			if t.Task != "" {
				rest = joinSp(t.Task, rest)
			}
			if t.Status != nil && !t.Status.Updated.IsZero() {
				rest = joinSp(rest, age(now.Sub(t.Status.Updated)))
			}
			switch {
			case t.ReportState() == "new" && t.Done:
				rest = joinSp(rest, "ready for review")
			case t.ReportState() == "new":
				rest = joinSp(rest, "report waiting")
			}
			if t.Report != nil && t.Report.PR != "" {
				rest = joinSp(rest, "PR "+prRef(t.Report.PR))
			}
			tr.what, tr.state, tr.rest = what, state, rest
			projs = append(projs, tr)
			ask := row{key: "nt:" + p.Slug + ":" + t.ID, project: p.Slug, session: tr.session, thread: t,
				mark: markAsk, who: p.Slug, what: what}
			switch {
			case t.ReportState() == "new":
				ask.state, ask.rest = "report", "unacknowledged report · a acks"
				needs = append(needs, ask)
			case t.Status != nil && t.Status.NeedsYou != "":
				ask.state, ask.rest = "waiting", oneLine(t.Status.NeedsYou)
				needs = append(needs, ask)
			}
		}
		sort.SliceStable(members, func(i, j int) bool { return members[i].Thread < members[j].Thread })
		for _, s := range members {
			projs = append(projs, row{key: "s:" + s.ID, session: s.ID, project: p.Slug,
				mark: markNested, who: s.ID, what: sessionName(s), state: stateWord(s), rest: joinSp(progress(s), age(now.Sub(s.Created)))})
		}
		c := p.Counts
		projs = append(projs, row{note: fmt.Sprintf("    tasks: %d needs you · %d in motion · %d on deck", c["needs_you"], c["in_motion"], c["on_deck"])})
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
		other = append(other, row{key: "s:" + s.ID, session: s.ID,
			mark: markTop, who: s.ID, what: sessionName(s), state: stateWord(s), rest: joinSp(progress(s), age(now.Sub(s.Created)), where)})
	}

	var rows []row
	if len(needs) > 0 {
		rows = append(rows, row{head: "NEEDS YOU"})
		rows = append(rows, needs...)
	}
	rows = append(rows, row{head: "PROJECTS"})
	if len(projs) == 0 {
		projs = []row{{note: "  no projects; n creates one"}}
	}
	rows = append(rows, projs...)
	rows = append(rows, row{head: "SESSIONS"})
	if len(other) == 0 {
		other = []row{{note: "  no sessions; s starts a shell, c an agent"}}
	}
	return append(rows, other...)
}

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
	if i := strings.LastIndex(url, "/pull/"); i >= 0 {
		return "#" + oneLine(url[i+len("/pull/"):])
	}
	return fit(oneLine(url), 30)
}

// threadDetail is what shows under a selected thread row (§4): its full
// todo list, its task's steps and its report's ## Next lines.
func threadDetail(t *ThreadRow) []string {
	const ind = "        "
	var out []string
	if t.Status != nil && len(t.Status.Todos) > 0 {
		out = append(out, ind+"todos:")
		for _, td := range t.Status.Todos {
			mark := map[string]string{"completed": "x", "in_progress": "~"}[string(td.Status)]
			if mark == "" {
				mark = " "
			}
			out = append(out, ind+"  ["+mark+"] "+oneLine(td.Text))
		}
	}
	if t.TaskRec != nil && len(t.TaskRec.Steps) > 0 {
		out = append(out, ind+t.TaskRec.Ref()+" steps:")
		for _, st := range t.TaskRec.Steps {
			box := "[ ]"
			if st.Done {
				box = "[x]"
			}
			out = append(out, fmt.Sprintf("%s  %s %d %s", ind, box, st.N, oneLine(st.Text)))
		}
	}
	if t.Report != nil && len(t.Report.Next) > 0 {
		head := fmt.Sprintf("report %d (%s) next:", t.Reports, t.ReportState())
		if t.ReportState() == "new" {
			head += "  a acks it"
		}
		out = append(out, ind+head)
		for i, n := range t.Report.Next {
			if i == 9 {
				break
			}
			out = append(out, fmt.Sprintf("%s  %d %s", ind, i+1, oneLine(n)))
		}
		out = append(out, ind+"  1-9 sends that line to the thread")
	}
	if len(out) == 0 {
		out = append(out, ind+"no todos, steps or report yet")
	}
	return out
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
