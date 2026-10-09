package session

import (
	"errors"
	"fmt"
	"os/exec"
	"slices"
	"strings"
	"time"
)

// Drift (docs/SPEC.md §8.8). An agent release can rename a flag or a
// config key, change a hook payload or a dialog's text, and tm would
// degrade without a word: state from the screen alone, prompts pasted.
// The version is never a gate; instead the session notes what the agent
// does that its manifest doesn't expect, once per kind, in the server
// log with the agent's version, and in tm agent explain ("drift").

const (
	// hookSilence: the screen has shown the agent working this long and
	// no hook event came. Both agents fire one before any work (Claude's
	// SessionStart, Codex's UserPromptSubmit).
	hookSilence = 2 * time.Minute
	// earlyExit: an agent that exits this soon after its launch likely
	// refused it (an unknown flag, a config value it can't read).
	earlyExit = 20 * time.Second
	maxDrift  = 8
)

// drift notes msg once per key.
func (s *Session) drift(rt *agentRT, key, msg string) {
	rt.mu.Lock()
	if rt.drifted[key] || len(rt.drifted) >= maxDrift {
		rt.mu.Unlock()
		return
	}
	if rt.drifted == nil {
		rt.drifted = map[string]bool{}
	}
	rt.drifted[key] = true
	rt.driftNotes = append(rt.driftNotes, msg)
	v := rt.version
	rt.mu.Unlock()
	if v == "" {
		v = "(version unknown)"
	}
	last := ""
	if rt.man != nil && rt.man.Identify.LastTested != "" {
		last = ", last tested " + rt.man.Identify.LastTested
	}
	s.cfg.Logf("session %s: agent drift: %s %s%s: %s (docs/SPEC.md §8.8)", s.cfg.ID, rt.a.Name(), v, last, msg)
}

// SetAgentVersion records the agent's version as its version_args print
// it, for the drift notes and tm agent explain; a status file that names
// one wins.
func (s *Session) SetAgentVersion(v string) {
	rt := s.agentRT()
	if rt == nil || v == "" {
		return
	}
	rt.mu.Lock()
	if rt.version == "" {
		rt.version = v
	}
	rt.mu.Unlock()
}

// checkHook notes a hook event the manifest doesn't register, and one
// without the agent's session id.
func (s *Session) checkHook(rt *agentRT, event string, payload map[string]any) {
	if rt.man == nil {
		return
	}
	if evs := rt.man.HookEvents(); len(evs) > 0 && !slices.Contains(evs, event) {
		s.drift(rt, "hook-event:"+event, fmt.Sprintf("hook event %q is none tm registered", event))
	}
	if f := rt.src.SessionField; f != "" {
		if id, _ := payload[f].(string); id == "" {
			s.drift(rt, "hook-session", fmt.Sprintf("hook event %q has no %s", event, f))
		}
	}
}

// checkHookSilence notes an agent seen working on screen for hookSilence
// without a single hook event: its hooks didn't load or don't run.
func (s *Session) checkHookSilence(rt *agentRT, now time.Time) {
	if rt.observed || rt.man == nil || len(rt.man.HookEvents()) == 0 || rt.seq.Load() > 0 {
		return
	}
	rt.mu.Lock()
	since := rt.workingSince
	rt.mu.Unlock()
	if since.IsZero() || now.Sub(since) < hookSilence {
		return
	}
	s.drift(rt, "no-hooks", fmt.Sprintf("working on screen for %s and no hook event came: its hooks may have changed; state comes from the screen",
		now.Sub(since).Round(time.Second)))
}

// checkExit notes an agent that exited soon after its launch, with the
// last lines it left on screen (an unknown flag, a config error).
func (s *Session) checkExit(rt *agentRT, status string, rows []string) {
	if rt.observed {
		return
	}
	after := time.Since(rt.started)
	if after >= earlyExit {
		return
	}
	var last []string
	for i := len(rows) - 1; i >= 0 && len(last) < 3; i-- {
		if l := strings.TrimSpace(rows[i]); l != "" {
			last = append([]string{l}, last...)
		}
	}
	s.drift(rt, "early-exit", fmt.Sprintf("exited (%s) %s after its launch; last on screen: %q",
		status, after.Round(time.Second), strings.Join(last, " / ")))
}

// checkChannel notes a prompt channel whose command failed: the agent
// refused it (a subcommand or flag gone), not a refusal the agent's
// adapter chose (no thread id yet, a slash command), nor a socket away.
func (s *Session) checkChannel(rt *agentRT, err error) {
	var exit *exec.ExitError
	if errors.As(err, &exit) || errors.Is(err, exec.ErrNotFound) {
		s.drift(rt, "channel", "prompt channel failed, pasting instead: "+err.Error())
	}
}
