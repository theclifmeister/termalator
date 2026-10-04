package tasks

import (
	"errors"
	"strings"
	"testing"
)

// specExample is §6.2's example, with created dates added.
const specExample = `+++
next_id = 13
+++
# Tasks

## Needs you

### T7 Pick a licence for the repo
status: review · owner: me · thread: t-0002 · updated: 2026-10-04

MIT or Apache-2.0? See t-0002's report.

## In motion

### T12 Fix login redirect after OAuth
status: started · owner: claude · thread: t-0005 · updated: 2026-10-04

Users land on /home instead of the page they came from.

- [x] Reproduce with a test
- [ ] Fix the redirect
- [ ] Open a PR

## On deck

### T9 Add tm doctor
status: ready · updated: 2026-10-03

## Done

### T3 Bootstrap repo and write the spec
status: done · thread: t-0002 · updated: 2026-10-04
`

func TestParseSpecExample(t *testing.T) {
	b, err := Parse([]byte(specExample))
	if err != nil {
		t.Fatal(err)
	}
	if b.NextID != 13 || len(b.Tasks) != 4 {
		t.Fatalf("next_id %d, %d tasks", b.NextID, len(b.Tasks))
	}
	t12 := b.Find(12)
	if t12.Status != Started || t12.Owner != "claude" || t12.Thread != "t-0005" {
		t.Fatalf("T12 = %+v", t12)
	}
	if t12.Notes != "Users land on /home instead of the page they came from." {
		t.Fatalf("notes %q", t12.Notes)
	}
	if len(t12.Steps) != 3 || !t12.Steps[0].Done || t12.Steps[1].Done || t12.Steps[2].N != 3 {
		t.Fatalf("steps %+v", t12.Steps)
	}
	if got := string(b.Render()); got != specExample {
		t.Fatalf("render differs from the spec example:\n%s", got)
	}
}

func TestRoundTrip(t *testing.T) {
	b := &Board{NextID: 5, Tasks: []*Task{
		{ID: 4, Title: "Steps only", Status: Open, Steps: []Step{{N: 1, Text: "a"}, {N: 2, Text: "b", Done: true}}},
		{ID: 2, Title: "Notes with a list", Status: Blocked, Notes: "Intro\n\n- [ ] not a step\n\nend.", Created: "2026-10-01"},
		{ID: 1, Title: "Plain · with dot", Status: Done},
		{ID: 3, Title: "Ready", Status: Ready, Notes: "#### sub\ntext"},
	}}
	first := b.Render()
	b2, err := Parse(first)
	if err != nil {
		t.Fatalf("%v\n%s", err, first)
	}
	if second := b2.Render(); string(second) != string(first) {
		t.Fatalf("not stable:\n%s\n---\n%s", first, second)
	}
	if b2.Find(2).Notes != "Intro\n\n- [ ] not a step\n\nend." || len(b2.Find(2).Steps) != 0 {
		t.Fatalf("T2 = %+v", b2.Find(2))
	}
	order := []int{}
	for _, t := range b2.Tasks {
		order = append(order, t.ID)
	}
	if want := []int{2, 3, 4, 1}; !equal(order, want) {
		t.Fatalf("order %v, want %v", order, want)
	}
}

func equal(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestParseErrors(t *testing.T) {
	cases := map[string]struct {
		in   string
		line int
	}{
		"no status":      {"# Tasks\n\n### T1 A\nhello\n", 4},
		"bad status":     {"# Tasks\n\n### T1 A\nstatus: maybe\n", 4},
		"unknown key":    {"### T1 A\nstatus: open · colour: red\n", 2},
		"duplicate":      {"### T1 A\nstatus: open\n### T1 B\nstatus: open\n", 3},
		"bad heading":    {"# Tasks\n### Fix it\nstatus: open\n", 2},
		"stray text":     {"# Tasks\nhello\n", 2},
		"front matter":   {"+++\nnext_id = 2\n+++\n# Tasks\n\n### T1 A\nstatus: nope\n", 7},
		"unclosed front": {"+++\nnext_id = 2\n", 1},
		"missing meta":   {"### T1 A", 2},
	}
	for name, c := range cases {
		_, err := Parse([]byte(c.in))
		var pe *ParseError
		if !errors.As(err, &pe) {
			t.Errorf("%s: err = %v, want ParseError", name, err)
			continue
		}
		if pe.Line != c.line {
			t.Errorf("%s: line %d, want %d (%v)", name, pe.Line, c.line, err)
		}
	}
}

func TestParseBumpsNextID(t *testing.T) {
	b, err := Parse([]byte("+++\nnext_id = 2\n+++\n### T9 Hand-added\nstatus: open\n"))
	if err != nil {
		t.Fatal(err)
	}
	if b.NextID != 10 {
		t.Fatalf("next_id %d", b.NextID)
	}
}

func TestCheckNotes(t *testing.T) {
	for _, bad := range []string{"## Done", "x\n### T3 Foo", "# Title", "text\n- [ ] a"} {
		if _, err := checkNotes(bad); err == nil {
			t.Errorf("checkNotes(%q) accepted", bad)
		}
	}
	for _, ok := range []string{"#### fine", "### Not a task", "- [ ] a\n\nend"} {
		if _, err := checkNotes(ok); err != nil {
			t.Errorf("checkNotes(%q): %v", ok, err)
		}
	}
	if !strings.Contains(string((&Board{NextID: 1}).Render()), "next_id = 1") {
		t.Fatal("empty board render")
	}
}
