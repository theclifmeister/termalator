package ticker

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/theclifmeister/terminatr/internal/proto"
	"github.com/theclifmeister/terminatr/internal/thread"
)

// clearRig is newRig with auto_clear set to on, and a coordinator idle at
// 50% of its context window (the hint is 40% by default).
func clearRig(t *testing.T, on bool, extra string) *rig {
	t.Helper()
	r := newRig(t)
	cfg := filepath.Join(os.Getenv("TERMINATR_HOME"), "config.toml")
	val := map[bool]string{true: "true", false: "false"}[on]
	if err := os.WriteFile(cfg, []byte(extra+"[projects.demo]\nauto_clear = "+val+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	c := &r.host.sessions[0]
	c.PID, c.State, c.Context, c.ContextWindow = 100, "idle", 100_000, 200_000
	return r
}

// With the setting on, an idle coordinator past the hint with nothing
// waiting is cleared once every condition held for the grace, journaled,
// and not again before ClearEvery.
func TestAutoClearClearsIdleCoordinator(t *testing.T) {
	r := clearRig(t, true, "")
	r.sweep(0)
	r.sweep(DefaultClearGrace - time.Second)
	if len(r.host.cleared) != 0 {
		t.Fatalf("cleared within the grace: %v", r.host.cleared)
	}
	r.sweep(time.Second)
	if want := []string{"s-1"}; !slices.Equal(r.host.cleared, want) {
		t.Fatalf("cleared %v, want %v", r.host.cleared, want)
	}
	if j := r.journal(); !strings.Contains(j, " ticker coordinator.clear demo s-1 at 50% of its context window (hint 40%)") {
		t.Fatalf("journal %q", j)
	}
	if !r.host.still() {
		t.Fatal("still false with nothing waiting")
	}
	// It didn't take (context still high): not again before ClearEvery,
	// and after it only once the grace held again.
	r.sweep(time.Second)
	r.sweep(DefaultClearEvery - 2*time.Second)
	if len(r.host.cleared) != 1 {
		t.Fatalf("cleared again within ClearEvery: %v", r.host.cleared)
	}
	r.sweep(time.Second)
	if len(r.host.cleared) != 2 {
		t.Fatalf("not tried again after ClearEvery: %v", r.host.cleared)
	}
}

// Each safety condition alone holds the clear back; once it lifts, the
// grace starts again.
func TestAutoClearWaitsForEveryCondition(t *testing.T) {
	cases := []struct {
		name string
		set  func(r *rig)
		undo func(r *rig)
	}{
		{"working", func(r *rig) { r.host.set("s-1", "working", "") }, func(r *rig) { r.host.set("s-1", "idle", "") }},
		{"queued prompt", func(r *rig) { r.host.sessions[0].Queued = 1 }, func(r *rig) { r.host.sessions[0].Queued = 0 }},
		{"coordinator question", func(r *rig) { r.host.sessions[0].Question = &proto.Question{} }, func(r *rig) { r.host.sessions[0].Question = nil }},
		{"context below the hint", func(r *rig) { r.host.sessions[0].Context = 70_000 }, func(r *rig) { r.host.sessions[0].Context = 100_000 }},
		{"context unknown", func(r *rig) { r.host.sessions[0].Context = 0 }, func(r *rig) { r.host.sessions[0].Context = 100_000 }},
		{"thread question", func(r *rig) { r.host.sessions[1].Question = &proto.Question{} }, func(r *rig) { r.host.sessions[1].Question = nil }},
		{"thread blocked", func(r *rig) { r.host.set("s-2", "blocked", "permission") }, func(r *rig) { r.host.set("s-2", "working", ""); r.handleAll() }},
		{"inbox item", func(r *rig) {
			if _, err := r.p.AddItem("report", "t-0001", "done", false); err != nil {
				r.t.Fatal(err)
			}
		}, func(r *rig) { r.handleAll() }},
		{"thread needs you", func(r *rig) { needsYou(r, "which colour?") }, func(r *rig) { needsYou(r, "") }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := clearRig(t, true, "")
			c.set(r)
			r.sweep(0)
			r.sweep(time.Hour)
			if len(r.host.cleared) != 0 {
				t.Fatalf("cleared while %s: %v", c.name, r.host.cleared)
			}
			c.undo(r)
			r.sweep(time.Second)
			if len(r.host.cleared) != 0 {
				t.Fatalf("cleared without a new grace: %v", r.host.cleared)
			}
			r.sweep(DefaultClearGrace)
			if len(r.host.cleared) != 1 {
				t.Fatalf("not cleared once free: %v (journal %q)", r.host.cleared, r.journal())
			}
		})
	}
}

func needsYou(r *rig, q string) {
	r.t.Helper()
	if _, err := thread.UpdateStatus(r.p, "t-0001", func(st *thread.Status) error { st.NeedsYou = q; return nil }); err != nil {
		r.t.Fatal(err)
	}
}

// At delivery the clear is dropped when something came up meanwhile; the
// clear's own place in the queue doesn't count.
func TestAutoClearStillAtDelivery(t *testing.T) {
	r := clearRig(t, true, "")
	r.sweep(0)
	r.sweep(DefaultClearGrace)
	if len(r.host.cleared) != 1 {
		t.Fatalf("not cleared: %v", r.host.cleared)
	}
	r.host.sessions[0].Queued = 1
	if !r.host.still() {
		t.Fatal("dropped for its own place in the queue")
	}
	if _, err := r.p.AddItem("report", "t-0001", "done", false); err != nil {
		t.Fatal(err)
	}
	if r.host.still() {
		t.Fatal("delivered over a new inbox item")
	}
}

// Off (the default), paused, or with the hint at 0, the ticker never
// clears; nor when the agent has no clear prompt, which is tried only
// once per ClearEvery.
func TestAutoClearOff(t *testing.T) {
	for name, r := range map[string]*rig{
		"off":    clearRig(t, false, ""),
		"hint 0": clearRig(t, true, "[ui]\ncontext_hint = 0\n"),
	} {
		r.sweep(0)
		for range 5 {
			r.sweep(time.Hour)
		}
		if len(r.host.cleared) != 0 || strings.Contains(r.journal(), "coordinator.clear") {
			t.Fatalf("%s: cleared: %v", name, r.host.cleared)
		}
	}
	r := newRig(t)
	cfg := filepath.Join(os.Getenv("TERMINATR_HOME"), "config.toml")
	if err := os.WriteFile(cfg, []byte("[projects.demo]\npaused = true\nauto_clear = true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	r.host.sessions[0].State, r.host.sessions[0].Context, r.host.sessions[0].ContextWindow = "idle", 190_000, 200_000
	r.sweep(0)
	r.sweep(time.Hour)
	if len(r.host.cleared) != 0 {
		t.Fatalf("cleared while paused: %v", r.host.cleared)
	}

	r = clearRig(t, true, "")
	r.host.clearErr = errors.New("no clear prompt")
	r.sweep(0)
	r.sweep(DefaultClearGrace)
	if strings.Contains(r.journal(), "coordinator.clear") {
		t.Fatalf("journaled a failed clear: %q", r.journal())
	}
}

// A relaunched coordinator (a new pid) waits the grace again.
func TestAutoClearGraceAfterRelaunch(t *testing.T) {
	r := clearRig(t, true, "")
	r.sweep(0)
	r.sweep(DefaultClearGrace - time.Second)
	r.host.sessions[0].PID = 101
	r.sweep(2 * time.Second)
	if len(r.host.cleared) != 0 {
		t.Fatalf("cleared a fresh process: %v", r.host.cleared)
	}
	r.sweep(DefaultClearGrace)
	if len(r.host.cleared) != 1 {
		t.Fatalf("not cleared: %v", r.host.cleared)
	}
}
