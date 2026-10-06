package doctor

import (
	"fmt"

	"github.com/theclifmeister/terminatr/internal/project"
)

// Upkeep warns about each project's context files over their size
// budgets (CONTEXT.md, MEMORY.md and memory/, docs/SPEC.md §7.6): they
// are read on every coordinator turn, so the coordinator consolidates
// them. There is no fix: rewriting them is the coordinator's work.
func Upkeep(d Deps) []Check {
	list, err := project.List()
	if err != nil {
		return nil // Leftovers reports an unreadable projects folder
	}
	var out []Check
	for _, s := range list {
		if s.Error != "" {
			continue
		}
		p, err := project.Open(s.Slug)
		if err != nil {
			continue
		}
		over, err := p.Oversized()
		if err != nil {
			out = append(out, Check{Group: "upkeep", Name: s.Slug, Status: Warn, Detail: firstLine(err.Error())})
			continue
		}
		for _, o := range over {
			out = append(out, Check{Group: "upkeep", Name: s.Slug, Status: Warn,
				Detail: fmt.Sprintf("%s (ask the coordinator)", o)})
		}
	}
	if len(out) == 0 && len(list) > 0 {
		out = append(out, Check{Group: "upkeep", Name: "context files", Status: OK,
			Detail: fmt.Sprintf("within budget (CONTEXT.md %d KB, MEMORY.md and each memory file %d KB)", project.BudgetContext>>10, project.BudgetMemory>>10)})
	}
	return out
}
