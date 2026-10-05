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

// TestUnsaved: uncommitted changes and commits no remote has are
// unsaved; a pushed commit, or one the merged PR's head had, is not.
func TestUnsaved(t *testing.T) {
	root := t.TempDir()
	origin, repo := filepath.Join(root, "origin.git"), filepath.Join(root, "repo")
	run(t, root, "init", "--bare", "-q", origin)
	run(t, root, "clone", "-q", origin, repo)
	os.WriteFile(filepath.Join(repo, "f"), []byte("x"), 0o644)
	run(t, repo, "add", ".")
	run(t, repo, "commit", "-q", "-m", "c")
	run(t, repo, "push", "-q", "origin", "HEAD:main")
	run(t, repo, "fetch", "-q", "origin")
	dir := filepath.Join(root, "wt")
	if _, err := Create(repo, dir, "tm/x", "origin/main"); err != nil {
		t.Fatal(err)
	}
	check := func(pushed, want string) {
		t.Helper()
		if got, err := Unsaved(dir, pushed); err != nil || got != want {
			t.Fatalf("unsaved %q %v, want %q", got, err, want)
		}
	}
	check("", "")
	os.WriteFile(filepath.Join(dir, "g"), []byte("y"), 0o644)
	check("", "uncommitted changes")
	run(t, dir, "add", ".")
	run(t, dir, "commit", "-q", "-m", "g")
	check("", "1 unpushed commit")
	run(t, dir, "push", "-q", "origin", "tm/x")
	check("", "")
	// The merge deleted the remote branch: its head still counts.
	head := revParse(t, dir)
	run(t, dir, "push", "-q", "origin", ":tm/x")
	run(t, dir, "fetch", "-q", "--prune", "origin")
	check("", "1 unpushed commit")
	check(head, "")
	check("0123456789012345678901234567890123456789", "1 unpushed commit")
	os.RemoveAll(dir)
	check("", "")
}

func revParse(t *testing.T, dir string) string {
	t.Helper()
	out, err := exec.Command("git", "-C", dir, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	return string(out[:40])
}

// TestMergedBase: a branch whose commits are all on origin's default
// branch, or on the checked-out branch of a repo without origin, is
// merged; a squash-merged or unmerged one is not.
func TestMergedBase(t *testing.T) {
	commit := func(dir, name string) {
		t.Helper()
		os.WriteFile(filepath.Join(dir, name), []byte(name), 0o644)
		run(t, dir, "add", ".")
		run(t, dir, "commit", "-q", "-m", name)
	}
	check := func(repo, branch, want string) {
		t.Helper()
		if got := MergedBase(repo, branch); got != want {
			t.Fatalf("MergedBase(%s) = %q, want %q", branch, got, want)
		}
	}

	// No remote: fast-forward, merge commit, squash, unmerged.
	repo := t.TempDir()
	run(t, repo, "init", "-q")
	commit(repo, "a")
	for _, b := range []string{"ff", "merge", "squash", "open"} {
		run(t, repo, "checkout", "-q", "-b", "tm/x/"+b, "main")
		commit(repo, b)
	}
	run(t, repo, "checkout", "-q", "main")
	run(t, repo, "merge", "-q", "--ff-only", "tm/x/ff")
	run(t, repo, "merge", "-q", "--no-ff", "-m", "m", "tm/x/merge")
	run(t, repo, "merge", "-q", "--squash", "tm/x/squash")
	run(t, repo, "commit", "-q", "-m", "squash")
	check(repo, "tm/x/ff", "main")
	check(repo, "tm/x/merge", "main")
	check(repo, "tm/x/squash", "")
	check(repo, "tm/x/open", "")
	// The base is the checked-out branch, which isn't its own base.
	run(t, repo, "checkout", "-q", "tm/x/open")
	check(repo, "tm/x/open", "")
	check(repo, "tm/x/ff", "")

	// With origin: merged on the remote (as last fetched) while the local
	// main is stale; a merge nobody fetched yet is not seen.
	root := t.TempDir()
	origin, clone, other := filepath.Join(root, "origin.git"), filepath.Join(root, "repo"), filepath.Join(root, "other")
	run(t, root, "init", "--bare", "-q", origin)
	run(t, root, "clone", "-q", origin, clone)
	commit(clone, "a")
	run(t, clone, "push", "-q", "origin", "HEAD:main")
	run(t, clone, "remote", "set-head", "origin", "main")
	run(t, clone, "checkout", "-q", "-b", "tm/x/pr")
	commit(clone, "pr")
	run(t, clone, "push", "-q", "origin", "tm/x/pr")
	run(t, clone, "checkout", "-q", "main")
	run(t, root, "clone", "-q", origin, other)
	run(t, other, "merge", "-q", "--no-ff", "-m", "Merge PR", "origin/tm/x/pr")
	run(t, other, "push", "-q", "origin", "main")
	check(clone, "tm/x/pr", "")
	run(t, clone, "fetch", "-q", "origin")
	check(clone, "tm/x/pr", "origin/main")
}

func TestMergeCommit(t *testing.T) {
	repo := t.TempDir()
	run(t, repo, "init", "-q")
	commit := func(name string) {
		t.Helper()
		os.WriteFile(filepath.Join(repo, name), []byte(name), 0o644)
		run(t, repo, "add", ".")
		run(t, repo, "commit", "-q", "-m", name)
	}
	commit("a")
	for _, n := range []string{"61", "610"} {
		run(t, repo, "checkout", "-q", "-b", "pr"+n, "main")
		commit("f" + n)
		run(t, repo, "checkout", "-q", "main")
		run(t, repo, "merge", "-q", "--no-ff", "-m", "Merge pull request #"+n+" from o/pr"+n, "pr"+n)
	}
	m61, m610 := MergeCommit(repo, 61), MergeCommit(repo, 610)
	if m61 == "" || m610 == "" || m61 == m610 || MergeCommit(repo, 6) != "" || MergeCommit(repo, 0) != "" {
		t.Fatalf("MergeCommit: %q %q", m61, m610)
	}
}
