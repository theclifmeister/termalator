package tui

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/theclifmeister/termalator/internal/proto"
	"github.com/theclifmeister/termalator/internal/tasks"
	"github.com/theclifmeister/termalator/internal/thread"
)

// The details panel: everything about the selected row, beside the list
// in a wide window (docs/SPEC.md §4). Narrower windows show a thread's
// details under its row instead.

// details is the panel for r, w cells wide, styled.
func (m *dash) details(r row, w int) []string {
	d := &panel{w: w}
	switch {
	case r.thread != nil:
		m.threadPanel(d, r)
	case strings.HasPrefix(r.key, "p:"):
		m.projectPanel(d, r)
	case r.session != "":
		if s, ok := m.session(r.session); ok {
			sessionPanel(d, s)
		}
	}
	return d.lines
}

func (m *dash) session(id string) (proto.SessionInfo, bool) {
	for _, s := range m.data.Sessions {
		if s.ID == id {
			return s, true
		}
	}
	return proto.SessionInfo{}, false
}

// panel collects a details panel's lines.
type panel struct {
	w     int
	lines []string
}

func (d *panel) add(s string) { d.lines = append(d.lines, fit(" "+s, d.w)+reset) }

func (d *panel) gap() {
	if len(d.lines) > 0 && d.lines[len(d.lines)-1] != "" {
		d.lines = append(d.lines, "")
	}
}

// title is the panel's first line, bold, and the state under it.
func (d *panel) title(text, state string) {
	d.add(styleHead.Render(oneLine(text)))
	if state != "" {
		g, st := stateLook(state)
		d.add(st.Render(strings.TrimSpace(g + " " + state)))
	}
	d.add(styleFaint.Render(strings.Repeat("─", max(d.w-2, 0))))
}

// field is a "label  value" line.
func (d *panel) field(label, value string) {
	if value != "" {
		d.add(styleFaint.Render(fmt.Sprintf("%-9s", label)) + " " + value)
	}
}

// wrap adds text wrapped to the panel's width.
func (d *panel) wrap(text string) {
	for _, l := range strings.Split(ansi.Wordwrap(text, max(d.w-2, 10), ""), "\n") {
		d.add(l)
	}
}

// progressLine is "▰▰▰▱▱ 60% 3/5".
func progressLine(pr thread.Progress) string {
	pct, done, total := pr.Percent, pr.Done, pr.Total
	if pct < 0 {
		return ""
	}
	n := strings.Count(bar(pct), "▰")
	s := styleGood.Render(strings.Repeat("▰", n)) + styleFaint.Render(strings.Repeat("▱", 5-n)) + fmt.Sprintf(" %d%%", pct)
	if total > 0 {
		s += fmt.Sprintf(" %d/%d", done, total)
	}
	return s
}

func (m *dash) threadPanel(d *panel, r row) {
	t := r.thread
	state := t.State
	if s, ok := m.session(t.Session); ok && t.Session != "" {
		state = stateWord(s)
		if s.State == "blocked" && s.Reason != "" {
			state += " " + s.Reason
		}
	}
	d.title(t.ID+" "+t.Title, state)
	d.field("project", r.project)
	d.field("task", t.Task)
	d.field("session", t.Session)
	if st := t.Status; st != nil {
		d.field("progress", progressLine(st.Progress()))
		if st.Current != "" {
			d.field("now", "▸ "+oneLine(st.Current))
		} else if st.Activity != "" {
			d.field("now", `"`+oneLine(st.Activity)+`"`)
		}
		if !st.Updated.IsZero() {
			d.field("updated", age(time.Since(st.Updated))+" ago")
		}
		if st.NeedsYou != "" {
			d.field("waiting", styleWarn.Render(oneLine(st.NeedsYou)))
		}
	}
	if t.Report != nil && t.Report.PR != "" {
		d.field("PR", oneLine(t.Report.PR))
	}
	if t.Reports > 0 {
		rs := fmt.Sprintf("%d, %s", t.Reports, t.ReportState())
		if t.ReportState() == "new" {
			rs = styleWarn.Render(rs) + styleFaint.Render(" · for the coordinator")
		}
		d.field("report", rs)
	}
	d.gap()
	for _, l := range threadDetail(t, "") {
		d.add(l)
	}
	d.gap()
	if r.session != "" {
		d.add(styleFaint.Render("enter watches it; the coordinator acts on it"))
	} else {
		d.add(styleFaint.Render("the coordinator acts on it"))
	}
}

