package worktree

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// TestPruneBranch: a branch merged into origin's default branch is
// deleted with git branch -d even when the checkout's main lags origin;
// an unmerged one, one checked out in a worktree and one with commits no
// remote has are kept, each with its reason.
func TestPruneBranch(t *testing.T) {
	repo, other := fixture(t)
	// A second PR's branch, merged on origin (by the other clone), so
	// the repo's own main lags.
	run(t, repo, "checkout", "-q", "-b", "tm/demo/t-0001-second")
	commit(t, repo, "s", "2\n", "second")
	run(t, repo, "push", "-q", "origin", "tm/demo/t-0001-second")
	run(t, repo, "checkout", "-q", "main")
	run(t, other, "pull", "-q", "origin", "main")
	run(t, other, "fetch", "-q", "origin", "tm/demo/t-0001-second")
	run(t, other, "merge", "-q", "--no-ff", "-m", "Merge pull request #2", "FETCH_HEAD")
	run(t, other, "push", "-q", "origin", "main")
	// Its upstream as `git push -u` sets it, gone since the merge
	// deleted it: plain git branch -d would check the lagging HEAD.
	run(t, repo, "branch", "-q", "--set-upstream-to", "origin/tm/demo/t-0001-second", "tm/demo/t-0001-second")
	run(t, other, "push", "-q", "origin", "--delete", "tm/demo/t-0001-second")
	run(t, repo, "fetch", "-q", "--prune", "origin")
	if _, err := git(repo, "merge-base", "--is-ancestor", "tm/demo/t-0001-second", "HEAD"); err == nil {
		t.Fatal("fixture: the checkout's main should lag origin's")
	}

	// Unmerged, pushed: kept.
	run(t, repo, "checkout", "-q", "-b", "tm/demo/t-0001-open")
	commit(t, repo, "o", "o\n", "open")
	run(t, repo, "push", "-q", "origin", "tm/demo/t-0001-open")
	run(t, repo, "checkout", "-q", "main")
	// Merged but checked out in a worktree: kept.
	run(t, repo, "branch", "tm/demo/t-0001-busy", "origin/main")
	wt := filepath.Join(t.TempDir(), "busy")
	run(t, repo, "worktree", "add", "-q", wt, "tm/demo/t-0001-busy")
	// Another thread's: not listed.
	run(t, repo, "branch", "tm/demo/t-00010-x", "origin/main")

	got := ThreadBranches(repo, "demo", "t-0001")
	want := []string{"tm/demo/t-0001-busy", "tm/demo/t-0001-open", "tm/demo/t-0001-second"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ThreadBranches = %q, want %q", got, want)
	}

	if ok, why := PruneBranch(repo, "tm/demo/t-0001-second"); !ok || why != "origin/main" {
		t.Fatalf("merged: %v %q", ok, why)
	}
	if BranchExists(repo, "tm/demo/t-0001-second") {
		t.Fatal("merged branch still there")
	}
	if cfg, _ := git(repo, "config", "--get-regexp", `^branch\.tm/demo/t-0001-second\.`); cfg != "" {
		t.Fatalf("config left behind: %q", cfg)
	}
	if ok, why := PruneBranch(repo, "tm/demo/t-0001-open"); ok || why != "not merged into origin/main" {
		t.Fatalf("unmerged: %v %q", ok, why)
	}
	if ok, why := PruneBranch(repo, "tm/demo/t-0001-busy"); ok || !strings.HasPrefix(why, "checked out in ") {
		t.Fatalf("checked out: %v %q", ok, why)
	}
	if ok, why := PruneBranch(repo, "tm/demo/t-0001-gone"); ok || why != "no such branch" {
		t.Fatalf("missing: %v %q", ok, why)
	}
	for _, b := range []string{"tm/demo/t-0001-open", "tm/demo/t-0001-busy"} {
		if !BranchExists(repo, b) {
			t.Fatalf("%s deleted", b)
		}
	}
}

// TestPruneBranchUnpushed: with origin/HEAD unknown the checked-out
// branch is the base, and a branch merged there by hand but never
// pushed is kept.
func TestPruneBranchUnpushed(t *testing.T) {
	repo, _ := fixture(t)
	run(t, repo, "remote", "set-head", "origin", "--delete")
	run(t, repo, "checkout", "-q", "-b", "tm/demo/t-0002-local")
	commit(t, repo, "l", "l\n", "local")
	run(t, repo, "checkout", "-q", "main")
	run(t, repo, "merge", "-q", "--ff-only", "tm/demo/t-0002-local")
	if ok, why := PruneBranch(repo, "tm/demo/t-0002-local"); ok || why != "1 unpushed commit(s)" {
		t.Fatalf("unpushed: %v %q", ok, why)
	}
	run(t, repo, "push", "-q", "origin", "main")
	if ok, why := PruneBranch(repo, "tm/demo/t-0002-local"); !ok || why != "main" {
		t.Fatalf("pushed: %v %q", ok, why)
	}
}

// TestPruneBranchNoRemote: in a repo without remotes, a branch merged
// into the checked-out branch is deleted.
func TestPruneBranchNoRemote(t *testing.T) {
	repo := t.TempDir()
	run(t, repo, "init", "-q")
	run(t, repo, "commit", "-q", "--allow-empty", "-m", "c")
	run(t, repo, "branch", "tm/demo/t-0003-x")
	if ok, why := PruneBranch(repo, "tm/demo/t-0003-x"); !ok || why != "main" {
		t.Fatalf("%v %q", ok, why)
	}
}
