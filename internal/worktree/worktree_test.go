package worktree

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func run(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@t", "-c", "init.defaultBranch=main"}, args...)...)
	cmd.Dir = dir
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, b)
	}
}

func TestCreateRemove(t *testing.T) {
	root := t.TempDir()
	origin, repo := filepath.Join(root, "origin.git"), filepath.Join(root, "repo")
	run(t, root, "init", "--bare", "-q", origin)
	run(t, root, "clone", "-q", origin, repo)
	os.WriteFile(filepath.Join(repo, "f"), []byte("x"), 0o644)
	run(t, repo, "add", ".")
	run(t, repo, "commit", "-q", "-m", "c")
	run(t, repo, "push", "-q", "origin", "HEAD:main")
	run(t, repo, "remote", "set-head", "origin", "main")

	dir := filepath.Join(root, "wt", "t-0001-x")
	base, err := Create(repo, dir, "tm/demo/t-0001-x", "")
	if err != nil || base != "origin/main" {
		t.Fatalf("create: %q %v", base, err)
	}
	if !BranchExists(repo, "tm/demo/t-0001-x") {
		t.Fatal("no branch")
	}
	if cd, err := CommonDir(dir); err != nil || filepath.Base(cd) != ".git" {
		t.Fatalf("common dir %q %v", cd, err)
	}
	os.WriteFile(filepath.Join(dir, "dirty"), []byte("x"), 0o644)
	if err := Remove(repo, dir); !errors.Is(err, ErrDirty) {
		t.Fatalf("dirty remove: %v", err)
	}
	os.Remove(filepath.Join(dir, "dirty"))
	if err := Remove(repo, dir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatal("worktree still there")
	}
	// Restore brings it back on its branch; removal by hand is pruned.
	if err := Restore(repo, dir, "tm/demo/t-0001-x"); err != nil {
		t.Fatal(err)
	}
	os.RemoveAll(dir)
	if err := Remove(repo, dir); err != nil {
		t.Fatalf("remove after rm -rf: %v", err)
	}
	if PRState(repo, "tm/demo/t-0001-x") == "MERGED" {
		t.Fatal("a local repo has no merged PR")
	}
}

func TestNoOrigin(t *testing.T) {
	repo := t.TempDir()
	run(t, repo, "init", "-q")
	run(t, repo, "commit", "-q", "--allow-empty", "-m", "c")
	if b, err := DefaultBase(repo); err != nil || b != "HEAD" {
		t.Fatalf("%q %v", b, err)
	}
}
