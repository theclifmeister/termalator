package cli

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/theclifmeister/terminatr/internal/caller"
	"github.com/theclifmeister/terminatr/internal/project"
)

// TestAsk: the coordinator's question store, through tm ask.
func TestAsk(t *testing.T) {
	h := newHarness(t)
	h.ok(human, "project", "new", "demo")
	h.env[caller.EnvProject] = "demo"

	out := h.ok(coord, "ask", "add", "Which repo gets the band?", "--task", "T12",
		"--option", "Main: the terminatr repo", "--option", "Site", "--recommend", "Main")
	if out != "asked Q1\n" {
		t.Fatalf("add %q", out)
	}
	// Asking it again updates the open one.
	if out := h.ok(coord, "ask", "add", "Which repo gets the band?", "--task", "T12", "--option", "Main", "--option", "Site"); out != "asked Q1\n" {
		t.Fatalf("re-add %q", out)
	}
	code, out, errs := h.run(coord, `[{"question":"Ship it?","options":[{"label":"Yes"},{"label":"No","description":"wait"}],"multi_select":true}]`, "ask", "add", "--file", "-")
	if code != 0 || out != "asked Q2\n" {
		t.Fatalf("add --file: exit %d %q %q", code, out, errs)
	}
	h.expect(1, "invalid-question", coord, "ask", "add", "One option?", "--option", "Only")
	h.expect(1, "invalid-question", coord, "ask", "add", "Bad pick?", "--option", "A", "--option", "B", "--recommend", "C")
	h.expect(1, "invalid-question", coord, "ask", "add", "Long chip?", "--header", "far too long a header", "--option", "A", "--option", "B")
	h.expect(1, "invalid-question", coord, "ask", "add", "Task?", "--task", "12", "--option", "A", "--option", "B")
	h.expect(2, "not both", coord, "ask", "add", "--file", "-", "--task", "T1")
	h.expect(1, "coordinator-only", thr, "ask", "list")

	list := h.ok(coord, "ask", "list")
	want := "Q1 T12 [T12] Which repo gets the band? Options: Main; Site\n" +
		"Q2 [Question] Ship it? (multi-select) Options: Yes; No — wait\n"
	if list != want {
		t.Fatalf("list\n%s\nwant\n%s", list, want)
	}
	var qs []project.Question
	if err := json.Unmarshal([]byte(h.ok(human, "ask", "list", "--json")), &qs); err != nil || len(qs) != 2 || !qs[1].MultiSelect {
		t.Fatalf("list --json %v %v", qs, err)
	}

	if out := h.ok(coord, "ask", "done", "q1"); out != "done Q1\n" {
		t.Fatalf("done %q", out)
	}
	h.expect(1, "unknown-question", coord, "ask", "done", "Q1")
	h.expect(1, "invalid-question", coord, "ask", "done", "T1")
	h.ok(coord, "ask", "done", "Q2")
	if out := h.ok(coord, "ask", "list", "--json"); strings.TrimSpace(out) != "[]" {
		t.Fatalf("empty list %q", out)
	}
	// Ids are never reused.
	if out := h.ok(coord, "ask", "add", "Again?", "--option", "A", "--option", "B"); out != "asked Q3\n" {
		t.Fatalf("next id %q", out)
	}
}
