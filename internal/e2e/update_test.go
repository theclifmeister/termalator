//go:build unix

package e2e

// Upgrades (docs/SPEC.md §3.6, Upgrade; §10.1 tm update).

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestSmokeUpgradeInPlace: a new tm renamed over the one the server was
// started from (what tm update and brew upgrade do) leaves the server
// running the old build, and attaching with the new tm still works: the
// client re-execs the server's pinned copy of its binary, not the path
// that now holds the new build.
func TestSmokeUpgradeInPlace(t *testing.T) {
	env := New(t)
	b, err := os.ReadFile(env.Bin)
	if err != nil {
		t.Fatal(err)
	}
	tm := filepath.Join(t.TempDir(), "tm")
	if err := os.WriteFile(tm, b, 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(tm, "server", "start")
	cmd.Env = env.Vars
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("server start: %v\n%s", err, out)
	}
	s := env.Start("printer", "-lines", "3")
	env.WaitFor(s, "ready", wait)

	// Same program, different bytes: a different build id.
	if err := os.WriteFile(tm+".new", append(b, "new build"...), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tm+".new", tm); err != nil {
		t.Fatal(err)
	}
	w := env.WindowCmd(80, 24, tm, "attach", s.ID)
	w.WaitFor("ready", wait)
	env.AssertMirrorsServer(w)

	// This tm is a source build: tm update refuses and says how to update.
	r := env.CLI("update")
	if r.Code != 1 || !strings.Contains(r.Stderr, "git pull && make") {
		t.Fatalf("tm update on a source build: exit %d\n%s%s", r.Code, r.Stdout, r.Stderr)
	}
}
