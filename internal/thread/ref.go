package thread

import (
	"regexp"
	"sort"
	"strings"

	"github.com/theclifmeister/terminatr/internal/project"
	"github.com/theclifmeister/terminatr/internal/tasks"
)

var taskRefRE = regexp.MustCompile(`^[Tt][0-9]+$`)

// ByRef is the thread id that ref names (docs/SPEC.md §9): a thread id
// ("t-0003") as is, or a task id ("T12") resolved to that task's open
// (unresolved) thread. A task with several open threads, or none, is
// refused with a list of its threads, which stay reachable by their
// ids.
func ByRef(p *project.Project, ref string) (string, error) {
	if !taskRefRE.MatchString(ref) {
		return ref, nil // a thread id, or a refusal from Load
	}
	n, ok := tasks.ParseRef(ref)
	if !ok {
		return "", refuse("unknown-thread", "%q is neither a thread id like t-0003 nor a task id like T12", ref)
	}
	recs, err := List(p)
	if err != nil {
		return "", err
	}
	var open, closed []string
	for _, r := range recs {
		if r.TaskID() != n {
			continue
		}
		if r.State == Resolved {
			closed = append(closed, r.ID)
		} else {
			open = append(open, r.ID)
		}
	}
	task := (&tasks.Task{ID: n}).Ref()
	if archived, _ := ListArchived(p); len(open) == 0 {
		for _, a := range archived {
			if id, _ := tasks.ParseRef(a.Task); id == n {
				closed = append(closed, a.ID)
			}
		}
		sort.Strings(closed)
	}
	switch {
	case len(open) == 1:
		return open[0], nil
	case len(open) > 1:
		return "", refuse("ambiguous-task", "%s has %d open threads: %s; name one by its id", task, len(open), strings.Join(open, ", "))
	case len(closed) > 0:
		return "", refuse("no-open-thread", "%s has no open thread; its resolved ones: %s (name one by its id)", task, strings.Join(closed, ", "))
	}
	return "", refuse("unknown-thread", "%s has no thread in project %s (tm thread list)", task, p.Slug)
}
