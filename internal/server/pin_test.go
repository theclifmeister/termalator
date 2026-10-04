package server

import (
	"os"
	"path/filepath"
	"testing"
)

// TestPinBinary: the pinned copy survives the original being replaced,
// and pinning another build removes the older copy.
func TestPinBinary(t *testing.T) {
	home := t.TempDir()
	bin := filepath.Join(t.TempDir(), "tm")
	os.WriteFile(bin, []byte("build one"), 0o755)
	p1, err := pinBinary(home, bin, "v1+abc+111")
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(p1) != filepath.Join(home, pinDir) {
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
	if p, err := pinBinary(home, bin, "v1+abc+111"); err != nil || p != p1 {
		t.Fatalf("repin: %s %v", p, err)
	}
	p2, err := pinBinary(home, bin, "v2+abc+222")
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(p2); string(b) != "build two" {
		t.Fatalf("second pin reads %q", b)
	}
	if _, err := os.Stat(p1); !os.IsNotExist(err) {
		t.Fatalf("old pin kept: %v", err)
	}
	if _, err := pinBinary(home, filepath.Join(home, "missing"), "v3"); err == nil {
		t.Fatal("pinning a missing binary: no error")
	}
}
