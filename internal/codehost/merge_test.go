package codehost

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// gitRepo is a new repo on main with one commit, and a git runner for it.
func gitRepo(t *testing.T) (string, func(args ...string) string) {
	t.Helper()
	repo := t.TempDir()
	run := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@example.com"}, args...)...)
		cmd.Dir = repo
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	run("init", "-q", "-b", "main")
	os.WriteFile(filepath.Join(repo, "a"), []byte("a"), 0o644)
	run("add", ".")
	run("commit", "-q", "-m", "a")
	return repo, run
}

// TestAzureMergedPR: Azure DevOps' subject for a completed PR, merge or
// squash, gives its number; a custom message or a rebase gives none.
func TestAzureMergedPR(t *testing.T) {
	repo, run := gitRepo(t)
	var a Azure
	for msg, want := range map[string]int{
		"Merged PR 123: Add the thing":    123,
		"Merge PR 7: Docs say this":       7,
		"Merged PR 12:  spaces":           12,
		"Merged PR 12 without colon":      0,
		"Merge pull request #61 from a/b": 0, // GitHub's
		"Fix the thing (#7)":              0,
		"Release 2.0 (custom message)":    0,
		"Revert \"Merged PR 5: x\"":       0,
		"Merged PR 1234567890: too long":  0,
	} {
		run("commit", "-q", "--allow-empty", "-m", msg)
		if n := a.MergedPR(repo, run("rev-parse", "HEAD")); n != want {
			t.Errorf("%q: %d, want %d", msg, n, want)
		}
	}
	if n := a.MergedPR(repo, "HEAD"); n != 0 {
		t.Errorf("not a commit id: %d", n)
	}
}

// TestAzureMergeCommit: the commit Azure DevOps wrote completing PR n
// is found on the default branch whether it merged (no-ff, a merge
// commit) or squashed (a plain commit); a PR completed with a custom
// message or a rebase is not.
func TestAzureMergeCommit(t *testing.T) {
	repo, run := gitRepo(t)
	branch := func(name string) {
		run("checkout", "-q", "-b", name, "main")
		os.WriteFile(filepath.Join(repo, name), []byte(name), 0o644)
		run("add", ".")
		run("commit", "-q", "-m", "work on "+name)
		run("checkout", "-q", "main")
	}
	branch("merged")
	run("merge", "-q", "--no-ff", "-m", "Merged PR 61: merged", "merged")
	m61 := run("rev-parse", "HEAD")
	branch("squashed")
	run("merge", "-q", "--squash", "squashed")
	run("commit", "-q", "-m", "Merged PR 610: squashed\n\nRelated work items: #4")
	m610 := run("rev-parse", "HEAD")
	branch("custom")
	run("merge", "-q", "--squash", "custom")
	run("commit", "-q", "-m", "Ship the custom thing")

	var a Azure
	if got := a.MergeCommit(repo, 61); got != m61 {
		t.Errorf("merge commit: %q, want %q", got, m61)
	}
	if got := a.MergeCommit(repo, 610); got != m610 {
		t.Errorf("squash commit: %q, want %q", got, m610)
	}
	for _, n := range []int{6, 0, -1, 62} {
		if got := a.MergeCommit(repo, n); got != "" {
			t.Errorf("PR %d: %q", n, got)
		}
	}
	// GitHub's rules don't read Azure DevOps' subjects.
	if got := (GitHub{}).MergeCommit(repo, 61); got != "" {
		t.Errorf("GitHub read Azure's subject: %q", got)
	}
}
