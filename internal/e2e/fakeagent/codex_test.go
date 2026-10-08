package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/creack/pty"

	"github.com/theclifmeister/terminatr/internal/agent/codex"
)

func TestParseCodexArgs(t *testing.T) {
	o := parseCodexArgs([]string{"resume", "id-1", "", "-c", "a=1", "--config=b=2", "-m", "gpt-6-luna",
		"--approve-for-me", "", "--", "hi", "-c"})
	if o.resume == nil || *o.resume != "id-1" || !slices.Equal(o.config, []string{"a=1", "b=2"}) ||
		o.model != "gpt-6-luna" || !o.approveForMe || o.yolo || o.prompt != "hi -c" {
		t.Fatalf("%+v", o)
	}
	o = parseCodexArgs([]string{"queue", "--thread=t1", "--message=-x"})
	if !o.queue || o.thread != "t1" || o.message != "-x" {
		t.Fatalf("queue: %+v", o)
	}
	o = parseCodexArgs([]string{"--dangerously-bypass-approvals-and-sandbox", "do it"})
	if !o.yolo || o.prompt != "do it" || o.resume != nil {
		t.Fatalf("yolo: %+v", o)
	}
}

// TestCodexTrustHash: the fake's hash, written apart, agrees with the
// Go agent's.
func TestCodexTrustHash(t *testing.T) {
	for _, ev := range []string{"PreToolUse", "SessionEnd"} {
		want, _ := codex.TrustHash(snakeCase(ev), codex.HookCommand, 5)
		if got := codexTrustHash(snakeCase(ev), codex.HookCommand, 5); got != want {
			t.Errorf("%s: %s, want %s", ev, got, want)
		}
	}
}

var codexEvents = []string{"SessionStart", "UserPromptSubmit", "PreToolUse", "PostToolUse", "PreCompact", "PostCompact",
	"PermissionRequest", "Stop", "Interrupt", "SubagentStart", "SubagentStop", "SessionEnd"}

type codexOpt struct {
	args      []string // before the kickoff
	kickoff   string
	untrusted bool // no -c projects for the cwd
	badHash   string
	env       []string
}

// startCodex runs the fake as "codex" with the hooks and trust flags
// the Go agent gives a launch. TERMINATR_BIN is the logging hook script,
// so the hooks' command ("$TERMINATR_BIN" hook --agent codex) runs it.
func startCodex(t *testing.T, o codexOpt) *agent {
	t.Helper()
	home, cwd, plugin := newEnv(t)
	return startCodexIn(t, home, cwd, plugin, o)
}

func startCodexIn(t *testing.T, home, cwd, plugin string, o codexOpt) *agent {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "codex")
	if err := os.Symlink(binPath, bin); err != nil {
		t.Fatal(err)
	}
	hooks, state, err := codex.HookArgs(codexEvents)
	if err != nil {
		t.Fatal(err)
	}
	if o.badHash != "" {
		h, _ := codex.TrustHash(snakeCase(o.badHash), codex.HookCommand, 5)
		state = strings.Replace(state, h, "sha256:00", 1)
	}
	args := o.args
	if !o.untrusted {
		args = append(args, "-c", fmt.Sprintf(`projects={%q={trust_level="trusted"}}`, cwd))
	}
	args = append(args, "-c", "check_for_update_on_startup=false", "-c", hooks, "-c", state)
	if o.kickoff != "" {
		args = append(args, "--", o.kickoff)
	}
	a := &agent{t: t, home: home, cwd: cwd, hookLog: filepath.Join(home, "hooks.jsonl"), log: filepath.Join(home, "fa.jsonl"), done: make(chan struct{})}
	a.cmd = exec.Command(bin, args...)
	a.cmd.Dir = cwd
	a.cmd.Env = append(os.Environ(), "HOME="+home, "HOOKLOG="+a.hookLog, "FAKEAGENT_LOG="+a.log,
		"FAKEAGENT_TRUST_DEBOUNCE_MS=300", "TERMINATR_SESSION=s1", "TERMINATR_BIN="+filepath.Join(plugin, "hook.sh"))
	a.cmd.Env = append(a.cmd.Env, o.env...)
	f, err := pty.StartWithSize(a.cmd, &pty.Winsize{Rows: 40, Cols: 120})
	if err != nil {
		t.Fatal(err)
	}
	a.pty = f
	go func() {
		buf := make([]byte, 8192)
		for {
			n, err := f.Read(buf)
			a.mu.Lock()
			a.out.Write(buf[:n])
			a.mu.Unlock()
			if err != nil {
				return
			}
		}
	}()
	go func() {
		a.err = a.cmd.Wait()
		close(a.done)
	}()
	t.Cleanup(func() {
		select {
		case <-a.done:
		default:
			a.cmd.Process.Kill()
			<-a.done
		}
		f.Close()
	})
	return a
}

// rolloutTypes lists the rollout's event_msg payload types.
func rolloutTypes(path string) []string {
	var out []string
	for _, r := range readJSONL(path) {
		if p, ok := r["payload"].(map[string]any); ok && r["type"] == "event_msg" {
			out = append(out, fmt.Sprint(p["type"]))
		}
	}
	return out
}

