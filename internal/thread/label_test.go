package thread

import (
	"strings"
	"testing"

	"github.com/theclifmeister/termilator/internal/caller"
	"github.com/theclifmeister/termilator/internal/project"
	"github.com/theclifmeister/termilator/internal/tasks"
)

func TestLabel(t *testing.T) {
	t.Setenv("TERMINATR_HOME", t.TempDir())
	p, err := project.New(project.Options{Name: "demo"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Tasks().Add(caller.Caller{Kind: caller.Human}, []tasks.NewTask{{Title: "Make needs-you tasks easy to find"}}); err != nil {
		t.Fatal(err)
	}
	Create(p, Record{Title: "old title", Task: "T1"})
	Create(p, Record{Title: "No task\x1b[31m here"})
	Create(p, Record{Title: "gone", Task: "T9"})
	Create(p, Record{Title: strings.Repeat("x", 100)})
	for id, want := range map[string]string{
		"t-0001": "t-0001 (T1 Make needs-you tasks easy to find)",
		"t-0002": "t-0002 (No task [31m here)",
		"t-0003": "t-0003 (T9 gone)",
		"t-0004": "t-0004 (" + strings.Repeat("x", 59) + "…)",
		"t-0099": "t-0099",
	} {
		if got := Label(p, id); got != want {
			t.Errorf("Label(%s) = %q, want %q", id, got, want)
		}
	}
	if got := Labelled(p, "t-0001", "PR #7 of t-0001: approved (tm thread show t-0001)"); got != "PR #7 of t-0001 (T1 Make needs-you tasks easy to find): approved (tm thread show t-0001)" {
		t.Errorf("Labelled %q", got)
	}
	if got := Labelled(p, "server", "the server restarted"); got != "the server restarted" {
		t.Errorf("Labelled %q", got)
	}
	for ref, want := range map[string]string{"T1": "T1 Make needs-you tasks easy to find", "T9": "T9", "nope": "nope"} {
		if got := TaskLabel(p, ref); got != want {
			t.Errorf("TaskLabel(%s) = %q, want %q", ref, got, want)
		}
	}
}
