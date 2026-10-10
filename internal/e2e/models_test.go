package e2e

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// The installed-agents scenarios of docs/SPEC.md §8.2 (Models): which
// agents tm sees installed decides what doctor, tm context and thread
// start offer, and the models are only what each fake agent answers to
// tm's probe (fakeagent/listmodels.go). The Env's PATH holds no real
// agent (basePath), so each scenario installs exactly its fakes.

// modelsProject makes project demo with a git repo (a clone of a bare
// origin), so threads can start.
func modelsProject(t *testing.T, env *Env) string {
	t.Helper()
	root := t.TempDir()
	origin, repo := filepath.Join(root, "origin.git"), filepath.Join(root, "repo")
	git := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@t", "-c", "init.defaultBranch=main"}, args...)...)
		cmd.Dir = dir
		if b, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, b)
		}
	}
	git(root, "init", "--bare", "-q", origin)
	git(root, "clone", "-q", origin, repo)
	os.WriteFile(filepath.Join(repo, "README"), []byte("hi\n"), 0o644)
	git(repo, "add", ".")
	git(repo, "commit", "-q", "-m", "first")
	git(repo, "push", "-q", "origin", "HEAD:main")
	git(repo, "remote", "set-head", "origin", "main")
	var p struct{ Slug, Dir string }
	if err := json.Unmarshal([]byte(env.MustCLI("project", "new", "demo", "--repo", repo, "--json")), &p); err != nil {
		t.Fatal(err)
	}
	env.Trust(p.Dir)
	return p.Dir
}

// refused runs tm and wants it refused with code, and msg in its error.
func refused(t *testing.T, env *Env, code, msg string, args ...string) {
	t.Helper()
	r := env.CLI(args...)
	if r.Code == 0 || !strings.Contains(r.Stderr, code) || !strings.Contains(r.Stderr, msg) {
		t.Fatalf("tm %s: exit %d\n%s%s\nwant %s: %s", strings.Join(args, " "), r.Code, r.Stdout, r.Stderr, code, msg)
	}
}

// contains wants each of want in got.
func contains(t *testing.T, what, got string, want ...string) {
	t.Helper()
	for _, w := range want {
		if !strings.Contains(got, w) {
			t.Fatalf("%s lacks %q:\n%s", what, w, got)
		}
	}
}

// threadArgv starts a thread and returns the argv its agent runs.
func threadArgv(t *testing.T, env *Env, args ...string) []string {
	t.Helper()
	env.MustCLI(append([]string{"thread", "start", "--project", "demo"}, args...)...)
	var argv []string
	Poll(agentWait, func() bool {
		for _, s := range env.Sessions() {
			if s.Thread != "" && len(s.Argv) > 0 {
				argv = s.Argv
			}
		}
		return argv != nil
	})
	if argv == nil {
		t.Fatal("no thread session")
	}
	for _, s := range env.Sessions() {
		if s.Thread != "" {
			env.CLI("thread", "stop", s.Thread, "--project", "demo")
		}
	}
	return argv
}

// TestAgentsNone: with no agent installed tm says so up front, in
// doctor, project new and tm context, and refuses to start a
// coordinator or a thread before anything is created.
func TestAgentsNone(t *testing.T) {
	env := New(t)
	r := env.CLI("doctor")
	if r.Code != 1 {
		t.Fatalf("doctor exit %d\n%s", r.Code, r.Stdout)
	}
	contains(t, "doctor", r.Stdout, "fail  agent", "no agent is installed, so tm can't run coordinators or threads: install one of Claude Code (claude), Codex (codex)")
	contains(t, "project new", env.MustCLI("project", "new", "demo"), "no agent is installed: the project can't run a coordinator or threads until one is")
	refused(t, env, "no-agent", "no agent is installed", "thread", "start", "Fix it", "--project", "demo")
	refused(t, env, "no-agent", "no agent is installed", "project", "open", "demo")
	contains(t, "context", env.MustCLI("context", "--project", "demo"), "Agents: none installed, so no thread can start")
	if out := env.MustCLI("thread", "list", "--project", "demo"); strings.Contains(out, "t-0001") {
		t.Fatalf("a refused start left a thread:\n%s", out)
	}
}

