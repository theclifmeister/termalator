package tasks

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// maxTitle caps the title column; longer titles end in "…".
const maxTitle = 60

// Lines are the tasks' one-line forms ("T12  started  Fix login redirect
// 2/3  t-0005  owner:claude") with their columns aligned, for
// `tm task list` and `tm context`: id, status, title, steps, thread,
// owner. A column no task has takes no room.
func Lines(ts []*Task) []string {
	// The id and status keep their old minimum widths, so a short list
	// reads as it always did.
	wRef, wStatus := 3, 7
	var wTitle, wSteps, wThread int
	steps := make([]string, len(ts))
	for i, t := range ts {
		wRef = max(wRef, width(t.Ref()))
		wStatus = max(wStatus, width(string(t.Status)))
		wTitle = max(wTitle, min(width(t.Title), maxTitle))
		if len(t.Steps) > 0 {
			steps[i] = fmt.Sprintf("%d/%d", t.StepsDone(), len(t.Steps))
		}
		wSteps = max(wSteps, width(steps[i]))
		wThread = max(wThread, width(t.Thread))
	}
	out := make([]string, len(ts))
	for i, t := range ts {
		cols := []string{pad(t.Ref(), wRef), pad(string(t.Status), wStatus), pad(clip(t.Title, maxTitle), wTitle)}
		if wSteps > 0 {
			cols = append(cols, strings.Repeat(" ", wSteps-width(steps[i]))+steps[i])
		}
		if wThread > 0 {
			cols = append(cols, pad(t.Thread, wThread))
		}
		if t.Owner != "" {
			cols = append(cols, "owner:"+t.Owner)
		}
		if t.Archived {
			cols = append(cols, "(archived)")
		}
		out[i] = strings.TrimRight(strings.Join(cols, "  "), " ")
	}
	return out
}

func width(s string) int { return utf8.RuneCountInString(s) }

func pad(s string, w int) string { return s + strings.Repeat(" ", max(w-width(s), 0)) }

// clip cuts s to w runes, the last one "…".
func clip(s string, w int) string {
	if width(s) <= w {
		return s
	}
	return string([]rune(s)[:w-1]) + "…"
}

// Detail is a task in full, for `tm task show`.
func Detail(t *Task) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s %s\n", t.Ref(), t.Title)
	meta := []string{"status: " + string(t.Status)}
	for _, kv := range [][2]string{{"owner", t.Owner}, {"thread", t.Thread}, {"created", t.Created}, {"updated", t.Updated}} {
		if kv[1] != "" {
			meta = append(meta, kv[0]+": "+kv[1])
		}
	}
	if t.Archived {
		meta = append(meta, "archived")
	}
	b.WriteString(strings.Join(meta, sep) + "\n")
	if t.Notes != "" {
		b.WriteString("\n" + t.Notes + "\n")
	}
	if len(t.Steps) > 0 {
		fmt.Fprintf(&b, "\nSteps (%d/%d):\n", t.StepsDone(), len(t.Steps))
		for _, s := range t.Steps {
			mark := " "
			if s.Done {
				mark = "x"
			}
			fmt.Fprintf(&b, "%3d. [%s] %s\n", s.N, mark, s.Text)
		}
	}
	return b.String()
}

// JSON is the stable --json shape of a task (§6.3).
type JSON struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Status   Status `json:"status"`
	Group    string `json:"group"`
	Notes    string `json:"notes"`
	Steps    []Step `json:"steps"`
	Thread   string `json:"thread"`
	Owner    string `json:"owner"`
	Created  string `json:"created"`
	Updated  string `json:"updated"`
	Archived bool   `json:"archived,omitempty"`
}

// ToJSON converts a task to its --json shape.
func ToJSON(t *Task) JSON {
	steps := t.Steps
	if steps == nil {
		steps = []Step{}
	}
	return JSON{
		ID: t.Ref(), Title: t.Title, Status: t.Status, Group: string(GroupOf(t.Status)),
		Notes: t.Notes, Steps: steps, Thread: t.Thread, Owner: t.Owner,
		Created: t.Created, Updated: t.Updated, Archived: t.Archived,
	}
}
