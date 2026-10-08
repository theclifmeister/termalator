package thread

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/theclifmeister/terminatr/internal/caller"
	"github.com/theclifmeister/terminatr/internal/project"
)

func libFixture(t *testing.T) *project.Project {
	t.Helper()
	t.Setenv("TERMINATR_HOME", t.TempDir())
	p, err := project.New(project.Options{Slug: "demo"})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func addLib(t *testing.T, p *project.Project, id, name, text string, at time.Time) {
	t.Helper()
	os.MkdirAll(Path(p, id, "library"), 0o755)
	f := Path(p, id, "library", name)
	if err := os.WriteFile(f, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	os.Chtimes(f, at, at)
}

// TestLibrary: every thread's files are listed, an archived (resolved)
// thread's from its tarball, newest first; a file reads, truncated at
// the cap; a name with a path is refused.
func TestLibrary(t *testing.T) {
	p := libFixture(t)
	day := func(d int) time.Time { return time.Date(2026, 10, d, 12, 0, 0, 0, time.UTC) }
	a, _ := Create(p, Record{Title: "Old", Task: "T1", State: Resolved, ResolvedAt: day(1)})
	b, _ := Create(p, Record{Title: "New", Task: "T2", State: Running})
	addLib(t, p, a.ID, "notes.md", "# Notes\n", day(2))
	addLib(t, p, a.ID, "data.json", `{"a":1}`, day(3))
	addLib(t, p, b.ID, "shot.png", "png", day(5))
	if err := Archive(p, a); err != nil {
		t.Fatal(err)
	}
	files, err := Library(p)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, f := range files {
		got = append(got, f.Thread+"/"+f.Name+" "+f.State)
	}
	want := "t-0002/shot.png running,t-0001/data.json archived,t-0001/notes.md archived"
	if strings.Join(got, ",") != want {
		t.Fatalf("library %v, want %s", got, want)
	}
	if files[1].Task != "T1" || files[1].Title != "Old" || files[1].Size != 7 {
		t.Errorf("archived file: %+v", files[1])
	}
	data, trunc, err := ReadLibraryFile(p, a.ID, "data.json", 100)
	if err != nil || trunc || string(data) != `{"a":1}` {
		t.Fatalf("read archived: %q %v %v", data, trunc, err)
	}
	if data, trunc, _ := ReadLibraryFile(p, b.ID, "shot.png", 2); !trunc || string(data) != "pn" {
		t.Fatalf("read capped: %q %v", data, trunc)
	}
	for _, bad := range []string{"../thread.toml", "a/b", ".hidden", ""} {
		if _, _, err := ReadLibraryFile(p, b.ID, bad, 10); err == nil {
			t.Errorf("read %q", bad)
		}
	}
	if _, _, err := ReadLibraryFile(p, b.ID, "gone.txt", 10); err == nil {
		t.Error("read a missing file")
	}
}

// TestRemoveLibrary: a live thread's file, an archived thread's file and
// then all of a thread's files go, each journaled; a missing file is
// refused and nothing else is touched.
func TestRemoveLibrary(t *testing.T) {
	p := libFixture(t)
	at := time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC)
	live, _ := Create(p, Record{Title: "Live", State: Running})
	arch, _ := Create(p, Record{Title: "Arch", State: Resolved, ResolvedAt: at})
	addLib(t, p, live.ID, "a.txt", "a", at)
	addLib(t, p, live.ID, "b.txt", "b", at)
	addLib(t, p, arch.ID, "c.txt", "c", at)
	addLib(t, p, arch.ID, "d.txt", "d", at)
	if err := Archive(p, arch); err != nil {
		t.Fatal(err)
	}
	c := caller.Caller{Kind: caller.Human}

	if n, err := RemoveLibrary(p, c, live.ID, "a.txt"); n != 1 || err != nil {
		t.Fatalf("rm live: %d %v", n, err)
	}
	if n, err := RemoveLibrary(p, c, live.ID, "a.txt"); n != 0 || err == nil {
		t.Fatalf("rm again: %d %v", n, err)
	}
	if n, err := RemoveLibrary(p, c, arch.ID, "c.txt"); n != 1 || err != nil {
		t.Fatalf("rm archived: %d %v", n, err)
	}
	files, _ := Library(p)
	if len(files) != 2 {
		t.Fatalf("after two: %+v", files)
	}
	// The archive still loads, with the rest of the thread in it.
	if at, err := LoadArchived(p, arch.ID); err != nil || string(at.Files["library/d.txt"]) != "d" || at.Files["library/c.txt"] != nil {
		t.Fatalf("archive after rm: %v %v", err, at)
	}
	if n, err := RemoveLibrary(p, c, arch.ID, ""); n != 1 || err != nil {
		t.Fatalf("rm all archived: %d %v", n, err)
	}
	if n, err := RemoveLibrary(p, c, live.ID, ""); n != 1 || err != nil {
		t.Fatalf("rm all live: %d %v", n, err)
	}
	if files, _ := Library(p); len(files) != 0 {
		t.Fatalf("left: %+v", files)
	}
	if _, err := os.Stat(Dir(p, live.ID)); err != nil {
		t.Errorf("the thread's folder went: %v", err)
	}
	j, _ := os.ReadFile(p.Path("JOURNAL.md"))
	for _, want := range []string{"library.rm t-0001 a.txt", "library.rm t-0002 c.txt", "library.rm t-0002 d.txt", "library.rm t-0001 b.txt"} {
		if !strings.Contains(string(j), want) {
			t.Errorf("journal lacks %q:\n%s", want, j)
		}
	}
	if _, err := RemoveLibrary(p, c, live.ID, "../x"); err == nil {
		t.Error("rm with a path")
	}
}
