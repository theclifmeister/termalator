package e2e

// tm thread answer (docs/SPEC.md §11.2): the coordinator relays the
// user's answer to a thread's question menu, recognised by the agent's
// screen rule; journaled. The fake agent draws Claude's AskUserQuestion
// menu, with "Type something." as a text field.

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
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

// TestThreadAnswerByMod: with the agent's mod, the menu is open on the
// server (`tm session ask`, here run by the test in the mod's place):
// tm thread show and tm context list it, and tm thread answer fills in
// its questions by label, number or the user's words; the mod gets the
// answers once the last question has one. Without an open menu, answers
// by label are refused: the screen's keys take numbers only.
func TestThreadAnswerByMod(t *testing.T) {
	env, projDir, _ := threadEnv(t)
	th := startThread(t, env, projDir)
	answer := func(args ...string) Result {
		return env.CLI(append([]string{"thread", "answer", "t-0001", "--project", "demo"}, args...)...)
	}
	if r := answer("--option", "Blue"); r.Code != 1 || !strings.Contains(r.Stderr+r.Stdout, "no-mod-question") {
		t.Fatalf("answer by label without the mod: %+v", r)
	}

	mod := exec.Command(env.Bin, "session", "ask", th.ID)
	mod.Env = env.Vars
	mod.Stdin = strings.NewReader(`{"tool":"AskUserQuestion","questions":[
		{"question":"Which colour?","header":"Colour","multiSelect":false,"options":[{"label":"Red","description":"warm"},{"label":"Blue","description":""}]},
		{"question":"Which fruits?","header":"Fruit","multiSelect":true,"options":[{"label":"Apple"},{"label":"Pear"},{"label":"Plum"}]}]}`)
	var out, errb bytes.Buffer
	mod.Stdout, mod.Stderr = &out, &errb
	if err := mod.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { mod.Process.Kill() })
	exited := make(chan error, 1)
	go func() { exited <- mod.Wait() }()

	var show string
	if !Poll(agentWait, func() bool {
		show = env.MustCLI("thread", "show", "t-0001", "--project", "demo")
		return strings.Contains(show, "Question (open since")
	}) {
		t.Fatalf("thread show lacks the question:\n%s", show)
	}
	for _, w := range []string{"question open", "1. [Colour] Which colour?", "1. Red — warm", "3. (the user's own words: --text)", "2. [Fruit] Which fruits? (several: --choice 1,3)"} {
		if !strings.Contains(show, w) {
			t.Errorf("thread show lacks %q:\n%s", w, show)
		}
	}
	if ctx := env.MustCLI("context", "--project", "demo"); !strings.Contains(ctx, "Question: "+th.ID+" (thread t-0001) waits on a question menu") ||
		!strings.Contains(ctx, "   2. Pear") {
		t.Errorf("context lacks the question:\n%s", ctx)
	}

	if r := answer("--option", "Green"); r.Code != 1 || !strings.Contains(r.Stderr, "no-option") {
		t.Fatalf("answer with an unknown label: %+v", r)
	}
	if r := answer("--option", "blue"); r.Code != 0 || !strings.Contains(r.Stdout, "question 1 of 2: Blue; 1 more") {
		t.Fatalf("answer 1: %+v", r)
	}
	if show = env.MustCLI("thread", "show", "t-0001", "--project", "demo"); !strings.Contains(show, `Which colour? → answered: "Blue"`) {
		t.Fatalf("thread show after answer 1:\n%s", show)
	}
	if r := answer("--choice", "1,3", "--text", "a kiwi"); r.Code != 0 || !strings.Contains(r.Stdout, "answered t-0001: Apple, Plum, a kiwi") {
		t.Fatalf("answer 2: %+v", r)
	}
	select {
	case err := <-exited:
		if err != nil || strings.TrimSpace(out.String()) != `{"Which colour?":"Blue","Which fruits?":"Apple, Plum, a kiwi"}` {
			t.Fatalf("the mod got %q (%v) %s", out.String(), err, errb.String())
		}
	case <-time.After(agentWait):
		t.Fatal("the mod got no answers")
	}
	if show = env.MustCLI("thread", "show", "t-0001", "--project", "demo"); strings.Contains(show, "Question") {
		t.Fatalf("the question outlived its answers:\n%s", show)
	}
	journal, _ := os.ReadFile(filepath.Join(projDir, "JOURNAL.md"))
	for _, w := range []string{"human thread.answer t-0001 Which colour? → Blue", "human thread.answer t-0001 Which fruits? → Apple, Plum, a kiwi"} {
		if !strings.Contains(string(journal), w) {
			t.Errorf("journal lacks %q:\n%s", w, journal)
		}
	}
}