// queue runs `codex queue` as the Go agent does.
func (a *agent) queue(thread, text string) (string, error) {
	cmd := exec.Command(binPath, "queue", "--thread="+thread, "--message="+text)
	cmd.Args[0] = "codex"
	cmd.Env = append(os.Environ(), "HOME="+a.home, "FAKEAGENT_LOG="+a.log, "FAKEAGENT_FLAVOR=codex")
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// TestCodexTurn: no hook before the first prompt; then SessionStart
// (startup), UserPromptSubmit and Stop, with the rollout as the
// transcript, which gets task_started, token_count and task_complete.
func TestCodexTurn(t *testing.T) {
	t.Parallel()
	a := startCodex(t, codexOpt{args: []string{"-m", "gpt-5.6-terra"}})
	a.waitScreen(0, codexPlaceholder)
	if ev := a.events(); len(ev) != 0 {
		t.Fatalf("hooks before the first prompt: %v", ev)
	}
	a.send("\x1b[200~hello\x1b[201~")
	a.send("\r")
	a.waitEvent("Stop", 1)
	if got := a.events(); strings.Join(got, ",") != "SessionStart,UserPromptSubmit,Stop" {
		t.Fatalf("events %v", got)
	}
	h := a.hooks()
	roll, _ := h[0]["transcript_path"].(string)
	if h[0]["source"] != "startup" || h[0]["model"] != "gpt-5.6-terra" || !strings.Contains(roll, "/.codex/sessions/") ||
		!strings.HasSuffix(roll, "-"+fmt.Sprint(h[0]["session_id"])+".jsonl") {
		t.Fatalf("SessionStart %v", h[0])
	}
	if got := rolloutTypes(roll); strings.Join(got, ",") != "task_started,token_count,task_complete" {
		t.Fatalf("rollout %v", got)
	}
	if h[2]["last_assistant_message"] != "ok: hello" {
		t.Fatalf("Stop %v", h[2])
	}
	start := a.logKinds("start")[0]
	if start["hooks"] != float64(len(codexEvents)) || start["untrusted_hooks"] != float64(0) {
		t.Fatalf("start %v", start)
	}
}

// TestCodexHooksReview: a hook whose hash is wrong is untrusted: the
// review dialog shows, and "Continue without trusting" runs only the
// trusted ones.
func TestCodexHooksReview(t *testing.T) {
	t.Parallel()
	a := startCodex(t, codexOpt{badHash: "Stop", kickoff: "hello"})
	a.waitScreen(0, "Hooks need review")
	a.sendUntil("3", func() bool { return len(a.logKinds("hooks-review")) > 0 })
	a.waitEvent("UserPromptSubmit", 1)
	waitUntil(t, "task_complete", func() bool { return slices.Contains(rolloutTypes(a.rollout()), "task_complete") })
	if count(a.events(), "Stop") != 0 {
		t.Fatalf("the untrusted Stop hook ran: %v", a.events())
	}
}

// rollout is the transcript_path the hooks reported last.
func (a *agent) rollout() string {
	h := a.hooks()
	if len(h) == 0 {
		return ""
	}
	p, _ := h[len(h)-1]["transcript_path"].(string)
	return p
}

// TestCodexFolderTrust: without -c projects for the folder, the trust
// dialog comes first; "Trust and continue" goes on.
func TestCodexFolderTrust(t *testing.T) {
	t.Parallel()
	a := startCodex(t, codexOpt{untrusted: true, kickoff: "hello"})
	a.waitScreen(0, "Trust this folder?")
	a.sendUntil("1", func() bool { return count(a.events(), "UserPromptSubmit") > 0 })
	a.waitEvent("Stop", 1)
}

// TestCodexApproval: PermissionRequest, then the dialog; y runs the
// command, Esc interrupts the turn (Interrupt, turn_aborted).
func TestCodexApproval(t *testing.T) {
	t.Parallel()
	a := startCodex(t, codexOpt{kickoff: "run codex-approval"})
	a.waitScreen(0, "Press enter to confirm or esc to cancel")
	a.waitEvent("PermissionRequest", 1)
	a.send("y")
	a.waitEvent("Stop", 1)
	if got := a.events(); strings.Join(got, ",") != "SessionStart,UserPromptSubmit,PreToolUse,PermissionRequest,PostToolUse,Stop" {
		t.Fatalf("events %v", got)
	}
	m := a.mark()
	a.send("\x1b[200~run codex-approval\x1b[201~\r")
	a.waitScreen(m, "Would you like to run the following command?")
	a.waitEvent("PermissionRequest", 2)
	a.esc()
	a.waitEvent("Interrupt", 1)
	a.waitScreen(m, "Conversation interrupted")
	waitUntil(t, "turn_aborted", func() bool { return slices.Contains(rolloutTypes(a.rollout()), "turn_aborted") })
	if count(a.events(), "Stop") != 1 {
		t.Fatalf("an interrupted turn fired Stop: %v", a.events())
	}
}

// TestCodexQuestion: the plan-mode menu fires no hook; None of the
// above takes notes.
func TestCodexQuestion(t *testing.T) {
	t.Parallel()
	a := startCodex(t, codexOpt{kickoff: "run question"})
	a.waitScreen(0, "enter to submit answer")
	a.waitScreen(0, "Question 1/1 (1 unanswered)")
	a.send("\x1b[B\x1b[B\t")
	a.send("green\r")
	a.waitEvent("Stop", 1)
	if got := a.events(); strings.Join(got, ",") != "SessionStart,UserPromptSubmit,Stop" {
		t.Fatalf("events %v", got)
	}
	if r := a.logKinds("answer"); len(r) != 1 || r[0]["answer"] != "green" {
		t.Fatalf("answer %v", r)
	}
}

// TestCodexQueueAndClear: `codex queue` reaches the TUI for its thread;
// after /clear a message for the thread left runs out of sight, and the
// new thread reports itself (SessionStart clear) at its first prompt.
func TestCodexQueueAndClear(t *testing.T) {
	t.Parallel()
	a := startCodex(t, codexOpt{kickoff: "first"})
	a.waitEvent("Stop", 1)
	sid := fmt.Sprint(a.hooks()[0]["session_id"])
	if out, err := a.queue("no-such-thread", "x"); err == nil || !strings.Contains(out, "thread not found") {
		t.Fatalf("unknown thread: %v %s", err, out)
	}
	if out, err := a.queue(sid, "second"); err != nil {
		t.Fatalf("queue: %v %s", err, out)
	}
	a.waitEvent("Stop", 2)
	if p := a.logKinds("prompt"); p[1]["via"] != "queue" || p[1]["text"] != "second" {
		t.Fatalf("prompts %v", p)
	}

	a.send("\x1b[200~/clear\x1b[201~\r")
	waitUntil(t, "the thread left", func() bool { return a.rolloutOf(sid) != "" && len(a.logKinds("slash")) > 0 })
	if _, err := a.queue(sid, "lost"); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "the hidden run", func() bool { return len(a.logKinds("queue-hidden")) == 1 })
	if count(a.events(), "SessionStart") != 1 {
		t.Fatalf("SessionStart before the first prompt after /clear: %v", a.events())
	}
	a.send("\x1b[200~third\x1b[201~\r")
	a.waitEvent("Stop", 3)
	var start rec
	for _, h := range a.hooks() {
		if h["hook_event_name"] == "SessionStart" {
			start = h
		}
	}
	if start["source"] != "clear" || start["session_id"] == sid {
		t.Fatalf("SessionStart after /clear %v (old %s)", start, sid)
	}

	// The thread left is unloaded later, under its own id.
	a.waitEvent("SessionEnd", 1)
	for _, h := range a.hooks() {
		if h["hook_event_name"] == "SessionEnd" && (h["reason"] != "other" || h["session_id"] != sid) {
			t.Fatalf("SessionEnd %v, want reason other for %s", h, sid)
		}
	}

	a.send("\x1b[200~/compact\x1b[201~\r")
	a.waitEvent("PostCompact", 1)
	if n := count(a.events(), "SessionStart"); n != 2 {
		t.Fatalf("SessionStart on /compact: %v", a.events())
	}
	a.send("\x1b[200~fourth\x1b[201~\r")
	a.waitEvent("SessionStart", 3)
	if h := a.hooks(); h[len(h)-1]["source"] != "compact" || h[len(h)-1]["session_id"] != start["session_id"] {
		t.Fatalf("after /compact %v", h[len(h)-1])
	}
}

