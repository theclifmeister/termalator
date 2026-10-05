package tui

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/theclifmeister/termilator/internal/proto"
	"github.com/theclifmeister/termilator/internal/thread"
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
		parts = append(parts, fmt.Sprintf("%d queued", s.Queued))
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
		return s.Thread
	case s.Agent != "":
		return s.Agent
	case s.Title != "":
		return oneLine(s.Title)
	case len(s.Argv) > 0:
		return strings.Join(s.Argv, " ")
	}
	return "shell"
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

// statusLine is the attach status bar (docs/SPEC.md §4): session,
// project, state, progress, a note on the pane (where: the sidebar's keys,
// a flash) and "prefix+d dashboard", in reverse video. After the
// prefix it lists the commands instead. Hints never show the prefix's
// key, which is configurable: only the help and the settings do.
func statusLine(s proto.SessionInfo, ts *thread.Status, pending bool, cols int, where string) string {
	line, _ := statusBar(s, ts, pending, cols, where)
	return line
}

// pendingHead starts the status bar's list of commands after the prefix.
const pendingHead = " prefix ▸ "

// statusBar is statusLine with its buttons, by column: after the prefix
// each command; else the ≡ menu and "prefix+d dashboard".
func statusBar(s proto.SessionInfo, ts *thread.Status, pending bool, cols int, where string) (string, []hint) {
	parts := []string{" " + s.ID}
	if s.Project != "" {
		parts = append(parts, s.Project+" "+sessionName(s))
	} else {
		parts = append(parts, sessionName(s))
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
	left := strings.Join(parts, " · ")
	if pending {
		left = pendingHead + `d dashboard · q quit · a project · p ] [ projects · i t , ? · { } b sidebar · | info · tab sidebar keys · r remote control`
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
	return menuButton + "  prefix+d dashboard ", []hint{{0, 2, "menu"}, {3, 3 + len("prefix+d dashboard"), "d"}}
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
