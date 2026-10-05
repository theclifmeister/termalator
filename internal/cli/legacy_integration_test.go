package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"golang.org/x/sys/unix"

	"github.com/theclifmeister/termilator/internal/legacy"
)

// legacyRun runs tm with HOME = home and no TERMILATOR_HOME, as a user's
// shell does, plus env.
func legacyRun(t *testing.T, home string, env []string, args ...string) (int, string) {
	t.Helper()
	cmd := exec.Command(tmBin, args...)
	cmd.Dir = home
	cmd.Env = append([]string{"PATH=" + os.Getenv("PATH"), "HOME=" + home,
		"TERMILATOR_TEST_OWNER=" + strconv.Itoa(os.Getpid())}, env...)
	out, err := cmd.CombinedOutput()
	code := 0
	if x, ok := err.(*exec.ExitError); ok {
		code = x.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	return code, string(out)
}

// TestBinaryLegacyHome: a v0.1.0 (Termalator) state directory moves to
// ~/.termilator on the first command, but not while a Termalator server
// holds it; TERMALATOR_* variables are still read.
func TestBinaryLegacyHome(t *testing.T) {
	if testing.Short() {
		t.Skip("binary tests are skipped with -short")
	}
	if tmBin == "" {
		t.Fatal("tm binary was not built (see the TestMain output)")
	}
	home := t.TempDir()
	if r, err := filepath.EvalSymlinks(home); err == nil {
		home = r
	}
	old, nu := filepath.Join(home, ".termalator"), filepath.Join(home, ".termilator")

	// TERMALATOR_HOME still names the state directory.
	if code, out := legacyRun(t, home, []string{"TERMALATOR_HOME=" + old}, "project", "new", "Demo"); code != 0 {
		t.Fatalf("project new: exit %d: %s", code, out)
	}
	if _, err := os.Stat(filepath.Join(old, "projects", "demo")); err != nil {
		t.Fatalf("TERMALATOR_HOME ignored: %v", err)
	}

	// A Termalator server holds the old home: nothing moves.
	t.Setenv("HOME", home)
	t.Setenv("XDG_RUNTIME_DIR", "")
	t.Setenv("TERMILATOR_SOCKET", "")
	p := legacy.OldPaths(old)
	if err := os.MkdirAll(p.RunDir, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(p.RunDir) })
	os.WriteFile(p.PID, []byte("4242\n"), 0o600)
	f, err := os.OpenFile(p.Lock, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	code, out := legacyRun(t, home, nil, "project", "list")
	if code != ExitRefused || !strings.Contains(out, "Termalator server (pid 4242) still runs") || !strings.Contains(out, "tm server restart") {
		t.Fatalf("with an old server: exit %d: %s", code, out)
	}
	code, out = legacyRun(t, home, nil, "doctor", "--json")
	if !strings.Contains(out, `"group": "rename"`) || !strings.Contains(out, "pid 4242") {
		t.Fatalf("doctor: exit %d: %s", code, out)
	}
	if _, err := os.Stat(nu); err == nil {
		t.Fatal("moved while the old server runs")
	}
	f.Close()

	// The server is gone: the next command moves the home.
	code, out = legacyRun(t, home, nil, "project", "list")
	if code != 0 || !strings.Contains(out, "moved "+old+" to "+nu) || !strings.Contains(out, "demo") {
		t.Fatalf("after the old server: exit %d: %s", code, out)
	}
	if _, err := os.Stat(filepath.Join(nu, "projects", "demo")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Fatal("old home still there")
	}
	// Once only.
	if code, out := legacyRun(t, home, nil, "project", "list"); code != 0 || strings.Contains(out, "moved") {
		t.Fatalf("second run: exit %d: %s", code, out)
	}
}

// TestBinaryLegacyRestart: tm server restart stops a server that runs on
// ~/.termalator, moves the home and starts the new server on
// ~/.termilator.
func TestBinaryLegacyRestart(t *testing.T) {
	if testing.Short() {
		t.Skip("binary tests are skipped with -short")
	}
	if tmBin == "" {
		t.Fatal("tm binary was not built (see the TestMain output)")
	}
	// Short, so the run dir stays under the home (no /tmp fallback).
	home, err := os.MkdirTemp("/tmp", "tl")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(home) })
	if r, err := filepath.EvalSymlinks(home); err == nil {
		home = r
	}
	old, nu := filepath.Join(home, ".termalator"), filepath.Join(home, ".termilator")
	t.Cleanup(func() {
		legacyRun(t, home, nil, "server", "stop", "--yes")
		legacyRun(t, home, []string{"TERMILATOR_HOME=" + old}, "server", "stop", "--yes")
	})
	// The old server: a server whose home is ~/.termalator.
	if code, out := legacyRun(t, home, []string{"TERMILATOR_HOME=" + old}, "server", "start"); code != 0 {
		t.Fatalf("server start: exit %d: %s", code, out)
	}
	code, out := legacyRun(t, home, nil, "server", "restart", "--yes")
	if code != 0 || !strings.Contains(out, "stopped") || !strings.Contains(out, "moved "+old+" to "+nu) || !strings.Contains(out, "started") {
		t.Fatalf("server restart: exit %d: %s", code, out)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Fatal("old home still there")
	}
	code, out = legacyRun(t, home, nil, "server", "status", "--json")
	if code != 0 || !strings.Contains(out, nu) {
		t.Fatalf("server status: exit %d: %s", code, out)
	}
}
