package codex

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"slices"
	"strings"
	"time"

	"github.com/theclifmeister/terminatr/internal/agent"
)

// queueTimeout bounds one `codex queue`; it took 20-100 ms on 0.160,
// with and without the shared app-server daemon. The core falls back to
// paste on any error, so this must stay short.
const queueTimeout = time.Second

// ErrNoThread means no hook has reported Codex's thread id yet.
var ErrNoThread = errors.New("codex: no thread id yet")

// ErrSlashCommand means the text is a slash command, which `codex
// queue` would hand the model as text: it goes by paste instead.
var ErrSlashCommand = errors.New("codex: a slash command runs only when typed")

// ErrThreadLeft means the session's thread id is the one a /clear left:
// the new thread's id is reported only at its first prompt.
var ErrThreadLeft = errors.New("codex: the thread was left; its successor has no id yet")

// switchCommands start the TUI on another thread. The thread left stays
// loaded: a message queued for it runs there, out of sight (0.160).
var switchCommands = []string{"/clear", "/new", "/resume", "/fork"}

// Typed notes a switch command typed in the pane by hand, which no hook
// reports, as Prompt does one sent through tm: prompts are pasted until
// the hooks report the new thread. A line starting with "/" counts when
// its command is a switch command, could have picked one from the
// command popup (Enter on a partial name runs the entry the popup
// highlights, matched fuzzily), or was edited by keys the core can't
// follow. Counting one too many only pastes prompts until the next
// prompt on the thread (Hook).
func (a *Agent) Typed(t agent.PromptTarget, line string, exact bool) {
	if t.AgentSID != "" && switches(line, exact) {
		a.leave(t.SessionID, t.AgentSID)
	}
}

// switches reports whether line, submitted in the prompt box, may
// switch threads (Typed).
func switches(line string, exact bool) bool {
	f := strings.Fields(line)
	if len(f) == 0 || !strings.HasPrefix(f[0], "/") {
		return false
	}
	if !exact || slices.Contains(switchCommands, f[0]) {
		return true
	}
	if len(f) > 1 {
		return false // arguments: no popup pick
	}
	for _, c := range switchCommands {
		if subsequence(f[0][1:], c[1:]) {
			return true
		}
	}
	return false
}

// subsequence reports whether s's letters appear in t in order.
func subsequence(s, t string) bool {
	for _, r := range s {
		i := strings.IndexRune(t, r)
		if i < 0 {
			return false
		}
		t = t[i+len(string(r)):]
	}
	return true
}

// Prompt queues text for the session's thread with `codex queue --thread
// <id> --message <text>`. The running TUI takes it as the user's own
// prompt, at once when idle, after the current turn when one runs, and
// leaves a draft in the composer alone (checked on 0.160; see the
// manifest's [inject]). Exit status 0 means the message is in Codex's
// durable queue, not that the TUI has it yet. A slash command is
// refused so the core pastes it, and after one that switches threads
// (/clear) so is every prompt until the hooks report the new thread's
// id, at its first prompt.
//
// A timeout that kills `codex queue` after it wrote the queue may get
// the prompt delivered twice, once queued and once pasted.
func (a *Agent) Prompt(ctx context.Context, t agent.PromptTarget, text string) error {
	if t.AgentSID == "" {
		return ErrNoThread
	}
	if a.leftThread(t.SessionID, t.AgentSID) {
		return ErrThreadLeft
	}
	if cmd := strings.TrimSpace(text); strings.HasPrefix(cmd, "/") {
		if slices.Contains(switchCommands, strings.Fields(cmd)[0]) {
			a.leave(t.SessionID, t.AgentSID)
		}
		return ErrSlashCommand
	}
	ctx, cancel := context.WithTimeout(ctx, queueTimeout)
	defer cancel()
	// The = forms keep a text starting with "-" from reading as a flag.
	cmd := exec.CommandContext(ctx, a.m.Launch.Command, "queue", "--thread="+t.AgentSID, "--message="+text)
	cmd.WaitDelay = 100 * time.Millisecond
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Run(); err != nil {
		if msg := strings.TrimSpace(out.String()); msg != "" {
			return fmt.Errorf("codex queue: %w: %s", err, errorLine(msg))
		}
		return fmt.Errorf("codex queue: %w", err)
	}
	return nil
}

// leave notes that session is leaving thread: Prompt pastes until the
// hooks report another.
func (a *Agent) leave(session, thread string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.left == nil {
		a.left = map[string]string{}
	}
	a.left[session] = thread
}

// stay notes that a prompt ran on thread: it is the one shown, so a
// note that a session left it was wrong (a /clear refused mid-turn, a
// popup pick of another command).
func (a *Agent) stay(thread string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for s, t := range a.left {
		if t == thread {
			delete(a.left, s)
		}
	}
}

// leftThread reports whether thread is the one session left, and
// forgets the note once the hooks have reported another.
func (a *Agent) leftThread(session, thread string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	t, ok := a.left[session]
	if ok && t != thread {
		delete(a.left, session)
	}
	return ok && t == thread
}

// errorLine is the last line of out that isn't a warning: Codex prints
// "WARNING: …" lines before its error.
func errorLine(out string) string {
	lines := strings.Split(out, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if l := strings.TrimSpace(lines[i]); l != "" && !strings.HasPrefix(l, "WARNING:") {
			return l
		}
	}
	return lines[len(lines)-1]
}