// taskPanel shows a task.
func taskPanel(d *panel, t *tasks.Task) {
	d.title(t.Ref()+" "+t.Title, string(t.Status))
	d.field("thread", t.Thread)
	if len(t.Steps) > 0 {
		d.field("steps", progressLine(thread.Progress{Percent: pctOf(t.StepsDone(), len(t.Steps)), Done: t.StepsDone(), Total: len(t.Steps)}))
	}
	if notes := strings.TrimSpace(t.Notes); notes != "" {
		d.gap()
		for _, l := range strings.Split(notes, "\n") {
			d.wrap(oneLine(l))
		}
	}
	if len(t.Steps) > 0 {
		d.gap()
		for _, s := range t.Steps {
			d.add(fmt.Sprintf("%s %d %s", todoGlyph(map[bool]string{true: "done"}[s.Done]), s.N, oneLine(s.Text)))
		}
	}
}

func (m *dash) projectPanel(d *panel, r row) {
	var p *ProjectData
	for i := range m.data.Projects {
		if m.data.Projects[i].Slug == r.project {
			p = &m.data.Projects[i]
		}
	}
	if p == nil {
		return
	}
	name := p.Name
	if name == "" {
		name = p.Slug
	}
	d.title(name, r.state)
	d.field("project", p.Slug)
	if s, ok := m.session(r.session); ok && r.session != "" {
		d.field("session", s.ID)
		d.field("progress", progressLine(sessionProgress(s)))
		if s.Current != "" {
			d.field("now", "▸ "+oneLine(s.Current))
		}
	} else {
		d.field("session", styleFaint.Render("no coordinator; enter starts it"))
	}
	c := p.Counts
	d.field("tasks", fmt.Sprintf("%d needs you · %d in motion · %d on deck", c["needs_you"], c["in_motion"], c["on_deck"]))
	d.field("threads", fmt.Sprint(len(p.Threads)))
	if p.Unread > 0 {
		d.field("inbox", styleWarn.Render(fmt.Sprintf("%d unhandled", p.Unread))+styleFaint.Render(" · i shows them"))
	}
	if p.Err != "" {
		d.gap()
		d.wrap(styleBad.Render("error: " + oneLine(p.Err)))
	}
	d.gap()
	d.add(styleFaint.Render("enter opens the coordinator · t tasks · i inbox"))
}

func sessionPanel(d *panel, s proto.SessionInfo) {
	state := stateWord(s)
	if s.State == "blocked" && s.Reason != "" {
		state += " " + s.Reason
	}
	d.title(s.ID+" "+sessionName(s), state)
	d.field("project", s.Project)
	d.field("role", string(s.Role))
	d.field("agent", s.Agent)
	d.field("progress", progressLine(sessionProgress(s)))
	if s.Current != "" {
		d.field("now", "▸ "+oneLine(s.Current))
	}
	if s.Queued > 0 {
		d.field("queued", fmt.Sprintf("%d prompt%s", s.Queued, map[bool]string{true: "s"}[s.Queued != 1]))
	}
	where := s.Cwd
	if home, _ := os.UserHomeDir(); home != "" && strings.HasPrefix(where, home) {
		where = "~" + where[len(home):]
	}
	d.field("dir", oneLine(where))
	if len(s.Argv) > 0 {
		d.field("command", oneLine(strings.Join(s.Argv, " ")))
	}
	if !s.Created.IsZero() {
		d.field("started", age(time.Since(s.Created))+" ago")
	}
	d.gap()
	d.add(styleFaint.Render("enter attaches"))
}
