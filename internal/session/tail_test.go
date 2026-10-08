package session

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/theclifmeister/terminatr/internal/agent"
)

// TestReadTailUsage: the tailer hands on the usage of whole lines
// appended since the file was named, once each: a repeat of the last
// report (same key) and a partial last line are not counted (the latter
// until it is whole).
func TestReadTailUsage(t *testing.T) {
	m, err := agent.ParseManifest([]byte(`manifest_version = 1
name = "a"
[launch]
command = "a"
[jsonl_tail]
path_field = "transcript_path"
[jsonl_tail.usage]
match = { type = "token_count" }
input = "last.input"
cache_read = "last.cached"
input_includes_cache = true
context = "last.input"
context_window = "window"
key = "total"
turn = { type = "task_complete" }
`))
	if err != nil {
		t.Fatal(err)
	}
	rt, err := newAgentRT(AgentConfig{Agent: agent.FromManifest(m)}, 0, false)
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "rollout.jsonl")
	appendLines := func(s string) {
		f, err := os.OpenFile(p, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		f.WriteString(s)
		f.Close()
	}
	appendLines(`{"type":"token_count","last":{"input":999},"total":999}` + "\n") // before the session named it
	rt.setTail(p)
	appendLines(`{"type":"token_count","last":{"input":100,"cached":40},"window":1000,"total":100}` + "\n" +
		`{"type":"token_count","last":{"input":100,"cached":40},"window":1000,"total":100}` + "\n" +
		`{"type":"token_count","last":{"input":300,"cached":250},"window":1000,"total":400}` + "\n" +
		`{"type":"task_complete"}` + "\n" +
		`{"type":"token_count","last":{"input":7`)
	got := rt.readTail()
	want := []agent.Usage{
		{Input: 60, CacheRead: 40, Context: 100, ContextWindow: 1000, Key: "100"},
		{Input: 50, CacheRead: 250, Context: 300, ContextWindow: 1000, Key: "400"},
		{Turns: 1},
	}
	if len(got) != len(want) {
		t.Fatalf("usage %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("usage[%d] %+v, want %+v", i, got[i], want[i])
		}
	}
	// The partial line, whole now; then a repeat of it across reads.
	appendLines(`00,"cached":0},"window":1000,"total":1100}` + "\n")
	if got := rt.readTail(); len(got) != 1 || got[0].Input != 700 {
		t.Fatalf("completed line: %+v", got)
	}
	appendLines(`{"type":"token_count","last":{"input":700,"cached":0},"window":1000,"total":1100}` + "\n")
	if got := rt.readTail(); len(got) != 0 {
		t.Fatalf("repeat across reads: %+v", got)
	}
}
