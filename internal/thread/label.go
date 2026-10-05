package thread

import (
	"strings"
	"unicode"

	"github.com/theclifmeister/termilator/internal/project"
	"github.com/theclifmeister/termilator/internal/tasks"
)

// maxLabelTitle caps the title in a label, in runes.
const maxLabelTitle = 60

// Label names a thread for the coordinator with its task and title, so
// a bare id needs no lookup: "t-0003 (T10 Make needs-you tasks easy to
// find)", or "t-0003 (title)" for a thread without a task. The title
// comes from TASKS.md or thread.toml, which only the coordinator and tm
// write. It falls back to the bare id.
func Label(p *project.Project, id string) string {
	r, err := Load(p, id)
	if err != nil {
		return id
	}
	desc := cleanTitle(r.Title)
	if r.Task != "" {
		ref, title := r.Task, desc
		if n, ok := tasks.ParseRef(r.Task); ok {
			if t, err := p.Tasks().Get(n); err == nil {
				ref, title = t.Ref(), cleanTitle(t.Title)
			}
		}
		desc = strings.TrimSpace(ref + " " + title)
	}
	if desc == "" {
		return id
	}
	return id + " (" + desc + ")"
}

// Labelled replaces the first mention of thread id in text with its
// Label.
func Labelled(p *project.Project, id, text string) string {
	if !ValidID(id) || !strings.Contains(text, id) {
		return text
	}
	return strings.Replace(text, id, Label(p, id), 1)
}

// cleanTitle keeps a title to one short line of printable text.
func cleanTitle(s string) string {
	s = strings.Join(strings.FieldsFunc(s, func(r rune) bool { return unicode.IsSpace(r) || !unicode.IsPrint(r) }), " ")
	if r := []rune(s); len(r) > maxLabelTitle {
		s = strings.TrimSpace(string(r[:maxLabelTitle-1])) + "…"
	}
	return s
}
