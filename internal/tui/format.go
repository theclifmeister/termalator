package tui

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/theclifmeister/terminatr/internal/proto"
	"github.com/theclifmeister/terminatr/internal/thread"
)

// Shared text for the dashboard rows and the attach status bar.

// stateWord is a session's state, "running" for a plain program.
func stateWord(s proto.SessionInfo) string {
	switch {
	case s.State != "":
		return s.State
	case s.Agent != "":
		return "starting"
	}
	return "running"
}

// blockReason is why a blocked session is blocked; "question" says
// "question open" while the server holds the menu (SessionInfo.Question).
func blockReason(s proto.SessionInfo) string {
	if s.Reason == "question" && s.Question != nil {
		return "question open"
	}
	return s.Reason
}

// questionLines are a session's open question menu, as tm thread show
// prints it: each question with its header, then its options by number,
// the last one the menu's free text. Each line is plain; indent says how
// far the options sit in.
func questionLines(q *proto.Question) []string {
	var out []string
	for i, it := range q.Questions {
		l := fmt.Sprintf("%d. %s", i+1, oneLine(it.Question))
		if it.Header != "" {
			l = fmt.Sprintf("%d. [%s] %s", i+1, oneLine(it.Header), oneLine(it.Question))
		}
		if it.MultiSelect {
			l += " (several)"
		}
		if it.Answered {
			out = append(out, l+" → answered: "+oneLine(it.Answer))
			continue
		}
		out = append(out, l)
		for j, o := range it.Options {
			opt := fmt.Sprintf("   %d. %s", j+1, oneLine(o.Label))
			if o.Description != "" {
				opt += " — " + oneLine(o.Description)
			}
			out = append(out, opt)
		}
		out = append(out, fmt.Sprintf("   %d. (the user's own words)", len(it.Options)+1))
	}
	return out
}

// progressOnly is progress without the block's reason, which rows show
// on their own.
func progressOnly(s proto.SessionInfo) string {
	s.Reason = ""
	return progress(s, nil)
}

// progress is the derived percent, done/total and the current todo
// (docs/SPEC.md §7.3): "40% 2/5 ▸ Write SPEC §8", or the block reason.
// For a thread's session, st is its STATUS.md, which the dashboard shows
// too; without it, the session's own todos count.
func progress(s proto.SessionInfo, st *thread.Status) string {
	var parts []string
	if s.State == "blocked" && s.Reason != "" {
		parts = append(parts, s.Reason)
	}
	switch {
	case st != nil:
		if p := threadProgress(st); p != "" {
			parts = append(parts, p)
		}
	default:
		if p := sessionProgress(s).String(); p != "" {
			parts = append(parts, p)
		}
		if s.Current != "" {
			parts = append(parts, "▸ "+oneLine(s.Current))
		}
	}
	if s.Queued > 0 {
		q := fmt.Sprintf("%d queued", s.Queued)
		if s.QueueNote(time.Now()) != "" {
			q += " (held)"
		}
		parts = append(parts, q)
	}
	return strings.Join(parts, " ")
}

// sessionName is what a row calls a session: its role in its project,
// the agent, or the command.
func sessionName(s proto.SessionInfo) string {
	switch {
	case s.Role == proto.RoleCoordinator:
		return "coordinator"
	case s.Role == proto.RoleThread && s.Thread != "":
		return threadName(s.Task, s.Thread)
	case s.Agent != "":
		return s.Agent
	case s.Title != "":
		return oneLine(s.Title)
	case len(s.Argv) > 0:
		return strings.Join(s.Argv, " ")
	}
	return "shell"
}

// threadName is how the UI names a thread (docs/SPEC.md §4): by its
// task's id ("T12"), which is what the user and the coordinator talk
// in; a thread without a task (ad hoc, adopted) by its own id. The
// thread id shows in the details: the info panel, tm thread show.
func threadName(task, id string) string {
	if task != "" {
		return task
	}
	return id
}

// age is a short duration: "4s", "3m", "2h", "5d".
func age(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}