// rolloutOf is the rollout the hooks named for a thread.
func (a *agent) rolloutOf(thread string) string {
	for _, h := range a.hooks() {
		if h["session_id"] == thread {
			p, _ := h["transcript_path"].(string)
			return p
		}
	}
	return ""
}

// TestCodexResume: `resume <id>` continues the rollout; SessionStart
// (resume) at the first prompt. An unknown id fails.
func TestCodexResume(t *testing.T) {
	t.Parallel()
	home, cwd, plugin := newEnv(t)
	a := startCodexIn(t, home, cwd, plugin, codexOpt{kickoff: "first"})
	a.waitEvent("Stop", 1)
	sid := fmt.Sprint(a.hooks()[0]["session_id"])
	a.cmd.Process.Signal(os.Interrupt)
	a.cmd.Process.Kill()
	<-a.done

	b := startCodexIn(t, home, cwd, plugin, codexOpt{args: []string{"resume", sid}, kickoff: "again"})
	b.waitEvent("Stop", 2)
	h := b.hooks()
	var starts []rec
	for _, r := range h {
		if r["hook_event_name"] == "SessionStart" {
			starts = append(starts, r)
		}
	}
	if len(starts) != 2 || starts[1]["source"] != "resume" || starts[1]["session_id"] != sid || starts[1]["transcript_path"] != starts[0]["transcript_path"] {
		t.Fatalf("SessionStarts %v", starts)
	}

	c := startCodexIn(t, home, cwd, plugin, codexOpt{args: []string{"resume", "nope"}})
	if code := c.waitExit(); code != 1 {
		t.Fatalf("unknown id: exit %d", code)
	}
}
