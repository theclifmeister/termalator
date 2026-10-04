package tasks

import (
	"strings"
	"testing"
)

// TestLinesAlign: the steps and thread columns start at the same place
// whatever the title's length.
func TestLinesAlign(t *testing.T) {
	ts := []*Task{
		{ID: 1, Status: Started, Title: "Short", Thread: "t-0001", Steps: []Step{{N: 1, Done: true}, {N: 2}}},
		{ID: 12, Status: Review, Title: "A much longer title than the first", Thread: "t-0002", Steps: make([]Step, 10)},
		{ID: 3, Status: Ready, Title: "No thread"},
	}
	lines := Lines(ts)
	col := func(l, s string) int { return strings.Index(l, s) }
	if a, b := col(lines[0], "t-0001"), col(lines[1], "t-0002"); a != b || a < 0 {
		t.Fatalf("threads at %d and %d:\n%s", a, b, strings.Join(lines, "\n"))
	}
	if a, b := col(lines[0], "1/2")+3, col(lines[1], "0/10")+4; a != b {
		t.Fatalf("steps end at %d and %d:\n%s", a, b, strings.Join(lines, "\n"))
	}
	if lines[2] != "T3   ready    No thread" {
		t.Fatalf("trailing columns not trimmed: %q", lines[2])
	}
}
