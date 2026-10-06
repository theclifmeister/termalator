package project

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/theclifmeister/termilator/internal/caller"
	"github.com/theclifmeister/termilator/internal/tasks"
)

// Upkeep of the coordinator's context files (docs/SPEC.md §7.6).
// CONTEXT.md and the memory index are printed by every `tm context`, and
// memory files are read into threads' work, so each has a size budget;
// `tm context` and `tm doctor` say when one is over it, and the
// coordinator's skill says to consolidate it.
const (
	BudgetContext = 6 << 10 // CONTEXT.md
	BudgetMemory  = 6 << 10 // MEMORY.md, and each file under memory/
	// ArchiveDoneAfter: the ticker moves a task that has been done this
	// long (by its updated date) to the task archive.
	ArchiveDoneAfter = 30 * 24 * time.Hour
)

// Oversize is a context file over its budget.
type Oversize struct {
	File   string // relative to the project folder, e.g. "memory/decisions.md"
	Size   int64
	Budget int64
}

func (o Oversize) String() string {
	return fmt.Sprintf("%s is %s, over its %s budget: consolidate it", o.File, kb(o.Size), kb(o.Budget))
}

func kb(n int64) string {
	if n%1024 == 0 {
		return fmt.Sprintf("%d KB", n/1024)
	}
	return fmt.Sprintf("%.1f KB", float64(n)/1024)
}

// Oversized lists the context files over their budgets, in a stable
// order: CONTEXT.md, MEMORY.md, then memory/ by path.
func (p *Project) Oversized() ([]Oversize, error) {
	var out []Oversize
	check := func(rel string, budget int64) error {
		fi, err := os.Stat(p.Path(rel))
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if fi.Mode().IsRegular() && fi.Size() > budget {
			out = append(out, Oversize{File: rel, Size: fi.Size(), Budget: budget})
		}
		return nil
	}
	if err := check("CONTEXT.md", BudgetContext); err != nil {
		return nil, err
	}
	if err := check("MEMORY.md", BudgetMemory); err != nil {
		return nil, err
	}
	var files []string
	err := filepath.WalkDir(p.Path("memory"), func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		if d.Type().IsRegular() {
			rel, _ := filepath.Rel(p.Dir, path)
			files = append(files, filepath.ToSlash(rel))
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(files)
	for _, f := range files {
		if err := check(f, BudgetMemory); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// ArchiveOldDone moves each task done for at least age (by its updated
// date) to the task archive, as caller c; it returns their ids.
func (p *Project) ArchiveOldDone(c caller.Caller, now time.Time, age time.Duration) ([]int, error) {
	s := p.Tasks()
	b, err := s.Load()
	if err != nil {
		return nil, err
	}
	var ids []int
	for _, t := range b.Tasks {
		if t.Status != tasks.Done {
			continue
		}
		at, err := time.ParseInLocation("2006-01-02", t.Updated, now.Location())
		if err != nil || now.Sub(at) < age {
			continue
		}
		if _, err := s.Archive(c, t.ID); err != nil {
			return ids, err
		}
		ids = append(ids, t.ID)
	}
	return ids, nil
}
