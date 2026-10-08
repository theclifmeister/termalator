package session

import (
	"testing"
	"time"
)

// TestAnswer: a rule's keys are typed once it has been the screen's
// match for answerSettle, again every answerRetry while it stays, at
// most answerTries times; another match starts over.
func TestAnswer(t *testing.T) {
	rt := &agentRT{keys: map[string]string{"review": "3"}}
	t0 := time.Now()
	at := func(d time.Duration) time.Time { return t0.Add(d) }
	steps := []struct {
		rule string
		at   time.Duration
		want string
	}{
		{"review", 0, ""},                      // just painted
		{"review", 300 * time.Millisecond, ""}, // settling
		{"review", time.Second, "3"},
		{"review", 2 * time.Second, ""}, // waiting for it to go
		{"review", 4 * time.Second, "3"},
		{"review", 7 * time.Second, "3"},
		{"review", 10 * time.Second, ""}, // tried enough
		{"idle", 11 * time.Second, ""},
		{"review", 12 * time.Second, ""}, // shown again: settle first
		{"review", 13 * time.Second, "3"},
		{"", 14 * time.Second, ""},
	}
	for i, s := range steps {
		if got := rt.answer(s.rule, at(s.at)); got != s.want {
			t.Errorf("step %d (%s at %v): %q, want %q", i, s.rule, s.at, got, s.want)
		}
	}
}
