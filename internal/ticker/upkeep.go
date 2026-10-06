package ticker

import (
	"errors"
	"io/fs"
	"os"
	"time"

	"github.com/theclifmeister/terminatr/internal/caller"
	"github.com/theclifmeister/terminatr/internal/config"
	"github.com/theclifmeister/terminatr/internal/project"
	"github.com/theclifmeister/terminatr/internal/thread"
	"github.com/theclifmeister/terminatr/internal/worktree"
)

// upkeep moves what is old out of a project's working files (docs/SPEC.md
// §7.6), once an hour and on the first sweep after a start, by the
// project's retention settings: done tasks to the task archive, resolved
// threads' folders to threads/archive/, handled inbox items and journal
// lines to compressed monthly files. Nothing is deleted, and a thread
// with work left in its worktree or branch stays as it is.
func (t *Ticker) upkeep(p *project.Project, safety config.Safety, now time.Time) {
	days := func(n int) time.Duration { return time.Duration(n) * t.o.Day }
	// Journaled as the ticker's task.archive.
	if ids, err := p.ArchiveOldDone(caller.Caller{Kind: caller.Ticker}, now, days(safety.ArchiveTasksDays)); err != nil {
		t.o.Log.Printf("ticker: %s: archive done tasks: %v", p.Slug, err)
	} else if len(ids) > 0 {
		t.o.Log.Printf("ticker: %s: archived %d done tasks", p.Slug, len(ids))
	}
	t.archiveThreads(p, now, days(safety.ArchiveThreadsDays))
	if n, err := p.ArchiveInbox(now, days(safety.ArchiveInboxDays)); err != nil {
		t.o.Log.Printf("ticker: %s: archive inbox: %v", p.Slug, err)
	} else if n > 0 {
		t.o.Log.Printf("ticker: %s: bundled %d handled inbox items into inbox/done/", p.Slug, n)
	}
	// Last, so the lines the steps above journaled stay in JOURNAL.md.
	if n, err := p.ArchiveJournal(now, days(safety.ArchiveJournalDays)); err != nil {
		t.o.Log.Printf("ticker: %s: archive journal: %v", p.Slug, err)
	} else if n > 0 {
		t.o.Log.Printf("ticker: %s: moved %d journal lines to journal/", p.Slug, n)
	}
}

// archiveThreads packs each thread resolved at least age ago into
// threads/archive/, unless Kept gives a reason to leave it; journaled as
// the ticker's thread.archive.
func (t *Ticker) archiveThreads(p *project.Project, now time.Time, age time.Duration) {
	recs, err := thread.List(p)
	if err != nil {
		t.o.Log.Printf("ticker: %s: archive threads: %v", p.Slug, err)
		return
	}
	n := 0
	for _, r := range recs {
		if r.State != thread.Resolved {
			continue
		}
		if at := thread.ResolvedAt(p, r); at.IsZero() || now.Sub(at) < age {
			continue
		}
		why, err := t.o.Kept(r)
		if err != nil {
			t.o.Log.Printf("ticker: %s: archive %s: %v", p.Slug, r.ID, err)
			continue
		}
		if why != "" {
			continue // looked at again next time; resolve already said so
		}
		if err := thread.Archive(p, r); err != nil {
			t.o.Log.Printf("ticker: %s: archive %s: %v", p.Slug, r.ID, err)
			continue
		}
		if err := p.Journal(caller.Caller{Kind: caller.Ticker}, "thread.archive", r.ID, ""); err != nil {
			t.o.Log.Printf("ticker: %s: %v", p.Slug, err)
		}
		n++
	}
	if n > 0 {
		t.o.Log.Printf("ticker: %s: archived %d resolved threads", p.Slug, n)
	}
}

// kept says why a resolved thread's folder stays: its worktree is still
// there (resolve keeps one with uncommitted changes, or one it couldn't
// remove), or its branch has commits nowhere else. An adopted thread's
// checkout or folder is the user's own and holds nothing of tm's.
func kept(r *thread.Record) (string, error) {
	if r.Adopted || r.Checkout {
		return "", nil
	}
	if r.Worktree != "" {
		if _, err := os.Stat(r.Worktree); err == nil {
			return "its worktree is still there", nil
		} else if !errors.Is(err, fs.ErrNotExist) {
			return "", err
		}
	}
	return worktree.BranchUnsaved(r.Repo, r.Branch)
}