// TestAgentsCodexOnly: only Codex installed: only its models, as it
// answered, in doctor and tm context; threads run it with its models,
// and Claude or a Claude model is refused.
func TestAgentsCodexOnly(t *testing.T) {
	env := New(t)
	env.FakeCodex()
	modelsProject(t, env)
	contains(t, "doctor", env.CLI("doctor").Stdout, "ok    codex models", "2 models, asked 0.160.0 today (chatgpt, plus)")
	ctx := env.MustCLI("context", "--project", "demo")
	contains(t, "context", ctx, "Thread agent: codex (the only agent installed)", "  fake-codex-1: Fake Codex 1: the default (codex's own default)", "  fake-codex-2: Fake Codex 2: another")
	if strings.Contains(ctx, "claude") || strings.Contains(ctx, "fake-hidden") {
		t.Fatalf("context offers more than codex's models:\n%s", ctx)
	}
	refused(t, env, "agent-not-installed", "claude isn't installed", "thread", "start", "Fix it", "--agent", "claude", "--project", "demo")
	refused(t, env, "unknown-model", "fake-codex-1, fake-codex-2", "thread", "start", "Fix it", "--model", "fake-big", "--project", "demo")
	argv := strings.Join(threadArgv(t, env, "--model", "fake-codex-2", "Fix it"), " ")
	if !strings.Contains(argv, "-m fake-codex-2") {
		t.Fatalf("argv %s", argv)
	}
}

// TestAgentsClaudeOnly: only Claude installed: its models, a tag from
// what it says (no auto mode), threads with --model and without.
func TestAgentsClaudeOnly(t *testing.T) {
	env := New(t)
	env.FakeClaude()
	modelsProject(t, env)
	contains(t, "doctor", env.CLI("doctor").Stdout, "ok    claude models", "2 models, asked 2.1.289 today (Fake Plan, firstParty)")
	ctx := env.MustCLI("context", "--project", "demo")
	contains(t, "context", ctx, "Thread agent: claude (the only agent installed)", "  fake-big: Fake Big: for big work", "  fake-small: Fake Small: for small work; no auto mode")
	if strings.Contains(ctx, "codex") {
		t.Fatalf("context names codex:\n%s", ctx)
	}
	refused(t, env, "unknown-model", "fake-big, fake-small", "thread", "start", "Fix it", "--model", "fake-codex-1", "--project", "demo")
	if argv := threadArgv(t, env, "--model", "fake-small", "Fix it"); !strings.Contains(strings.Join(argv, " "), "--model fake-small") {
		t.Fatalf("argv %q", argv)
	}
}

// TestAgentsBoth: both installed: both answer, each its own block; with
// none chosen a thread start is refused naming both, and a model of the
// other agent is refused.
func TestAgentsBoth(t *testing.T) {
	env := New(t)
	env.FakeClaude()
	env.FakeCodex()
	modelsProject(t, env)
	contains(t, "doctor", env.CLI("doctor").Stdout, "ok    claude models", "ok    codex models", "several agents are installed (claude, codex)")
	refused(t, env, "agent-not-chosen", "several agents are installed (claude, codex)", "thread", "start", "Fix it", "--project", "demo")
	refused(t, env, "agent-not-chosen", "several agents are installed", "project", "open", "demo")
	writeConfig(t, env, "[defaults]\nthread_agent = \"claude\"\ncoordinator_agent = \"claude\"\n")
	ctx := env.MustCLI("context", "--project", "demo")
	contains(t, "context", ctx, "Thread agent: claude (config.toml; the human's): tm thread start runs it; --agent may name another installed agent: codex",
		"Models of claude", "  fake-big:", "Models of codex", "  fake-codex-1:")
	refused(t, env, "unknown-model", "isn't one of codex's models", "thread", "start", "Fix it", "--agent", "codex", "--model", "fake-big", "--project", "demo")
}

