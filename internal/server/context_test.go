package server

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/theclifmeister/terminatr/internal/home"
	"github.com/theclifmeister/terminatr/internal/proto"
)

func TestContextWindow(t *testing.T) {
	for _, c := range []struct {
		model  string
		tokens int64
		want   int64
	}{
		{"claude-sonnet-5-5", 90_000, windowStandard},
		{"claude-sonnet-5-5[1m]", 90_000, windowLong},
		{"claude-opus-5-5", 250_000, windowLong}, // more than 200k proves the long window
	} {
		if got := contextWindow(c.model, c.tokens); got != c.want {
			t.Errorf("%s %d: %d, want %d", c.model, c.tokens, got, c.want)
		}
	}
}

// TestContextAlertPerCrossing: a coordinator's context rising past
// [ui] context_hint rings once; it rings again only after a fall below.
func TestContextAlertPerCrossing(t *testing.T) {
	h := t.TempDir()
	t.Setenv(home.Env, h)
	s := &Server{records: map[string]SessionRecord{
		"s-1": {ID: "s-1", Role: proto.RoleCoordinator},
		"s-2": {ID: "s-2", Role: proto.RoleThread},
	}}
	steps := []struct {
		id     string
		tokens int64
		alerts uint64
	}{
		{"s-1", 50_000, 0},  // 25%
		{"s-1", 90_000, 1},  // 45%: crossed
		{"s-1", 120_000, 1}, // still above
		{"s-1", 20_000, 1},  // a /clear
		{"s-1", 85_000, 2},  // crossed again
		{"s-2", 150_000, 2}, // a thread's context never rings
	}
	for i, st := range steps {
		s.setContext(st.id, "claude-x", st.tokens)
		if got := s.alerts.Load(); got != st.alerts {
			t.Fatalf("step %d: %d alerts, want %d", i, got, st.alerts)
		}
	}
	// Never.
	os.WriteFile(filepath.Join(h, "config.toml"), []byte("[ui]\ncontext_hint = 0\n"), 0o600)
	s.setContext("s-1", "claude-x", 10_000)
	s.setContext("s-1", "claude-x", 190_000)
	if got := s.alerts.Load(); got != 2 {
		t.Fatalf("with the hint off: %d alerts", got)
	}
}

func TestContextOf(t *testing.T) {
	t.Setenv(home.Env, t.TempDir())
	sessions := []proto.SessionInfo{
		{ID: "s-1", Role: proto.RoleCoordinator, Project: "demo", Context: 84_000, ContextWindow: 200_000},
		{ID: "s-2", Role: proto.RoleCoordinator, Project: "other"},
	}
	c := contextOf("demo", sessions)
	if c == nil || c.Percent != 42 || c.Threshold != 40 || !c.Hint || c.Tokens != 84_000 || c.Window != 200_000 {
		t.Fatalf("demo: %+v", c)
	}
	sessions[0].Context = 20_000
	if c := contextOf("demo", sessions); c == nil || c.Percent != 10 || c.Hint {
		t.Fatalf("low: %+v", c)
	}
	if c := contextOf("other", sessions); c != nil {
		t.Fatalf("no report yet: %+v", c)
	}
	if c := contextOf("none", sessions); c != nil {
		t.Fatalf("no coordinator: %+v", c)
	}
}
