package thread

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/theclifmeister/terminatr/internal/project"
)

// TestArchive: a resolved thread's folder becomes a tarball with an index
// line; LoadArchived reads it back, its id isn't reused, and a thread
// that isn't resolved is refused.
func TestArchive(t *testing.T) {
	t.Setenv("TERMINATR_HOME", t.TempDir())
	p, err := project.New(project.Options{Name: "demo"})
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	r, _ := Create(p, Record{Title: "Fix the\nlogin", Task: "T4", State: Resolved, ResolvedAt: at, Reports: 1}) // t-0001
	os.WriteFile(Path(p, r.ID, "REPORT.md"), []byte("## Report\nok\n\n## Next\nnothing\n"), 0o644)
	os.MkdirAll(Path(p, r.ID, "library"), 0o755)
	os.WriteFile(Path(p, r.ID, "library", "shot.png"), []byte("png"), 0o644)
	os.WriteFile(Path(p, r.ID, ".REPORT.md.lock"), nil, 0o600)
	open, _ := Create(p, Record{Title: "open", State: Running}) // t-0002

	if err := Archive(p, open); err == nil {
		t.Fatal("archived an open thread")
	}
	if err := Archive(p, r); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(Dir(p, r.ID)); !os.IsNotExist(err) {
		t.Fatalf("folder still there: %v", err)
	}
	if _, err := Load(p, r.ID); err == nil {
		t.Fatal("Load found an archived thread")
	}
	got, err := LoadArchived(p, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Record.Task != "T4" || !got.Record.ResolvedAt.Equal(at) || string(got.Files["library/shot.png"]) != "png" ||
		!strings.Contains(string(got.Files["REPORT.md"]), "## Next") {
		t.Fatalf("archived thread: %+v %v", got.Record, got.Files)
	}
	if _, ok := got.Files[".REPORT.md.lock"]; ok {
		t.Error("lock file archived")
	}
	list, _ := ListArchived(p)
	if len(list) != 1 || list[0].ID != "t-0001" || list[0].Title != "Fix the login" || list[0].Resolved != "2026-09-01" {
		t.Fatalf("index: %+v", list)
	}
	if l := list[0].Line(); !strings.HasPrefix(l, "T4 (t-0001) Fix the login  archived (resolved 2026-09-01") {
		t.Errorf("line %q", l)
	}
	// The archived id stays taken, even once the open one is gone too.
	os.RemoveAll(Dir(p, open.ID))
	n, _ := Create(p, Record{Title: "new", State: Running})
	if n.ID != "t-0002" {
		// t-0002's folder is gone and nothing archived it: free again.
		t.Fatalf("next id %s", n.ID)
	}
	os.RemoveAll(Dir(p, n.ID))
	os.Remove(archivePath(p, "t-0001"))
	os.WriteFile(archivePath(p, "t-0007"), nil, 0o644)
	if n, _ := Create(p, Record{Title: "new", State: Running}); n.ID != "t-0008" {
		t.Fatalf("next id %s, want t-0008 after archived t-0007", n.ID)
	}
	// A task whose threads are all archived names them.
	if _, err := ByRef(p, "T4"); err == nil || !strings.Contains(err.Error(), "its resolved ones: t-0001") {
		t.Fatalf("ByRef(T4): %v", err)
	}
}
