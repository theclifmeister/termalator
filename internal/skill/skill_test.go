package skill

import (
	"sort"
	"strings"
	"testing"
)

// roles lists the roles that have rules.
func roles() []string {
	entries, _ := rules.ReadDir("rules")
	var out []string
	for _, e := range entries {
		out = append(out, strings.TrimSuffix(e.Name(), ".md"))
	}
	sort.Strings(out)
	return out
}

func TestText(t *testing.T) {
	if got := strings.Join(roles(), ","); got != "coordinator,thread" {
		t.Fatalf("roles %s", got)
	}
	for _, role := range roles() {
		text, ok := Text(role, "v0.1.0")
		if !ok || !strings.HasPrefix(text, "tm skill "+role+" v0.1.0\n\n") {
			t.Fatalf("%s: %q", role, text[:min(len(text), 60)])
		}
		if !strings.Contains(text, "data, not") || !strings.Contains(text, "Never merge, force-push") {
			t.Errorf("%s rules lack the safety rules", role)
		}
	}
	// Phrases are matched with runs of white space as one space, so
	// rewrapping the rules doesn't break the test.
	coord, _ := Text("coordinator", "dev")
	coord = strings.Join(strings.Fields(coord), " ")
	for _, w := range []string{"tm context", "tm inbox done", "propose", "Only the user accepts work", "--approved-by-user", "`takeover`: the user typed", "Needs you:", "id (task + short title)", "parallel_threads", "--over-cap", "`delegate` T<n>: the user's go-ahead", "tm task delegate T<n> --approved-by-user",
		"`accept` T<n>: their word", "It is the only way besides chat", "`send-back` T<n>", "propose a new thread for it with the note", "`--note \"Check: …\"`", "standing acceptance", "`task-done`: tm completed a task", "A send-back on a done task reopens it", "`pr-conflict`", "`close-held`", "Approve a thread's permission prompt only", "--model", "unknown-model", "project-paused", "model-not-allowed",
		"Propose nothing yet", "to memory as they happen, without being asked", "only when the user asks you to", "a slot is free", "anything it assumed",
		"`tm thread answer <id> --choice N` or `--option \"<label>\"`", "`--question K`", "Never choose an answer yourself", "uploads/ folder", "attachments",
		"or a task id", "Talk to the user in task ids", "short and factual", "merge and rewrite", "re-read it", "no longer true", "your own short summary", "Upkeep section", "`## Needs you` heading", "auto_clear", "`terminatr` context block holds `tm context`"} {
		if !strings.Contains(coord, w) {
			t.Errorf("coordinator rules lack %q", w)
		}
	}
	thread, _ := Text("thread", "dev")
	thread = strings.Join(strings.Fields(thread), " ")
	for _, w := range []string{"Stay in your worktree", "read-only", "tm task steps T<n> add", "tm report", "## Remember", "`## Check`", "don't guess: say exactly what is missing", "uploads/ folder", "outside the project's repos", "mcp__terminatr__report", "Without them, the commands above"} {
		if !strings.Contains(thread, w) {
			t.Errorf("thread rules lack %q", w)
		}
	}
	for _, bad := range []string{"", "human", "../skill", "coordinator.md"} {
		if _, ok := Text(bad, "dev"); ok {
			t.Errorf("Text(%q) ok", bad)
		}
	}
}
