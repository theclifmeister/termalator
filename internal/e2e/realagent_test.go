//go:build realclaude || realcodex

package e2e

// The helpers the real-agent suites share (realclaude_test.go,
// realcodex_test.go): an isolated terminatr with the user's login, an
// agent started and waited for, and the hook events it produced. A third
// agent's suite adds its build tag here and its own scenarios.

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const realWait = 120 * time.Second

// realAgentEnv is an isolated terminatr with the user's real HOME (the
// agent's login lives there, or in the CODEX_HOME it names) and the agent
// on PATH. loginCheck, if set, gets the agent's binary and returns why it
// can't run (not logged in), or "".
func realAgentEnv(t *testing.T, name string, loginCheck func(bin string) string) *Env {
	t.Helper()
	bin, err := exec.LookPath(name)
	if err != nil {
		t.Skip(name + " is not on PATH")
	}
	if loginCheck != nil {
		if why := loginCheck(bin); why != "" {
			t.Skip(why)
		}
	}
	env := New(t)
	env.Setenv("HOME", os.Getenv("HOME"))
	if h := os.Getenv("CODEX_HOME"); h != "" {
		env.Setenv("CODEX_HOME", h)
	}
	binDir := filepath.Dir(bin)
	if name == "claude" {
		binDir = pinClaudePermissionMode(t, bin) + ":" + binDir
	}
	env.Setenv("PATH", binDir+":"+filepath.Dir(env.Bin)+":/usr/bin:/bin:/usr/sbin:/sbin")
	return env
}

// pinClaudePermissionMode returns a folder holding a `claude` that runs
// bin with --permission-mode default, so the permission scenarios don't
// depend on a defaultMode (auto, say) in the user's own settings, which
// stay untouched. Launches that already choose a mode (yolo) pass through.
func pinClaudePermissionMode(t *testing.T, bin string) string {
	t.Helper()
	dir := t.TempDir()
	// CLAUDE_CODE_ENABLE_TODO_TOOLS: claude 2.1.295 offers TaskCreate and
	// TaskUpdate only to legacy or unset models; the suite's haiku (5.x)
	// gets no todo tool without it, and the todos scenario needs one.
	script := "#!/bin/sh\nexport CLAUDE_CODE_ENABLE_TODO_TOOLS=1\n" +
		"for a in \"$@\"; do case \"$a\" in --dangerously-skip-permissions|--permission-mode|--permission-mode=*) exec '" + bin + "' \"$@\";; esac; done\n" +
		"exec '" + bin + "' --permission-mode default \"$@\"\n"
	if err := os.WriteFile(filepath.Join(dir, "claude"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

// realStart starts the agent with model in a fresh folder and waits for
// it to be idle. Claude's trust dialog is answered the way a user would,
// through the screen rules; for Codex tm trusts the folder itself (-c
// projects) and a dialog left on screen fails here.
func realStart(t *testing.T, env *Env, name, model string, args ...string) (*Session, string) {
	t.Helper()
	dir := env.Workdir()
	s := env.StartAgent(name, dir, append([]string{"--model", model}, args...)...)
	if !Poll(realWait, func() bool {
		i, _ := env.Info(s)
		return i.State == "idle" || i.Reason == "trust"
	}) {
		t.Fatalf("%s never came up idle:\n%s\nexplain:\n%s", name, env.Screen(s), env.CLI("agent", "explain", s.ID).Stdout)
	}
	if i, _ := env.Info(s); i.Reason == "trust" {
		time.Sleep(time.Second) // keys within ~0.5 s of the dialog are dropped
		if strings.Contains(env.Screen(s), "Yes, I accept") {
			env.Keys(s, "2") // the bypass warning (yolo)
		} else {
			env.Keys(s, "\x1b[B")
			time.Sleep(200 * time.Millisecond)
			env.Keys(s, "\r")
		}
	}
	env.WaitState(s, "idle", realWait)
	return s, dir
}

// agentEvents lists the hook events the server received for a session
// after seq, and the last seq.
func agentEvents(env *Env, s *Session, after uint64) ([]string, uint64) {
	var out []string
	last := after
	for _, ev := range env.Explain(s).Events {
		if ev.Seq > after {
			out = append(out, ev.Event)
			last = ev.Seq
		}
	}
	return out, last
}