// TestAgentsFutureOnly: an agent tm has no code for, gemini, from a user
// manifest alone: its [list_models] speaks its own protocol (a "models"
// command answering a list of its own shape), and tm offers its models
// and starts it with them.
func TestAgentsFutureOnly(t *testing.T) {
	env := New(t)
	lister := `[list_models]
args = ["models"]
send = ['{"id":7,"method":"models"}']

[list_models.models]
reply = { id = "7" }
path = "result.list"
name = "slug"
display = "title"

`
	env.FakeAgent("gemini", func(m string) string {
		return regexp.MustCompile(`(?s)\[list_models\].*?\n\[inject\]`).ReplaceAllString(m, lister+"[inject]")
	})
	answers := filepath.Join(t.TempDir(), "gemini.jsonl")
	os.WriteFile(answers, []byte(`{"id":7,"result":{"list":[{"slug":"g-1","title":"G One"},{"slug":"g-2","title":"G Two"}]}}`+"\n"), 0o600)
	env.Setenv("FAKEAGENT_MODELS", answers)
	modelsProject(t, env)
	contains(t, "doctor", env.CLI("doctor").Stdout, "ok    gemini models", "2 models, asked")
	ctx := env.MustCLI("context", "--project", "demo")
	contains(t, "context", ctx, "Thread agent: gemini (the only agent installed)", "  g-1: G One", "  g-2: G Two")
	if argv := threadArgv(t, env, "--model", "g-2", "Fix it"); !strings.Contains(strings.Join(argv, " "), "--model g-2") {
		t.Fatalf("argv %q", argv)
	}
}

// TestModelsUnknown: an installed agent that can't be asked (logged
// out, hanging, answering garbage) has no models: doctor says why,
// --model is refused, and threads run its own default.
func TestModelsUnknown(t *testing.T) {
	root, err := moduleRoot()
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name, mode, file, why string
	}{
		{"logged out", "", filepath.Join(root, "internal", "models", "testdata", "claude-logged-out.jsonl"), "logged out of claude"},
		{"hang", "hang", "", "probe failed: the agent didn't answer in time"},
		{"garbage", "garbage", "", "probe failed:"},
	} {
		t.Run(c.name, func(t *testing.T) {
			env := New(t)
			env.FakeClaude(func(m string) string {
				return strings.Replace(m, "[list_models.models]", "timeout_seconds = 2\n\n[list_models.models]", 1)
			})
			if c.mode != "" {
				env.Setenv("FAKEAGENT_MODELS_MODE", c.mode)
			}
			if c.file != "" {
				env.Setenv("FAKEAGENT_MODELS", c.file)
			}
			modelsProject(t, env)
			contains(t, "doctor", env.CLI("doctor").Stdout, "warn  claude models", "unknown: "+c.why)
			refused(t, env, "models-unknown", c.why, "thread", "start", "Fix it", "--model", "fake-big", "--project", "demo")
			contains(t, "context", env.MustCLI("context", "--project", "demo"), "Models of claude: unknown ("+c.why)
			if argv := threadArgv(t, env, "Fix it"); slices.Contains(argv, "--model") {
				t.Fatalf("argv %q", argv)
			}
		})
	}
}

// TestModelRefused: a model the account refuses is learnt from the
// session's transcript: marked for this account, the coordinator told
// (an inbox item), and the next start with it refused.
func TestModelRefused(t *testing.T) {
	env := New(t)
	env.FakeClaude()
	scripts := t.TempDir()
	os.WriteFile(filepath.Join(scripts, "refuse.toml"), []byte(`[[step]]
do = "transcript"
payload = { type = "assistant", error = "model_not_found", isApiErrorMessage = true, message = { content = "There's an issue with the selected model (fake-small). It may not exist or you may not have access to it." } }
`), 0o644)
	env.Setenv("FAKEAGENT_SCRIPTS", scripts)
	modelsProject(t, env)
	env.MustCLI("doctor")
	env.MustCLI("thread", "start", "--project", "demo", "--model", "fake-small", "Fix it")
	var thread *Session
	Poll(agentWait, func() bool {
		for _, s := range env.Sessions() {
			if s.Thread != "" {
				thread = &Session{ID: s.ID}
			}
		}
		return thread != nil
	})
	if thread == nil {
		t.Fatal("no thread session")
	}
	env.WaitState(thread, "idle", agentWait)
	env.Prompt(thread, "run refuse")
	ok := Poll(agentWait, func() bool {
		return strings.Contains(env.MustCLI("context", "--project", "demo"), "refused for the user's account: fake-small")
	})
	if !ok {
		t.Fatalf("not learnt:\n%s", env.MustCLI("context", "--project", "demo"))
	}
	refused(t, env, "model-refused", "claude refused fake-small for the user's account", "thread", "start", "--project", "demo", "--model", "fake-small", "Again")
	contains(t, "doctor", env.CLI("doctor").Stdout, "refused for your account: fake-small (There's an issue with the selected model")
	items, _ := filepath.Glob(filepath.Join(env.Home, "projects", "demo", "inbox", "2*-model-refused-*.md"))
	if len(items) != 1 {
		t.Fatalf("inbox items %v", items)
	}
}
