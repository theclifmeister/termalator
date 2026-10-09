package fsx

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestSwapIn(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tm")
	os.WriteFile(path, []byte("old"), 0o755)
	nb := filepath.Join(dir, ".new")
	os.WriteFile(nb, []byte("new"), 0o600)
	restore, err := SwapIn(nb, path)
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(path); string(b) != "new" {
		t.Fatalf("path holds %q", b)
	}
	if fi, _ := os.Stat(path); runtime.GOOS != "windows" && fi.Mode().Perm()&0o100 == 0 {
		t.Errorf("mode %v is not executable", fi.Mode())
	}
	if restore != nil { // Windows keeps the old file
		if err := restore(); err != nil {
			t.Fatal(err)
		}
		if b, _ := os.ReadFile(path); string(b) != "old" {
			t.Fatalf("restored %q", b)
		}
		if b, _ := os.ReadFile(nb); string(b) != "new" {
			t.Fatalf("new file went to %q", b)
		}
	}
}

func TestSwapInMissingTarget(t *testing.T) {
	dir := t.TempDir()
	nb := filepath.Join(dir, ".new")
	os.WriteFile(nb, []byte("new"), 0o600)
	if _, err := SwapIn(nb, filepath.Join(dir, "tm")); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "tm")); string(b) != "new" {
		t.Fatalf("holds %q", b)
	}
}

func TestCleanAside(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tm[1].exe")
	for _, n := range []string{"tm[1].exe", "tm[1].exe.old-20260101T000000", "tm[1].exe.old-20260101T000000-1", "tm[1].exe.other"} {
		os.WriteFile(filepath.Join(dir, n), nil, 0o600)
	}
	if left := CleanAside(path); left != 0 {
		t.Errorf("%d left", left)
	}
	es, _ := os.ReadDir(dir)
	var got []string
	for _, e := range es {
		got = append(got, e.Name())
	}
	if strings.Join(got, " ") != "tm[1].exe tm[1].exe.other" {
		t.Errorf("left %v", got)
	}
}
