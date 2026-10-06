package ticker

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// remoteRig is newRig with coordinator_remote_control set to on.
func remoteRig(t *testing.T, on bool) *rig {
	t.Helper()
	r := newRig(t)
	cfg := filepath.Join(os.Getenv("TERMINATR_HOME"), "config.toml")
	val := map[bool]string{true: "true", false: "false"}[on]
	if err := os.WriteFile(cfg, []byte("[projects.demo]\ncoordinator_remote_control = "+val+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	r.host.sessions[0].PID = 100
	return r
}

func (r *rig) setRemote(on, held bool) {
	r.host.sessions[0].RemoteControl, r.host.sessions[0].RemoteHeld = on, held
}

// With the setting on, a coordinator that reads off gets remote control
// back once it is idle and has read off for the grace, at most once per
// RemoteEvery, journaled.
func TestKeepRemoteTurnsItBackOn(t *testing.T) {
	r := remoteRig(t, true)
	r.sweep(0)
	r.sweep(2 * time.Minute)
	if len(r.host.remote) != 0 {
		t.Fatalf("turned on while working: %v", r.host.remote)
	}
	r.host.set("s-1", "idle", "")
	r.sweep(time.Second)
	if want := []string{"s-1 on"}; !slices.Equal(r.host.remote, want) {
		t.Fatalf("remote %v, want %v", r.host.remote, want)
	}
	if j := r.journal(); !strings.Contains(j, " ticker remote.on demo s-1 prompted") {
		t.Fatalf("journal %q", j)
	}
	// It didn't take (still reads off): tried again only after RemoteEvery.
	r.sweep(DefaultRemoteEvery - time.Second)
	if len(r.host.remote) != 1 {
		t.Fatalf("rate limit: %v", r.host.remote)
	}
	r.sweep(time.Second)
	if len(r.host.remote) != 2 {
		t.Fatalf("not tried again: %v", r.host.remote)
	}
	// On: nothing to do. When it drops, it waits the grace again.
	r.setRemote(true, false)
	r.sweep(DefaultRemoteEvery)
	r.setRemote(false, false)
	r.sweep(time.Second)
	if len(r.host.remote) != 2 {
		t.Fatalf("acted within the grace: %v", r.host.remote)
	}
	r.sweep(DefaultRemoteGrace)
	if len(r.host.remote) != 3 {
		t.Fatalf("drop not repaired: %v", r.host.remote)
	}
}

// A coordinator that just started (or was relaunched: a new pid) reads
// off until it has connected: the grace starts again with the process.
func TestKeepRemoteGraceAfterRelaunch(t *testing.T) {
	r := remoteRig(t, true)
	r.host.set("s-1", "idle", "")
	r.sweep(0)
	r.sweep(DefaultRemoteGrace - time.Second)
	r.host.sessions[0].PID = 101
	r.sweep(2 * time.Second)
	if len(r.host.remote) != 0 {
		t.Fatalf("acted on a fresh process: %v", r.host.remote)
	}
	r.sweep(DefaultRemoteGrace)
	if len(r.host.remote) != 1 {
		t.Fatalf("not turned on: %v", r.host.remote)
	}
}

// The user's own off holds: the ticker doesn't fight it, and a queued
// prompt or a busy coordinator waits.
func TestKeepRemoteRespectsManualOff(t *testing.T) {
	r := remoteRig(t, true)
	r.host.set("s-1", "idle", "")
	r.setRemote(false, true)
	r.sweep(0)
	r.sweep(time.Hour)
	if len(r.host.remote) != 0 {
		t.Fatalf("fought the user's off: %v", r.host.remote)
	}
	r.setRemote(false, false)
	r.host.sessions[0].Queued = 1
	r.sweep(time.Second)
	r.sweep(time.Hour)
	if len(r.host.remote) != 0 {
		t.Fatalf("pasted over a queued prompt: %v", r.host.remote)
	}
	r.host.sessions[0].Queued = 0
	r.sweep(time.Second)
	if len(r.host.remote) != 1 {
		t.Fatalf("not turned on once free: %v", r.host.remote)
	}
}

// With the setting off the ticker never touches remote control.
func TestKeepRemoteSettingOff(t *testing.T) {
	r := remoteRig(t, false)
	r.host.set("s-1", "idle", "")
	for range 5 {
		r.sweep(time.Hour)
	}
	if len(r.host.remote) != 0 || strings.Contains(r.journal(), "remote.on") {
		t.Fatalf("acted with the setting off: %v", r.host.remote)
	}
}
