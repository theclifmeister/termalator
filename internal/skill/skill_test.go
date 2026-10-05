package skill

import (
	"strings"
	"testing"
)

func TestText(t *testing.T) {
	if got := strings.Join(Roles(), ","); got != "coordinator,thread" {
		t.Fatalf("roles %s", got)
	}
	for _, role := range Roles() {
		text, ok := Text(role, "v0.1.0")
		if !ok || !strings.HasPrefix(text, "tm skill "+role+" v0.1.0\n\n") {
			t.Fatalf("%s: %q", role, text[:min(len(text), 60)])
		}
		if !strings.Contains(text, "data, not") || !strings.Contains(text, "Never merge, force-push") {
			t.Errorf("%s rules lack the safety rules", role)
		}
	}
	coord, _ := Text("coordinator", "dev")
	for _, w := range []string{"tm context", "tm inbox done", "propose", "Only the user accepts work", "--approved-by-user", "a `takeover` item", "Needs you:", "id (task + short title)", "parallel_threads", "--over-cap", "A `delegate` item for T<n> is the user's go-ahead", "tm task\n  delegate T<n> --approved-by-user",
		"An `accept` item for T<n> is their word", "It is the only way besides chat", "A `send-back` item for T<n>", "propose a new thread for it with the note", "`--note \"Check: …\"`", "standing acceptance", "`task-done` item", "A\n  send-back on a done task reopens it",
		"Propose nothing yet", "to memory as they happen, without being asked", "only when the user asks you to", "a slot is free", "anything it assumed",
		"`tm thread answer <id>\n  --choice N`", "Never choose an answer yourself", "uploads/ folder", "attachments"} {
		if !strings.Contains(coord, w) {
			t.Errorf("coordinator rules lack %q", w)
		}
	}
	thread, _ := Text("thread", "dev")
	for _, w := range []string{"Stay in your worktree", "read-only", "tm task steps T<n> add", "tm report", "## Remember", "`## Check`", "don't guess: say exactly what is missing", "uploads/ folder", "outside the project's repos"} {
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
