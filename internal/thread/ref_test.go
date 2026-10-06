package thread

import (
	"strings"
	"testing"

	"github.com/theclifmeister/terminatr/internal/caller"
	"github.com/theclifmeister/terminatr/internal/project"
	"github.com/theclifmeister/terminatr/internal/tasks"
)

// TestByRef: a task id names the task's open thread; a thread id stays
// as it is; several open threads, or only resolved ones, are refused
// with the list.
func TestByRef(t *testing.T) {
	t.Setenv("TERMINATR_HOME", t.TempDir())
	p, err := project.New(project.Options{Name: "demo"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Tasks().Add(caller.Caller{Kind: caller.Human}, []tasks.NewTask{{Title: "a"}, {Title: "b"}, {Title: "c"}}); err != nil {
		t.Fatal(err)
	}
	Create(p, Record{Title: "a old", Task: "T1", State: Resolved}) // t-0001
	Create(p, Record{Title: "a", Task: "T1", State: Running})      // t-0002
	Create(p, Record{Title: "b", Task: "T2", State: Running})      // t-0003
	Create(p, Record{Title: "b too", Task: "T2", State: Stopped})  // t-0004
	Create(p, Record{Title: "c", Task: "T3", State: Resolved})     // t-0005
	for ref, want := range map[string]string{"T1": "t-0002", "t1": "t-0002", "t-0001": "t-0001", "t-0099": "t-0099"} {
		if got, err := ByRef(p, ref); err != nil || got != want {
			t.Errorf("ByRef(%s) = %q, %v; want %q", ref, got, err, want)
		}
	}
	for ref, want := range map[string]string{
		"T2": "T2 has 2 open threads: t-0003, t-0004",
		"T3": "T3 has no open thread; its resolved ones: t-0005",
		"T9": "T9 has no thread",
	} {
		if _, err := ByRef(p, ref); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("ByRef(%s): %v, want %q", ref, err, want)
		}
	}
}
