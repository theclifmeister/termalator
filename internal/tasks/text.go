package tasks

import (
	"fmt"
	"strings"
)

// Line is a task's one-line form, used by `tm task list` and `tm context`:
// "T12  started  Fix login redirect   t-0005  owner:claude  2/3".
func Line(t *Task) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%-4s %-8s %s", t.Ref(), t.Status, t.Title)
	if t.Thread != "" {
		b.WriteString("   " + t.Thread)
	}
	if t.Owner != "" {
		b.WriteString("  owner:" + t.Owner)
	}
	if len(t.Steps) > 0 {
		fmt.Fprintf(&b, "  %d/%d", t.StepsDone(), len(t.Steps))
	}
	if t.Archived {
		b.WriteString("  (archived)")
	}
	return b.String()
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
