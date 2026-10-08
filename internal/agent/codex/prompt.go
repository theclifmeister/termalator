package codex

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
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

// Prompt queues text for the session's thread with `codex queue --thread
// <id> --message <text>`. The running TUI takes it as the user's own
// prompt, at once when idle, after the current turn when one runs, and
// leaves a draft in the composer alone (checked on 0.160; see the
// manifest's [inject]). Exit status 0 means the message is in Codex's
// durable queue, not that the TUI has it yet. A slash command is
// refused so the core pastes it.
//
// A timeout that kills `codex queue` after it wrote the queue may get
// the prompt delivered twice, once queued and once pasted.
func (a *Agent) Prompt(ctx context.Context, t agent.PromptTarget, text string) error {
	if t.AgentSID == "" {
		return ErrNoThread
	}
	if strings.HasPrefix(strings.TrimSpace(text), "/") {
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
