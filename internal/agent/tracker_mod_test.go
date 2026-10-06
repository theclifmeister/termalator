package agent

import (
	"testing"
	"time"
)

func (r *rig) mod(state State, reason, event string) {
	r.tick(10 * time.Millisecond)
	r.tr.Mod(state, reason, event)
}

// TestTrackerMod: in a session with the mod, the mod's word decides
// while its heartbeat holds (docs/SPEC.md §8.4 rule 0); the status file
// and the screen don't move it, and the hooks' level states are ignored.
func TestTrackerMod(t *testing.T) {
	r := newRig(t)
	r.tr.ExpectMod()
	// Before the mod is heard from, the other sources decide; SessionStart's
	// idle is not one of them in a mod session.
	r.hook("SessionStart", map[string]any{"source": "startup"})
	r.screen("idle-prompt-box", StateIdle, "")
	r.want(StateIdle, "", "screen")
	if ev := r.tr.Explain().Events; len(ev) != 1 || ev[0].Signals[len(ev[0].Signals)-1] != "idle (ignored: the mod reports state)" {
		t.Fatalf("events %+v", ev)
	}

	r.mod(StateIdle, "", "session.start")
	r.want(StateIdle, "", "mod")
	r.mod(StateWorking, "", "turn.start")
	// A busy status file after a slash command, a stale idle screen:
	// neither moves the mod's state (T59).
	r.status(StateIdle, "")
	r.screen("idle-prompt-box", StateIdle, "")
	r.want(StateWorking, "", "mod")
	r.mod(StateIdle, "", "turn.complete")
	r.status(StateWorking, "")
	r.want(StateIdle, "", "mod")

	// Background subagents and the exit hook still count.
	r.hook("SubagentStart", map[string]any{"agent_type": "general-purpose", "agent_id": "a1"})
	r.want(StateWorking, "background", "mod+counters")
	r.hook("SubagentStop", map[string]any{"agent_type": "general-purpose", "agent_id": "a1"})
	r.want(StateIdle, "", "mod")
	r.hook("SessionEnd", map[string]any{"reason": "prompt_input_exit"})
	r.want(StateExited, "", "hooks")

	e := r.tr.Explain()
	if e.Mod == nil || !e.Mod.Live || e.Mod.Event != "turn.complete" || e.Mod.State != StateIdle {
		t.Fatalf("explain mod %+v", e.Mod)
	}
}

// TestTrackerModBlocked: what the mod can't see. A permission granted
// fires nothing until the tool ends, so a newer working status file or
// screen ends the mod's blocked; a dialog no mod event covers shows on
// screen while the mod says idle.
func TestTrackerModBlocked(t *testing.T) {
	r := newRig(t)
	r.tr.ExpectMod()
	r.status(StateWorking, "")
	r.mod(StateWorking, "", "turn.start")
	r.mod(StateBlocked, "permission", "PermissionRequest")
	r.screen("working-title-spinner", StateWorking, "") // the dialog closed, the spinner is back
	r.want(StateWorking, "", "mod+screen")

	r = newRig(t)
	r.tr.ExpectMod()
	r.screen("working-title-spinner", StateWorking, "")
	r.mod(StateBlocked, "permission", "PermissionRequest")
	r.status(StateBlocked, "permission")
	r.want(StateBlocked, "permission", "mod")
	r.status(StateWorking, "")
	r.want(StateWorking, "", "mod+status_file")
	r.mod(StateWorking, "", "PostToolUse")
	r.want(StateWorking, "", "mod")

	r.mod(StateIdle, "", "turn.complete")
	r.screen("blocked-question", StateBlocked, "question")
	r.want(StateBlocked, "question", "mod+screen")
}

// TestTrackerModHeartbeat: the mod's word stands for ModTimeout after it
// was last heard from; beats keep it; then the other sources decide,
// and they decide again as soon as the mod is heard from.
func TestTrackerModHeartbeat(t *testing.T) {
	r := newRig(t)
	r.tr.ExpectMod()
	r.mod(StateWorking, "", "turn.start")
	r.status(StateIdle, "")
	for range 5 {
		r.tick(ModBeat)
		r.mod(StateWorking, "", "beat")
	}
	r.want(StateWorking, "", "mod")
	if ev := r.tr.Explain().Events; len(ev) != 1 {
		t.Fatalf("a beat with the same state was recorded as an event: %+v", ev)
	}
	r.tick(ModTimeout)
	r.want(StateIdle, "", "status_file")
	if m := r.tr.Explain().Mod; m == nil || m.Live {
		t.Fatalf("explain mod %+v, want not live", m)
	}
	r.mod(StateBlocked, "question", "tool.call")
	r.want(StateBlocked, "question", "mod")
}

// TestTrackerNoMod: a session without the mod is as before: no mod in
// explain, the hooks' level states count.
func TestTrackerNoMod(t *testing.T) {
	r := newRig(t)
	r.hook("SessionStart", map[string]any{"source": "startup"})
	r.want(StateIdle, "", "hooks")
	if r.tr.Explain().Mod != nil {
		t.Fatal("explain shows a mod")
	}
}
