package detect

import (
	"strings"
	"testing"

	"github.com/theclifmeister/terminatr/internal/agent"
)

func codexEngine(t *testing.T) *Engine {
	t.Helper()
	reg, err := agent.Load("")
	if err != nil {
		t.Fatal(err)
	}
	a, ok := reg.Get("codex")
	if !ok {
		t.Fatal("no codex manifest")
	}
	e, err := New(a.Rules())
	if err != nil {
		t.Fatal(err)
	}
	return e
}

// Fixtures: Codex 0.160.0 in a tm pane, 120x40 (T91's captures in
// threads/t-0083/library/T91-codex-screens.md, and T98's), blank rows
// kept where the layout has them.
const (
	codexTrust = `  Folder access
  /private/tmp/claude-501/t83/wt

  Note: You’re in a subdirectory of a Git project. Trusting will apply to the repository root:
  /private/tmp/claude-501/t83/repo

  Trust this folder? Codex can read, edit, and run files here, subject to your permission settings. Folder settings
  can run code automatically, even without a model request. Continue only if you trust these files. Your trust
  decision will be saved.

› 1. Trust and continue
  2. Back to Agent Command Center

  enter continue · esc back`

	codexUpdate = `  Update available · 0.160.0 → 0.160.1
  Release notes: https://github.com/openai/codex/releases/latest

› 1. Update now (runs ` + "`brew upgrade --cask codex`" + `)
  2. Skip
  3. Skip until next version

  enter continue · esc skip`

	codexHooksReview = `  Hooks need review
  4 hooks are new or changed.
  Hooks can run outside the sandbox after you trust them.

› 1. Review hooks
  2. Trust all and continue
  3. Continue without trusting (hooks won't run)

  enter confirm · esc skip`

	codexIdle = `  >_ OpenAI Codex (v0.160.0)
     /private/tmp/claude-501/t134/wt

  Hello again, carbon-based collaborator.

› Reply with just READY.

• READY

  Worked for 2s • 17:04

› Ask Codex to do anything

  GPT-6-Luna medium · /private/tmp/claude-501/t134/wt
  ? for shortcuts                                                                             ⚠ 2 warnings · f2 to view`

	codexTyped = `• READY

  Worked for 2s • 17:04

› Create the file /private/tmp/claude-501/t134/outside.txt containing hi with a shell command, requesting escalated
  permissions.

  GPT-6-Luna medium · /private/tmp/claude-501/t134/wt
                                                                                              ⚠ 2 warnings · f2 to view`

	codexWorking = `■ Conversation interrupted - use /feedback if something went wrong

› Run the shell command: sleep 6; then reply DONE.

• Working (3s • esc to interrupt) · 1 background terminal running · /ps to view · /stop to close

› Ask Codex to do anything

  GPT-6-Luna medium · /private/tmp/claude-501/t134/wt
  ? for shortcuts                                                                             ⚠ 2 warnings · f2 to view`

	codexApproval = `› Create the file /private/tmp/claude-501/t134/outside.txt containing hi with a shell command, requesting escalated
  permissions.

• Running printf 'hi' > /private/tmp/claude-501/t134/outside.txt

  Would you like to run the following command?

  Environment: local

  Reason: Do you want to allow creating /private/tmp/claude-501/t134/outside.txt with the requested contents?

  $ printf 'hi' > /private/tmp/claude-501/t134/outside.txt

› 1. Yes, proceed (y)
  2. Yes, and don't ask again for commands that start with ` + "`printf 'hi' > /private/tmp/claude-501/t134/outside.txt`" + ` (p)
  3. No, and tell Codex what to do differently (esc)

  Press enter to confirm or esc to cancel`

	codexQuestion = `• Exploring
  └ List rg --files -g '!*node_modules*' -g '!*.lock'
    + Show details

  Question 1/1 (1 unanswered)
  Which colour should the setting use?

  › 1. Red                 Use red as the setting colour.
    2. Green               Use green as the setting colour.
    3. Blue (Recommended)  Use blue as the setting colour.
    4. None of the above   Optionally, add details in notes (tab)

  tab to add notes | enter to submit answer | esc to interrupt`

	codexNew = `■ Conversation interrupted - use /feedback if something went wrong
• Context compacted · 2s
  Worked for 2s • 22:23

  Where should the new conversation run?

› 1. Current checkout  Keep using the current working directory
  2. New worktree      Create an isolated managed checkout

  enter select · esc back`
)

func TestCodexRules(t *testing.T) {
	e := codexEngine(t)
	cases := []struct {
		name, body string
		rule       string
		state      agent.State
		reason     string
	}{
		{"folder trust", codexTrust, "trust-folder", agent.StateBlocked, "trust"},
		{"update", codexUpdate, "update-available", agent.StateBlocked, "update"},
		{"hooks review", codexHooksReview, "hooks-review", agent.StateBlocked, "trust"},
		{"idle composer", codexIdle, "idle-composer", agent.StateIdle, ""},
		{"typed, wrapped", codexTyped, "idle-composer", agent.StateIdle, ""},
		{"working beats the composer", codexWorking, "working-status", agent.StateWorking, ""},
		{"approval beats its selected option", codexApproval, "blocked-permission-dialog", agent.StateBlocked, "permission"},
		{"question", codexQuestion, "blocked-question", agent.StateBlocked, "question"},
		{"/new asks where", codexNew, "blocked-new-dialog", agent.StateBlocked, "question"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m, all := e.Eval(screen("", c.body))
			if m == nil {
				t.Fatalf("no rule matched")
			}
			if m.Rule != c.rule || m.State != c.state || m.Reason != c.reason {
				t.Fatalf("got %+v (all %+v), want %s %s/%s", *m, all, c.rule, c.state, c.reason)
			}
		})
	}
}

// TestCodexEmptyBox: the placeholder is dim (seen live, 0.160), so only
// it in the composer is an empty box; typed text, even wrapped, is not.
func TestCodexEmptyBox(t *testing.T) {
	e := codexEngine(t)
	has := func(s Screen) bool {
		_, all := e.Eval(s)
		for _, m := range all {
			if m.Rule == "idle-empty-box" {
				return true
			}
		}
		return false
	}
	rows := strings.Split(codexIdle, "\n")
	noDim := append([]string{}, rows...)
	for i, r := range noDim {
		noDim[i] = strings.Replace(r, "Ask Codex to do anything", "                        ", 1)
	}
	if !has(Screen{Rows: rows, NoDim: noDim}) {
		t.Fatal("the placeholder alone is not an empty box")
	}
	if typed := strings.Split(codexTyped, "\n"); has(Screen{Rows: typed, NoDim: typed}) {
		t.Fatal("typed text counts as an empty box")
	}
}

// TestCodexAnswers: the dialogs tm answers itself, with the option the
// user chose for each: Continue without trusting, Skip.
func TestCodexAnswers(t *testing.T) {
	reg, err := agent.Load("")
	if err != nil {
		t.Fatal(err)
	}
	a, _ := reg.Get("codex")
	keys := map[string]string{}
	for _, r := range a.Rules() {
		if r.Keys != "" {
			keys[r.ID] = r.Keys
		}
	}
	want := map[string]string{"hooks-review": "3", "update-available": "2"}
	if len(keys) != len(want) || keys["hooks-review"] != "3" || keys["update-available"] != "2" {
		t.Fatalf("keys %q, want %q", keys, want)
	}
	if !strings.Contains(codexHooksReview, "3. Continue without trusting") || !strings.Contains(codexUpdate, "2. Skip\n") {
		t.Fatal("the fixtures no longer number the options the keys pick")
	}
}
