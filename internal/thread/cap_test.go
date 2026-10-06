package thread

import (
	"testing"
	"time"

	"github.com/theclifmeister/terminatr/internal/proto"
)

// TestWorking is the cap's counting rule (docs/SPEC.md §9): open, live,
// not idle, not done since the last prompt.
func TestWorking(t *testing.T) {
	t0 := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	rec := func(id, state, session string) *Record {
		return &Record{ID: id, State: state, Session: session, LastPrompt: t0}
	}
	done := rec("t-0007", Running, "s7")
	done.Done, done.DoneAt = true, t0.Add(time.Minute)
	reprompted := rec("t-0008", Running, "s8")
	reprompted.Done, reprompted.DoneAt = true, t0.Add(-time.Minute)
	recs := []*Record{
		rec("t-0001", Running, "s1"),   // working: counts
		rec("t-0002", Running, "s2"),   // blocked: counts
		rec("t-0003", Running, "s3"),   // no state yet (unknown): counts
		rec("t-0004", Running, "s4"),   // idle
		rec("t-0005", Stopped, "s5"),   // stopped, even with a session
		rec("t-0006", Running, "gone"), // session no longer runs
		done,                           // done since its last prompt
		reprompted,                     // done before its last prompt: counts
		rec("t-0009", Resolved, "s9"),
		rec("t-0010", Running, "s10"), // a session of another thread
	}
	sessions := []proto.SessionInfo{
		{ID: "s1", Thread: "t-0001", State: "working"},
		{ID: "s2", Thread: "t-0002", State: "blocked"},
		{ID: "s3", Thread: "t-0003"},
		{ID: "s4", Thread: "t-0004", State: "idle"},
		{ID: "s5", Thread: "t-0005", State: "working"},
		{ID: "s7", Thread: "t-0007", State: "working"},
		{ID: "s8", Thread: "t-0008", State: "working"},
		{ID: "s9", Thread: "t-0009", State: "working"},
		{ID: "s10", Thread: "t-0099", State: "working"},
	}
	var got []string
	for _, r := range recs {
		if IsWorking(r, sessions) {
			got = append(got, r.ID)
		}
	}
	want := []string{"t-0001", "t-0002", "t-0003", "t-0008"}
	if len(got) != len(want) || Working(recs, sessions) != len(want) {
		t.Fatalf("working %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("working %v, want %v", got, want)
		}
	}
}
