// Package worktree creates and removes the git worktrees threads run in
// (docs/SPEC.md §9). A worktree is a plain checkout: termalator puts
// nothing in it, so removing it by any means loses nothing.
package worktree

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// git runs git in dir and returns its trimmed stdout.
func git(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(errb.String())
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("git %s: %s", strings.Join(args, " "), msg)
	}
	return strings.TrimSpace(out.String()), nil
}

// DefaultBase is what a new worktree starts from: origin's default branch
// (origin/HEAD's target), after fetching origin. A repo without origin
// starts from its own HEAD.
func DefaultBase(repo string) (string, error) {
	if _, err := git(repo, "remote", "get-url", "origin"); err != nil {
		return "HEAD", nil
	}
	if _, err := git(repo, "fetch", "--quiet", "origin"); err != nil {
		return "", err
	}
	ref, err := git(repo, "symbolic-ref", "--quiet", "--short", "refs/remotes/origin/HEAD")
	if err != nil || ref == "" {
		// origin/HEAD isn't set in every clone; ask the remote.
		git(repo, "remote", "set-head", "origin", "--auto")
		if ref, err = git(repo, "symbolic-ref", "--quiet", "--short", "refs/remotes/origin/HEAD"); err != nil || ref == "" {
			return "", fmt.Errorf("%s: can't tell origin's default branch; pass --base", repo)
		}
	}
	return ref, nil
}

// Create adds a worktree at dir on a new branch from base. base "" means
// DefaultBase. It returns the base used.
func Create(repo, dir, branch, base string) (string, error) {
	if base == "" {
		var err error
		if base, err = DefaultBase(repo); err != nil {
			return "", err
		}
	} else if _, err := git(repo, "remote", "get-url", "origin"); err == nil {
		git(repo, "fetch", "--quiet", "origin")
	}
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		return "", err
	}
	if _, err := git(repo, "worktree", "add", "--quiet", "-b", branch, dir, base); err != nil {
		return "", err
	}
	return base, nil
}

// Restore re-adds a worktree removed by hand, on its existing branch.
func Restore(repo, dir, branch string) error {
	git(repo, "worktree", "prune")
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		return err
	}
	_, err := git(repo, "worktree", "add", "--quiet", dir, branch)
	return err
}

// ErrDirty means a worktree has changes and was kept.
var ErrDirty = errors.New("worktree has uncommitted changes")

// Remove removes a worktree without force: a dirty one is kept and
// reported with ErrDirty. A worktree that is already gone is pruned.
func Remove(repo, dir string) error {
	if _, err := os.Stat(dir); errors.Is(err, os.ErrNotExist) {
		_, err := git(repo, "worktree", "prune")
		return err
	}
	if out, err := git(dir, "status", "--porcelain"); err == nil && out != "" {
		return ErrDirty
	}
	_, err := git(repo, "worktree", "remove", dir)
	return err
}

// CommonDir is the git directory a worktree's commits go to (the main
// repo's .git), which a sandboxed thread must be able to write.
func CommonDir(dir string) (string, error) {
	d, err := git(dir, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return "", err
	}
	return d, nil
}

// PRState asks gh for the state of the PR whose head is branch: "MERGED",
// "OPEN", "CLOSED", or "" when there is none or gh can't tell.
func PRState(repo, branch string) string {
	cmd := exec.Command("gh", "pr", "view", branch, "--json", "state", "--jq", ".state")
	cmd.Dir = repo
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// DeleteBranch deletes a local branch. Callers check first that its PR
// is merged; a squash merge leaves it unmerged in git's eyes, hence -D.
func DeleteBranch(repo, branch string) error {
	_, err := git(repo, "branch", "-D", branch)
	return err
}

// BranchExists reports whether the local branch exists.
func BranchExists(repo, branch string) bool {
	_, err := git(repo, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch)
	return err == nil
}
