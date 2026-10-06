package server

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// TestPinBinary: the pinned copy survives the original being replaced,
// and pinning another build removes the older copy.
func TestPinBinary(t *testing.T) {
	run := t.TempDir()
	bin := filepath.Join(t.TempDir(), "tm")
	os.WriteFile(bin, []byte("build one"), 0o755)
	p1, err := pinBinary(run, bin, "v1+abc+111")
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(p1) != filepath.Join(run, pinDir) {
		t.Fatalf("pinned at %s", p1)
	}
	// An upgrade renames a new binary over the old one.
	next := bin + ".new"
	os.WriteFile(next, []byte("build two"), 0o755)
	if err := os.Rename(next, bin); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(p1); string(b) != "build one" {
		t.Fatalf("pinned copy reads %q after the upgrade", b)
	}
	// Pinning again with the same build reuses it.
	if p, err := pinBinary(run, bin, "v1+abc+111"); err != nil || p != p1 {
		t.Fatalf("repin: %s %v", p, err)
	}
	p2, err := pinBinary(run, bin, "v2+abc+222")
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(p2); string(b) != "build two" {
		t.Fatalf("second pin reads %q", b)
	}
	if _, err := os.Stat(p1); !os.IsNotExist(err) {
		t.Fatalf("old pin kept: %v", err)
	}
	if _, err := pinBinary(run, filepath.Join(run, "missing"), "v3"); err == nil {
		t.Fatal("pinning a missing binary: no error")
	}
}

// TestRemoveLegacyPins: the old server-bin directory goes, except a file a
// live process runs.
func TestRemoveLegacyPins(t *testing.T) {
	if _, err := processCommands(); err != nil {
		t.Skip("cannot list processes: ", err)
	}
	home := t.TempDir()
	dir := filepath.Join(home, legacyPinDir)
	os.MkdirAll(dir, 0o700)
	idle := filepath.Join(dir, "tm-old")
	os.WriteFile(idle, []byte("x"), 0o700)
	removeLegacyPins(home)
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("legacy dir kept: %v", err)
	}

	os.MkdirAll(dir, 0o700)
	os.WriteFile(idle, []byte("x"), 0o700)
	live := filepath.Join(dir, "tm-live")
	self, err := os.Executable()
	if err != nil {
		t.Skip(err)
	}
	if err := copyFile(self, live); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(live, "-test.run=^TestPinHelperSleep$")
	cmd.Env = append(os.Environ(), "TM_PIN_HELPER=1")
	if err := cmd.Start(); err != nil {
		t.Skip("cannot run copied test binary: ", err)
	}
	defer func() { cmd.Process.Kill(); cmd.Wait() }()
	time.Sleep(200 * time.Millisecond)
	removeLegacyPins(home)
	if _, err := os.Stat(live); err != nil {
		t.Fatalf("running binary removed: %v", err)
	}
	if _, err := os.Stat(idle); !os.IsNotExist(err) {
		t.Fatalf("idle pin kept: %v", err)
	}
}

// TestPinHelperSleep is the process TestRemoveLegacyPins keeps running.
func TestPinHelperSleep(t *testing.T) {
	if os.Getenv("TM_PIN_HELPER") == "1" {
		time.Sleep(time.Minute)
	}
}
