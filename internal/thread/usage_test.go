package thread

import (
	"testing"

	"github.com/theclifmeister/terminatr/internal/project"
)

func TestUsageTotals(t *testing.T) {
	t.Setenv("TERMINATR_HOME", t.TempDir())
	p, err := project.New(project.Options{Name: "demo"})
	if err != nil {
		t.Fatal(err)
	}
	a, _ := Create(p, Record{Title: "A", Task: "T1", State: Running})
	b, _ := Create(p, Record{Title: "B", Task: "T1", State: Resolved})
	c, _ := Create(p, Record{Title: "C", State: Running})
	turn := Usage{Turns: 1, Input: 10, Output: 20, CacheRead: 300, CacheCreation: 40, CostUSD: 0.25}
	for _, r := range []*Record{a, a, b, c} {
		if _, err := Update(p, r.ID, func(x *Record) error { x.Usage = x.Usage.Add(turn); return nil }); err != nil {
			t.Fatal(err)
		}
	}
	recs, _ := List(p)
	byTask, total := TaskUsage(recs)
	if got := byTask[1]; got.Turns != 3 || got.Output != 60 || got.CostUSD != 0.75 {
		t.Fatalf("T1: %+v", got)
	}
	if len(byTask) != 1 || total.Turns != 4 || total.Tokens() != 4*370 || total.CostUSD != 1 {
		t.Fatalf("by task %v total %+v", byTask, total)
	}
	if got := total.String(); got != "1.5k tokens, $1.00" {
		t.Fatalf("String: %q", got)
	}
	if got := (Usage{}).String(); got != "" {
		t.Fatalf("zero: %q", got)
	}
	if got := turn.Detail(); got != "370 tokens (10 in, 20 out, 300 cache read, 40 cache write), $0.25, 1 turn" {
		t.Fatalf("Detail: %q", got)
	}
}
