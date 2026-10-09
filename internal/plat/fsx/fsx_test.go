package fsx

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"testing"

	"github.com/theclifmeister/terminatr/internal/plat/caps"
)

// entries lists dir's names.
func entries(t *testing.T, dir string) []string {
	t.Helper()
	es, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range es {
		names = append(names, e.Name())
	}
	return names
}

func TestWriteAtomic(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "f.json")
	for _, c := range []struct {
		data string
		perm fs.FileMode
	}{{"one", 0o600}, {"two", 0o644}, {"three", 0o400}} {
		if err := WriteAtomic(p, []byte(c.data), c.perm); err != nil {
			t.Fatal(err)
		}
		b, err := os.ReadFile(p)
		if err != nil || string(b) != c.data {
			t.Fatalf("read %q, %v; want %q", b, err, c.data)
		}
		if runtime.GOOS != "windows" {
			if fi, _ := os.Stat(p); fi.Mode().Perm() != c.perm {
				t.Fatalf("mode %o, want %o", fi.Mode().Perm(), c.perm)
			}
		}
	}
	if got := entries(t, dir); !slices.Equal(got, []string{"f.json"}) {
		t.Fatalf("dir holds %q", got)
	}
}

func TestStreamFailureKeepsOld(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "f")
	if err := WriteAtomic(p, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	boom := errors.New("boom")
	err := Stream(p, 0o644, func(w io.Writer) error {
		w.Write([]byte("half"))
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("err %v, want boom", err)
	}
	if b, _ := os.ReadFile(p); string(b) != "old" {
		t.Fatalf("path holds %q", b)
	}
	if got := entries(t, dir); !slices.Equal(got, []string{"f"}) {
		t.Fatalf("temp file left: %q", got)
	}
}

func TestReplace(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "bin")
	var seen string
	err := Replace(p, 0o700, func(tmp string) error {
		seen = tmp
		if filepath.Dir(tmp) != dir || filepath.Base(tmp)[0] != '.' {
			t.Errorf("tmp %s: want a hidden name in %s", tmp, dir)
		}
		return os.WriteFile(tmp, []byte("x"), 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(p); string(b) != "x" {
		t.Fatalf("path holds %q", b)
	}
	if _, err := os.Stat(seen); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("tmp still there: %v", err)
	}
	if err := Replace(p, 0o700, func(string) error { return errors.New("no") }); err == nil {
		t.Fatal("create's error was dropped")
	}
	if err := Replace(filepath.Join(dir, "missing", "f"), 0o600, func(tmp string) error {
		return os.WriteFile(tmp, nil, 0o600)
	}); err == nil {
		t.Fatal("wrote into a missing directory")
	}
}

func TestPrivateDir(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "a", "run")
	if err := EnsurePrivateDir(dir); err != nil {
		t.Fatal(err)
	}
	if err := CheckPrivate(filepath.Join(base, "none")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("missing dir: %v", err)
	}
	f := filepath.Join(base, "file")
	os.WriteFile(f, nil, 0o600)
	if err := CheckPrivate(f); err == nil {
		t.Fatal("a file passed")
	}
	if runtime.GOOS == "windows" {
		return
	}
	if err := os.Chmod(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := EnsurePrivateDir(dir); err == nil {
		t.Fatal("mode 0750 passed")
	}
	os.Chmod(dir, 0o700)
	link := filepath.Join(base, "link")
	if err := os.Symlink(dir, link); err != nil {
		t.Fatal(err)
	}
	if err := CheckPrivate(link); err == nil {
		t.Fatal("a symlink passed")
	}
}

func TestLinkRoleFile(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte("role"), 0o644)
	os.WriteFile(filepath.Join(dir, "CLAUDE.md"), []byte("stale"), 0o644)
	for range 2 {
		if err := LinkRoleFile(dir, "CLAUDE.md", "AGENTS.md"); err != nil {
			t.Fatal(err)
		}
		want := "role"
		if runtime.GOOS == "windows" {
			want = "@AGENTS.md\n" // an import line, not a symlink
		}
		if b, _ := os.ReadFile(filepath.Join(dir, "CLAUDE.md")); string(b) != want {
			t.Fatalf("CLAUDE.md reads %q, want %q", b, want)
		}
	}
}

func TestRel(t *testing.T) {
	for _, c := range []struct {
		root, p, rel string
		ok           bool
	}{
		{"/home/u", "/home/u", ".", true},
		{"/home/u", "/home/u/", ".", true},
		{"/home/u/", "/home/u/src/x", "src/x", true},
		{"/home/u", "/home/u2/x", "", false},
		{"/home/u", "/home", "", false},
		{"/", "/etc", "etc", true},
		{"", "/x", "", false},
		{"/home/u", "", "", false},
		{"/home/u", "/home/u/../v", "", false},
	} {
		rel, ok := Rel(filepath.FromSlash(c.root), filepath.FromSlash(c.p))
		if ok != c.ok || rel != filepath.FromSlash(c.rel) {
			t.Errorf("Rel(%q, %q) = %q, %v; want %q, %v", c.root, c.p, rel, ok, c.rel, c.ok)
		}
	}
	if Under("/Home/U/x", "/home/u") != caps.CaseFold {
		t.Errorf("case folding: want %v", caps.CaseFold)
	}
}

func TestSamePath(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "f")
	os.WriteFile(f, nil, 0o600)
	link := filepath.Join(dir, "l")
	if err := os.Symlink(f, link); err != nil {
		t.Skip(err)
	}
	if !SamePath(f, link) || !SamePath(f, filepath.Join(dir, ".", "f")) {
		t.Fatal("same file, different names")
	}
	if SamePath(f, filepath.Join(dir, "g")) {
		t.Fatal("different files")
	}
	if m := filepath.Join(dir, "missing"); Canonical(m) != m {
		t.Fatalf("Canonical(%s) = %s", m, Canonical(m))
	}
}

func TestTempRoots(t *testing.T) {
	roots := TempRoots()
	if len(roots) == 0 || roots[0] != os.TempDir() {
		t.Fatalf("TempRoots = %q", roots)
	}
}
