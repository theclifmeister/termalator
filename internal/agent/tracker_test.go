package agent

import (
	"strings"
	"testing"
	"time"
)

// rig drives a tracker through the Claude manifest with a fake clock, so
// every row of the arbitration table (docs/SPEC.md §8.4) and every stale
// case of the spike (§8.6, spikes/claude FINDINGS §2) is one test step.
type rig struct {
	t   *testing.T
	a   Agent
	tr  *Tracker
	now time.Time
	seq uint64
}

func newRig(t *testing.T) *rig {
	r := &rig{t: t, a: claude(t), now: time.Unix(1_000_000, 0)}
	r.tr = NewTracker(func() time.Time { return r.now })
	return r
}

func (r *rig) tick(d time.Duration) { r.now = r.now.Add(d) }

// hook delivers one event 10 ms after the previous step.
func (r *rig) hook(event string, payload map[string]any) {
	r.t.Helper()
	r.tick(10 * time.Millisecond)
	r.seq++
	if payload == nil {
		payload = map[string]any{}
	}
	if _, ok := payload["session_id"]; !ok {
		payload["session_id"] = "sid-1"
	}
	ev := HookEvent{Agent: "claude", Event: event, Payload: payload, Seq: r.seq, At: r.now}
	sigs, _, err := r.a.Hook(ev, nil)
	if err != nil {
		r.t.Fatal(err)
	}
	r.tr.Hook(ev, sigs)
}

func (r *rig) status(state State, reason string) {
	r.tick(10 * time.Millisecond)
	r.tr.Status(&StatusReading{Signal: Signal{Source: "status_file", State: state, Reason: reason}}, r.now, nil)
}

func (r *rig) screen(rule string, state State, reason string) {
	r.tick(100 * time.Millisecond)
	r.tr.Screen(ScreenSignal{Rule: rule, State: state, Reason: reason}, []string{rule})
}

func (r *rig) want(state State, reason, sources string) {
	r.t.Helper()
	m := r.tr.State()
	if m.State != state || m.Reason != reason || (sources != "" && m.Sources != sources) {
		r.t.Fatalf("state %s/%s (%s), want %s/%s (%s)", m.State, m.Reason, m.Sources, state, reason, sources)
	}
}

func TestTrackerHooksOnly(t *testing.T) {
	r := newRig(t)
	r.want(StateUnknown, "", "")
	r.hook("SessionStart", map[string]any{"source": "startup"})
	r.want(StateIdle, "", "hooks")
	r.hook("UserPromptSubmit", nil)
	r.want(StateWorking, "", "hooks")
	r.hook("PreToolUse", map[string]any{"tool_name": "Write"})
	r.hook("PermissionRequest", map[string]any{"tool_name": "Write", "tool_use_id": "toolu_1"})
	r.want(StateBlocked, "permission", "hooks")
	// Approved: the tool runs, a transient edge after the level signal.
	r.hook("PostToolUse", map[string]any{"tool_name": "Write"})
	r.want(StateWorking, "", "hooks")
	r.hook("Stop", nil)
	r.want(StateIdle, "", "hooks")
	r.hook("SessionEnd", map[string]any{"reason": "prompt_input_exit"})
	r.want(StateExited, "", "hooks")
}

// TestTrackerStaleCases: the three Esc cases fire no closing hook (s15).
// The status file, or without it the transcript, or the screen, must
// bring the state back.
func TestTrackerStaleCases(t *testing.T) {
	t.Run("status file clears Esc on a permission dialog", func(t *testing.T) {
		r := newRig(t)
		r.hook("UserPromptSubmit", nil)
		r.status(StateWorking, "")
		r.hook("PermissionRequest", map[string]any{"tool_name": "Write"})
		r.status(StateBlocked, "permission")
		r.want(StateBlocked, "permission", "status_file")
		// Esc: no hook at all; the file goes idle.
		r.status(StateIdle, "")
		r.want(StateIdle, "", "status_file")
	})
	t.Run("status file clears Esc while streaming and during a tool", func(t *testing.T) {
		r := newRig(t)
		r.hook("UserPromptSubmit", nil)
		r.hook("PreToolUse", map[string]any{"tool_name": "Bash"})
		r.status(StateWorking, "")
		r.status(StateIdle, "")
		r.want(StateIdle, "", "status_file")
	})
	t.Run("transcript interrupt without a status file", func(t *testing.T) {
		r := newRig(t)
		r.hook("UserPromptSubmit", nil)
		r.hook("PreToolUse", map[string]any{"tool_name": "Bash"})
		r.want(StateWorking, "", "hooks")
		r.tick(time.Second)
		r.tr.Tail(Signal{Source: "jsonl_tail", State: StateIdle, Reason: "interrupted"})
		r.want(StateIdle, "interrupted", "jsonl_tail")
		// The next prompt's hook is newer than the interrupt.
		r.hook("UserPromptSubmit", nil)
		r.want(StateWorking, "", "hooks")
	})
	t.Run("screen expires a dismissed dialog without status file or transcript", func(t *testing.T) {
		r := newRig(t)
		r.screen("blocked-permission-dialog", StateBlocked, "permission")
		r.hook("PermissionRequest", map[string]any{"tool_name": "Write"})
		r.want(StateBlocked, "permission", "")
		r.screen("idle-title", StateIdle, "")
		r.want(StateIdle, "", "hooks+screen")
	})
	t.Run("a hook stands in for a status file that lags behind", func(t *testing.T) {
		r := newRig(t)
		r.status(StateIdle, "")
		r.hook("UserPromptSubmit", nil)
		r.want(StateWorking, "", "hooks")
		r.status(StateWorking, "")
		r.want(StateWorking, "", "status_file")
	})
	t.Run("a late Notification does not resurrect an answered dialog", func(t *testing.T) {
		r := newRig(t)
		r.status(StateWorking, "")
		r.hook("Notification", map[string]any{"notification_type": "permission_prompt"})
		r.tick(statusLag)
		r.want(StateWorking, "", "status_file")
	})
}

