package server

import (
	"os"
	"path/filepath"
	"testing"
)

// TestPinCompanions: the files tm needs beside it (ConPTY on Windows)
// are pinned with it and survive the cleanup of other pins; one the
// source lacks is skipped.
func TestPinCompanions(t *testing.T) {
	defer func(c []string) { companions = c }(companions)
	companions = []string{"conpty.dll", "OpenConsole.exe"}
	src := t.TempDir()
	bin := filepath.Join(src, "tm"+pinExt)
	os.WriteFile(bin, []byte("build one"), 0o755)
	os.WriteFile(filepath.Join(src, "conpty.dll"), []byte("dll one"), 0o644)
	run := t.TempDir()
	pin, err := pinBinary(run, bin)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Dir(pin)
	if b, _ := os.ReadFile(filepath.Join(dir, "conpty.dll")); string(b) != "dll one" {
		t.Fatalf("pinned conpty.dll reads %q", b)
	}
	if _, err := os.Stat(filepath.Join(dir, "OpenConsole.exe")); !os.IsNotExist(err) {
		t.Fatalf("a companion the source lacks: %v", err)
	}
	// A new build with a new companion replaces it; a stray file goes.
	os.WriteFile(filepath.Join(src, "conpty.dll"), []byte("dll two"), 0o644)
	os.WriteFile(filepath.Join(dir, "stray"), nil, 0o600)
	if _, err := pinBinary(run, bin); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "conpty.dll")); string(b) != "dll two" {
		t.Fatalf("pinned conpty.dll reads %q after an upgrade", b)
	}
	if ents, _ := os.ReadDir(dir); len(ents) != 2 {
		t.Fatalf("pin dir holds %v", ents)
	}
}
