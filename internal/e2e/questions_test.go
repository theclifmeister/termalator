package e2e

// The coordinator's questions (docs/SPEC.md §7.5, Questions): tm ask
// stores them, the coordinator's watch counts them for its band, and
// tm ask open (the band's press, the dashboard's key) queues the prompt
// that has the coordinator open them in its question dialog.

import (
	"encoding/json"
	"os/exec"
	"strings"
	"testing"

	"github.com/theclifmeister/terminatr/internal/proto"
)

func TestQuestionsOpen(t *testing.T) {
	env, projDir, _ := threadEnv(t)
	coord := env.StartAgent("claude", projDir, "--role", "coordinator", "--project", "demo")
	env.WaitState(coord, "idle", agentWait)

	if r := env.CLI("ask", "open", "--project", "demo"); r.Code != 1 || !strings.Contains(r.Stderr, "no open questions") {
		t.Fatalf("open without questions: %+v", r)
	}
	// The coordinator asks, through the server.
	add := exec.Command(env.Bin, "ask", "add", "Which repo gets the band?", "--task", "T1",
		"--option", "Main: the tm repo", "--option", "Site", "--recommend", "Main", "--project", "demo")
	add.Env = append(env.Vars, "TERMINATR_SESSION="+coord.ID)
	if b, err := add.CombinedOutput(); err != nil || string(b) != "asked Q1\n" {
		t.Fatalf("ask add: %v\n%s", err, b)
	}
	// Its watch line counts them: the band's number.
	w := exec.Command(env.Bin, "watch", "--session", coord.ID, "--json")
	w.Env = env.Vars
	out, err := w.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Start(); err != nil {
		t.Fatal(err)
	}
	env.track(w.Process.Pid, "tm watch")
	var line proto.Watch
	err = json.NewDecoder(out).Decode(&line)
	w.Process.Kill()
	w.Wait()
	if err != nil || line.Questions != 1 {
		t.Fatalf("watch line %+v: %v", line, err)
	}

	// The user asks to answer them: the coordinator gets the fixed-word
	// prompt.
	r := env.CLI("ask", "open", "--project", "demo")
	if r.Code != 0 || !strings.Contains(r.Stdout, "1 question waiting: asked the coordinator ("+coord.ID+")") {
		t.Fatalf("open: %+v", r)
	}
	env.WaitFake("prompt", agentWait, func(r FakeRecord) bool {
		return strings.HasPrefix(r.Str("text"), "[tm] answer questions: the user wants to answer your 1 open question(s) now.")
	})

	// Answered: the store and the context are clear again.
	if r := env.CLI("ask", "done", "Q1", "--project", "demo"); r.Code != 0 {
		t.Fatalf("done: %+v", r)
	}
	if r := env.CLI("ask", "list", "--json", "--project", "demo"); strings.TrimSpace(r.Stdout) != "[]" {
		t.Fatalf("list: %+v", r)
	}
}