func TestTrackerRanking(t *testing.T) {
	r := newRig(t)
	// The status file beats hooks.
	r.hook("Stop", nil)
	r.status(StateWorking, "")
	r.want(StateWorking, "", "status_file")
	// A visible blocker beats a non-blocked status file (dialogs paint
	// before the file and hooks catch up), and the trust screen has no
	// other source at all.
	r.screen("trust-folder", StateBlocked, "trust")
	r.want(StateBlocked, "trust", "status_file+screen")
	r.screen("idle-title", StateIdle, "")
	r.want(StateWorking, "", "status_file")
	// An untested version, or a missing file, drops to hooks.
	r.tr.Status(nil, time.Time{}, ErrUntestedVersion)
	r.want(StateIdle, "", "hooks")
	if e := r.tr.Explain(); e.StatusErr == "" {
		t.Fatalf("explain should say why the status file is unused: %+v", e)
	}
	// Exit beats everything, including a status file still saying busy.
	r.status(StateWorking, "")
	r.tr.Exited("exit status 0")
	r.want(StateExited, "exit status 0", "exit")
}

func TestTrackerScreenOnly(t *testing.T) {
	r := newRig(t)
	r.screen("working-title-spinner", StateWorking, "")
	r.want(StateWorking, "", "screen")
	// working -> idle needs 3 evaluations in a row (or 700 ms).
	r.screen("idle-prompt-box", StateIdle, "")
	r.want(StateWorking, "", "screen")
	r.screen("idle-prompt-box", StateIdle, "")
	r.want(StateWorking, "", "screen")
	r.screen("idle-prompt-box", StateIdle, "")
	r.want(StateIdle, "", "screen")
	// Blockers are not debounced, and the transcript view abstains.
	r.screen("blocked-question", StateBlocked, "question")
	r.want(StateBlocked, "question", "screen")
	r.screen("transcript-view", StateUnknown, "transcript view")
	r.want(StateUnknown, "", "")
	// One slow evaluation pair covers the 700 ms too.
	r.screen("working-title-spinner", StateWorking, "")
	r.screen("idle-prompt-box", StateIdle, "")
	r.tick(800 * time.Millisecond)
	r.screen("idle-prompt-box", StateIdle, "")
	r.want(StateIdle, "", "screen")
}

func TestTrackerBackground(t *testing.T) {
	r := newRig(t)
	r.hook("UserPromptSubmit", nil)
	r.hook("SubagentStart", map[string]any{"agent_id": "a1", "agent_type": "general-purpose"})
	r.hook("Stop", nil)
	r.want(StateWorking, "background", "hooks+counters")
	// The prompt-suggestion side agent (no agent_type) changes nothing.
	r.hook("SubagentStop", map[string]any{"agent_id": "a9"})
	r.want(StateWorking, "background", "")
	// A subagent's own tool events don't move the state.
	r.hook("PreToolUse", map[string]any{"agent_id": "a1", "tool_name": "Bash"})
	r.want(StateWorking, "background", "")
	r.hook("SubagentStop", map[string]any{"agent_id": "a1", "agent_type": "general-purpose"})
	r.want(StateIdle, "", "hooks")
	// A repeated end can't go below zero.
	r.hook("SubagentStop", map[string]any{"agent_id": "a1", "agent_type": "general-purpose"})
	r.hook("SubagentStart", map[string]any{"agent_id": "a2", "agent_type": "general-purpose"})
	r.want(StateWorking, "background", "")
}

