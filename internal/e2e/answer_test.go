package e2e

// tm thread answer (docs/SPEC.md §11.2): the coordinator relays the
// user's answer to a thread's question menu, recognised by the agent's
// screen rule; journaled. The fake agent draws Claude's AskUserQuestion
// menu, with "Type something." as a text field.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const askScript = `
[[step]]
do = "question"
header = "Color"
question = "Which color should the button be?"
options = ["Red", "Blue"]

[[step]]
do = "question"
header = "Name"
question = "What should the button say?"
options = ["OK", "Go"]

[[step]]
do = "stream"
ms = 50
text = "ASKED"
`

func TestThreadAnswer(t *testing.T) {
	env, projDir, _ := threadEnv(t)
	scripts := ""
	for _, kv := range env.Vars {
		if v, ok := strings.CutPrefix(kv, "FAKEAGENT_SCRIPTS="); ok {
			scripts = v
		}
	}
	os.WriteFile(filepath.Join(scripts, "thread-ask.toml"), []byte(askScript), 0o644)
	th := startThread(t, env, projDir)
	answer := func(args ...string) Result {
		return env.CLI(append([]string{"thread", "answer", "t-0001", "--project", "demo"}, args...)...)
	}

	// No menu: refused.
	if r := answer("--choice", "1"); r.Code != 1 || !strings.Contains(r.Stderr+r.Stdout, "not-question") {
		t.Fatalf("answer while idle: %+v", r)
	}
	if r := answer(); r.Code != 2 {
		t.Fatalf("answer without --choice: %+v", r)
	}

	env.MustCLI("thread", "prompt", "t-0001", "run thread-ask", "--project", "demo")
	env.WaitState(th, "blocked", agentWait)
	env.WaitFor(th, "Which color should the button be?", agentWait)
	waitReason(t, env, th, "question")

	for _, c := range []struct {
		args []string
		code string
	}{
		{[]string{"--choice", "7"}, "no-option"},
		{[]string{"--choice", "2", "--text", "teal"}, "not-text-option"},
		{[]string{"--choice", "3"}, "needs-text"},
	} {
		if r := answer(c.args...); r.Code != 1 || !strings.Contains(r.Stderr+r.Stdout, c.code) {
			t.Errorf("answer %v: want %s, got %+v", c.args, c.code, r)
		}
	}
	if r := answer("--choice", "2"); r.Code != 0 || !strings.Contains(r.Stdout, "answered t-0001: Blue") {
		t.Fatalf("answer 2: %+v", r)
	}
	env.WaitFor(th, "→ Blue", agentWait)

	// The free-text option takes the user's words.
	env.WaitFor(th, "What should the button say?", agentWait)
	waitReason(t, env, th, "question")
	if r := answer("--choice", "3", "--text", "Start now\n"); r.Code != 0 || !strings.Contains(r.Stdout, `answered t-0001: "Start now"`) {
		t.Fatalf("answer with text: %+v", r)
	}
	env.WaitFor(th, "→ Start now", agentWait)
	env.WaitFor(th, "ASKED", agentWait)

	journal, _ := os.ReadFile(filepath.Join(projDir, "JOURNAL.md"))
	for _, w := range []string{"human thread.answer t-0001 Which color should the button be? → Blue",
		`human thread.answer t-0001 What should the button say? → "Start now"`} {
		if !strings.Contains(string(journal), w) {
			t.Errorf("journal lacks %q:\n%s", w, journal)
		}
	}
}

// waitReason waits until the session's state has reason r, from the
// settled screen too.
func waitReason(t *testing.T, env *Env, s *Session, r string) {
	t.Helper()
	if !Poll(agentWait, func() bool {
		i, _ := env.Info(s)
		x := env.Explain(s)
		return i.Reason == r && x.Screen != nil && x.Screen.Reason == r
	}) {
		i, _ := env.Info(s)
		t.Fatalf("reason %q, want %q:\n%s", i.Reason, r, env.Screen(s))
	}
}