// oneLine drops control characters, so text from agents can't break a row.
func oneLine(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f || r >= 0x80 && r < 0xa0 {
			return ' '
		}
		return r
	}, s)
}

// fit truncates or pads plain text to exactly w cells.
func fit(s string, w int) string {
	if w <= 0 {
		return ""
	}
	s = ansi.Truncate(s, w, "…")
	if n := ansi.StringWidth(s); n < w {
		s += strings.Repeat(" ", w-n)
	}
	return s
}

// pendingHead starts the status bar's list of commands after the prefix.
const pendingHead = " prefix ▸ "

// statusBar is the attach status bar (docs/SPEC.md §4): session,
// project, state, progress, a note on the pane (where: the sidebar's keys,
// a flash) and "prefix+d dashboard", in reverse video. After the
// prefix it lists the commands instead. Hints never show the prefix's
// key, which is configurable: only the help and the settings do. It
// returns the line and its buttons, by column: after the prefix each
// command; else the ≡ menu and "prefix+d dashboard".
func statusBar(s proto.SessionInfo, ts *thread.Status, pending bool, cols int, where string) (string, []hint) {
	// The session, its state, then what the keyboard does when it is
	// away from the pane. A project's session is named by its project
	// and role, its id last: a handle, not news; one of the user's own
	// has only its id and command to go by, id first.
	var parts []string
	id := ""
	if s.Project != "" {
		parts = append(parts, s.Project+" "+sessionName(s))
		id = s.ID
	} else {
		parts = append(parts, s.ID, sessionName(s))
	}
	st := stateWord(s)
	if p := progress(s, ts); p != "" {
		st += " " + p
	}
	parts = append(parts, st)
	if s.RemoteControl {
		parts = append(parts, "remote control on")
	}
	if where != "" {
		parts = append(parts, where)
	}
	if id != "" && where == "" {
		// A note in passing takes the id's place.
		parts = append(parts, id)
	}
	left := " " + strings.Join(parts, " · ")
	if pending {
		left = pendingHead + `d dashboard · q quit · a project · n new · p ] [ projects · i t , ? · { } b sidebar · | info · tab sidebar keys · r remote control`
	}
	right, rh := statusRight(pending)
	w := cols - ansi.StringWidth(right) - 1
	if w < 1 {
		return "\x1b[7m" + fit(left, cols) + "\x1b[27m", nil
	}
	var hs []hint
	if pending {
		hs = hints(strings.TrimPrefix(left, pendingHead), ansi.StringWidth(pendingHead))
	}
	// Only what shows: the left part is cut at w.
	hs = slices.DeleteFunc(hs, func(h hint) bool { return h.x1 > w-1 })
	for _, h := range rh {
		hs = append(hs, hint{h.x0 + w + 1, h.x1 + w + 1, h.key})
	}
	return "\x1b[7m" + fit(left, w) + " " + right + "\x1b[27m", hs
}

// statusRight is the status bar's right end and its buttons: the ≡ menu
// and "prefix+d dashboard", or after the prefix "prefix again sends it".
func statusRight(pending bool) (string, []hint) {
	if pending {
		return "prefix again sends it ", []hint{{0, 21, "prefix"}}
	}
	return menuButton + " menu · prefix+d dashboard ", []hint{{0, 6, "menu"}, {9, 9 + len("prefix+d dashboard"), "d"}}
}

// sessionProgress is a session's progress from its own todos.
func sessionProgress(s proto.SessionInfo) thread.Progress {
	return thread.SessionProgress(s.TodosDone, s.TodosTotal)
}

// threadProgress is a thread's line from STATUS.md (docs/SPEC.md §7.3):
// "60% 3/5 ▸ current", the self-reported activity only without steps or
// todos.
func threadProgress(st *thread.Status) string {
	if st == nil {
		return ""
	}
	var parts []string
	if p := st.Progress().String(); p != "" {
		parts = append(parts, p)
	}
	switch {
	case st.Current != "":
		parts = append(parts, "▸ "+oneLine(st.Current))
	case st.Activity != "":
		parts = append(parts, `"`+oneLine(st.Activity)+`"`)
	}
	return strings.Join(parts, " ")
}