// TestTrackerKickoff: an agent launched with a first prompt reports idle
// at startup, before it takes the prompt; until a source sees it at work
// that idle reads as working/kickoff (docs/SPEC.md §8.4), so a thread just
// started counts against the parallel threads cap.
func TestTrackerKickoff(t *testing.T) {
	r := newRig(t)
	r.tr.AwaitKickoff()
	r.want(StateUnknown, "", "")
	// The trust dialog is a blocker on screen, not the kickoff's turn.
	r.screen("trust-folder", StateBlocked, "trust")
	r.want(StateBlocked, "trust", "screen")
	r.screen("idle-prompt-box", StateIdle, "")
	r.hook("SessionStart", map[string]any{"source": "startup"})
	r.status(StateIdle, "")
	r.want(StateWorking, ReasonKickoff, "status_file+kickoff")
	r.hook("UserPromptSubmit", nil)
	r.want(StateWorking, "", "hooks")
	r.status(StateWorking, "")
	r.status(StateIdle, "")
	r.hook("Stop", nil)
	r.want(StateIdle, "", "hooks")

	// The turn can be over before the tracker looks: the hook still ends
	// the wait.
	r = newRig(t)
	r.tr.AwaitKickoff()
	r.status(StateIdle, "")
	r.want(StateWorking, ReasonKickoff, "")
	r.hook("UserPromptSubmit", nil)
	r.hook("Stop", nil)
	r.tick(2 * time.Second)
	r.status(StateIdle, "")
	r.want(StateIdle, "", "status_file")

	// The status file alone ends it too.
	r = newRig(t)
	r.tr.AwaitKickoff()
	r.status(StateIdle, "")
	r.status(StateWorking, "")
	r.status(StateIdle, "")
	r.want(StateIdle, "", "status_file")

	// An agent that never starts on its kickoff is believed in the end,
	// counted from when it first looked idle.
	r = newRig(t)
	r.tr.AwaitKickoff()
	r.tick(time.Hour) // a slow startup
	r.status(StateIdle, "")
	r.want(StateWorking, ReasonKickoff, "")
	r.tick(kickoffGrace - time.Second)
	r.want(StateWorking, ReasonKickoff, "")
	r.tick(time.Second)
	r.want(StateIdle, "", "status_file")

	// Without a kickoff, idle is idle.
	r = newRig(t)
	r.status(StateIdle, "")
	r.want(StateIdle, "", "status_file")
}

func TestTrackerSessionIDAndTodos(t *testing.T) {
	r := newRig(t)
	r.tr.SetAgentSID("sid-1")
	r.hook("SessionStart", map[string]any{"source": "startup", "session_id": "sid-1"})
	r.hook("PostToolUse", map[string]any{"tool_name": "TaskCreate",
		"tool_input": map[string]any{"subject": "alpha"}, "tool_response": map[string]any{"task": map[string]any{"id": "1"}}})
	r.hook("PostToolUse", map[string]any{"tool_name": "TaskUpdate",
		"tool_input": map[string]any{"taskId": "1", "status": "in_progress"}})
	if got := r.tr.Todos(); len(got) != 1 || got[0].Status != TodoInProgress || got[0].Text != "alpha" {
		t.Fatalf("todos = %+v", got)
	}
	// /clear: SessionEnd(clear) is not an exit; the new id wins; the list resets.
	r.hook("SessionEnd", map[string]any{"reason": "clear", "session_id": "sid-1"})
	ev := HookEvent{Event: "SessionStart", Payload: map[string]any{"source": "clear", "session_id": "sid-2"}, Seq: 100, At: r.now}
	sigs, _, _ := r.a.Hook(ev, nil)
	if !r.tr.Hook(ev, sigs) {
		t.Fatal("session id change not reported")
	}
	r.want(StateIdle, "", "hooks")
	if r.tr.AgentSID() != "sid-2" || len(r.tr.Todos()) != 0 {
		t.Fatalf("sid %q todos %+v", r.tr.AgentSID(), r.tr.Todos())
	}
	// Out-of-order (stale) events are dropped.
	old := HookEvent{Event: "SessionStart", Payload: map[string]any{"session_id": "sid-1"}, Seq: 50, At: r.now}
	sigs, _, _ = r.a.Hook(old, nil)
	r.tr.Hook(old, sigs)
	if r.tr.AgentSID() != "sid-2" {
		t.Fatal("a stale event changed the session id")
	}
	if n := len(r.tr.Explain().Events); n != 5 {
		t.Fatalf("explain keeps %d events, want 5", n)
	}
}

func TestUnwrapArgv(t *testing.T) {
	for _, c := range []struct{ in, want []string }{
		{[]string{"claude", "--resume", "x"}, []string{"claude", "--resume", "x"}},
		{[]string{"/usr/bin/node", "--no-warnings", "/usr/lib/bin/claude", "-c"}, []string{"/usr/lib/bin/claude", "-c"}},
		{[]string{"/bin/sh", "-c", "exec claude --model x"}, []string{"claude", "--model", "x"}},
		{[]string{"/bin/zsh", "-l"}, []string{"/bin/zsh", "-l"}},
		{nil, nil},
	} {
		got := UnwrapArgv(c.in)
		if strings.Join(got, " ") != strings.Join(c.want, " ") {
			t.Errorf("UnwrapArgv(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	if !claude(t).Identify(ProcessInfo{Argv: UnwrapArgv([]string{"node", "/opt/x/claude"})}) {
		t.Error("claude not identified through node")
	}
}
