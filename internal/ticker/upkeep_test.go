package ticker

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/theclifmeister/terminatr/internal/thread"
)

// TestUpkeep: the first sweep archives what is past its retention age:
// a thread resolved long ago (not one with work left, nor one resolved
// lately), old handled inbox items and old journal lines; a project's
// own archive_threads_days takes the next hourly pass further.
func TestUpkeep(t *testing.T) {
	r := newRig(t)
	day := 24 * time.Hour
	resolved := func(title string, ago time.Duration) string {
		rec, err := thread.Create(r.p, thread.Record{Title: title, State: thread.Resolved, ResolvedAt: r.now.Add(-ago)})
		if err != nil {
			t.Fatal(err)
		}
		return rec.ID
	}
	old := resolved("old", 40*day)      // t-0002
	held := resolved("held", 40*day)    // t-0003
	recent := resolved("recent", 5*day) // t-0004
	r.tk.o.Kept = func(rec *thread.Record) (string, error) {
		if rec.ID == held {
			return "its worktree is still there", nil
		}
		return "", nil
	}
	it, _ := r.p.AddItem("report", "t-0001", "old item", false)
	r.p.DoneItem(it.ID)
	past := r.now.Add(-40 * day)
	os.Chtimes(r.p.Path("inbox", "done", it.ID+".md"), past, past)
	f, _ := os.OpenFile(r.p.Path("JOURNAL.md"), os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString(past.Format(time.RFC3339) + " human old.line x\n")
	f.Close()

	r.sweep(0)
	for _, id := range []string{held, recent, "t-0001"} {
		if _, err := thread.Load(r.p, id); err != nil {
			t.Errorf("%s archived: %v", id, err)
		}
	}
	if _, err := thread.LoadArchived(r.p, old); err != nil {
		t.Fatalf("%s not archived: %v", old, err)
	}
	if _, err := os.Stat(r.p.Path("inbox", "done", it.ID+".md")); !os.IsNotExist(err) {
		t.Errorf("handled item still loose: %v", err)
	}
	// Bundled by the month in its id (when it was created).
	if _, err := os.Stat(r.p.Path("inbox", "done", it.ID[:4]+"-"+it.ID[4:6]+".tar.gz")); err != nil {
		t.Errorf("no inbox bundle: %v", err)
	}
	if _, err := os.Stat(r.p.Path("journal", past.Format("2006-01")+".md.gz")); err != nil {
		t.Errorf("no journal archive: %v", err)
	}
	j := r.journal()
	if strings.Contains(j, "old.line") || !strings.Contains(j, "ticker thread.archive "+old) {
		t.Errorf("journal:\n%s", j)
	}

	// The project keeps resolved threads 3 days: the next hourly pass
	// archives the recent one too; within the hour nothing runs.
	home := os.Getenv("TERMINATR_HOME")
	os.WriteFile(filepath.Join(home, "config.toml"), []byte("[projects.demo]\narchive_threads_days = 3\n"), 0o600)
	r.sweep(time.Minute)
	if _, err := thread.Load(r.p, recent); err != nil {
		t.Fatalf("archived within the hour: %v", err)
	}
	r.sweep(time.Hour)
	if _, err := thread.LoadArchived(r.p, recent); err != nil {
		t.Fatalf("%s not archived after the setting: %v", recent, err)
	}
	if _, err := thread.Load(r.p, held); err != nil {
		t.Fatalf("%s archived: %v", held, err)
	}
}

// TestKept: a resolved thread stays while its worktree is there or its
// branch has commits no remote has; an adopted one never holds.
func TestKept(t *testing.T) {
	repo, _ := gitFixture(t)
	wt := filepath.Join(t.TempDir(), "wt")
	gitT(t, repo, "worktree", "add", "-q", "-b", "tm/demo/t-0001", wt)
	rec := &thread.Record{Repo: repo, Branch: "tm/demo/t-0001", Worktree: wt}
	if why, err := kept(rec); err != nil || why != "its worktree is still there" {
		t.Fatalf("with worktree: %q, %v", why, err)
	}
	commitT(t, wt, "g", "two\n", "work")
	gitT(t, repo, "worktree", "remove", wt)
	if why, err := kept(rec); err != nil || why != "1 unpushed commit on tm/demo/t-0001" {
		t.Fatalf("unpushed branch: %q, %v", why, err)
	}
	gitT(t, repo, "push", "-q", "origin", "tm/demo/t-0001")
	if why, err := kept(rec); err != nil || why != "" {
		t.Fatalf("pushed branch: %q, %v", why, err)
	}
	gitT(t, repo, "branch", "-q", "-D", "tm/demo/t-0001")
	if why, err := kept(rec); err != nil || why != "" {
		t.Fatalf("no branch: %q, %v", why, err)
	}
	if why, _ := kept(&thread.Record{Worktree: repo, Adopted: true}); why != "" {
		t.Fatalf("adopted: %q", why)
	}
}
