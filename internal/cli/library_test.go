package cli

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/theclifmeister/terminatr/internal/project"
	"github.com/theclifmeister/terminatr/internal/thread"
)

// TestLibrary: tm library list shows every thread's files by name and
// size (--json too, no path in it), rm deletes one file or all of a
// thread's (journaled), and a thread may do neither.
func TestLibrary(t *testing.T) {
	h := newHarness(t)
	h.ok(human, "project", "new", "demo")
	p, _ := project.Open("demo")
	r, _ := thread.Create(p, thread.Record{Title: "Research", Task: "T5", State: thread.Running})
	os.MkdirAll(thread.Path(p, r.ID, "library"), 0o755)
	os.WriteFile(thread.Path(p, r.ID, "library", "notes.md"), []byte("# Notes\n"), 0o644)
	os.WriteFile(thread.Path(p, r.ID, "library", "more.txt"), []byte("more"), 0o644)

	out := h.ok(coord, "library", "list", "--project", "demo")
	for _, want := range []string{"T5", "t-0001", "notes.md", "8 bytes", "more.txt", "running"} {
		if !strings.Contains(out, want) {
			t.Errorf("list lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "threads/") {
		t.Errorf("list shows a path:\n%s", out)
	}
	var files []thread.LibFile
	if err := json.Unmarshal([]byte(h.ok(human, "library", "list", "--json", "--project", "demo")), &files); err != nil || len(files) != 2 {
		t.Fatalf("json: %v %+v", err, files)
	}

	h.expect(1, "coordinator-only", thr, "library", "list", "--project", "demo")
	h.expect(1, "coordinator-only", thr, "library", "rm", "t-0001", "--all", "--project", "demo")
	h.expect(2, "", coord, "library", "rm", "t-0001", "--project", "demo")
	h.expect(1, "invalid-file", coord, "library", "rm", "t-0001", "../thread.toml", "--project", "demo")
	h.expect(1, "unknown-file", coord, "library", "rm", "t-0001", "nope.txt", "--project", "demo")

	h.ok(coord, "library", "rm", "t-0001", "notes.md", "--project", "demo")
	if out := h.ok(coord, "library", "list", "--project", "demo"); strings.Contains(out, "notes.md") || !strings.Contains(out, "more.txt") {
		t.Fatalf("after rm:\n%s", out)
	}
	h.ok(human, "library", "rm", "t-0001", "--all", "--project", "demo")
	if out := h.ok(coord, "library", "list", "--project", "demo"); out != "" {
		t.Fatalf("after rm --all:\n%s", out)
	}
	j, _ := os.ReadFile(p.Path("JOURNAL.md"))
	for _, want := range []string{"coordinator", "library.rm t-0001 notes.md", "library.rm t-0001 more.txt"} {
		if !strings.Contains(string(j), want) {
			t.Errorf("journal lacks %q:\n%s", want, j)
		}
	}
}
