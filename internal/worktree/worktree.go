// Package worktree creates and removes the git worktrees threads run in
// (docs/SPEC.md §9). A worktree is a plain checkout: terminatr puts
// nothing in it, so removing it by any means loses nothing.
package worktree

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/theclifmeister/terminatr/internal/codehost"
)

// git runs git in dir and returns its trimmed stdout.
func git(dir string, args ...string) (string, error) {
	return gitWithin(0, dir, args...)
}

// gitWithin is git that is killed after timeout (0: never).
func gitWithin(timeout time.Duration, dir string, args ...string) (string, error) {
	ctx := context.Background()
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	cmd := exec.CommandContext(ctx, "git", args...)
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

// ErrNoRepo means the repository folder is gone (moved or deleted).
var ErrNoRepo = errors.New("repository folder is gone")

// Remove removes a worktree without force: a dirty one is kept and
// reported with ErrDirty. A worktree that is already gone is pruned. A
// repo that is gone gives ErrNoRepo, and nothing is touched.
func Remove(repo, dir string) error {
	if _, err := os.Stat(repo); errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("%s: %w", repo, ErrNoRepo)
	}
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

// RemoveForce removes a worktree with its uncommitted changes (git
// worktree remove --force): resolve --discard, after BranchUnsaved found
// no commit that is nowhere else. A worktree already gone is pruned.
func RemoveForce(repo, dir string) error {
	if _, err := os.Stat(repo); errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("%s: %w", repo, ErrNoRepo)
	}
	if _, err := os.Stat(dir); errors.Is(err, os.ErrNotExist) {
		_, err := git(repo, "worktree", "prune")
		return err
	}
	_, err := git(repo, "worktree", "remove", "--force", dir)
	return err
}

// Repair reconnects repo with its linked worktrees at dirs after either
// side moved (git worktree repair): the worktrees' .git files and the
// repo's records of them then name each other's current paths.
func Repair(repo string, dirs ...string) error {
	if len(dirs) == 0 {
		return nil
	}
	_, err := git(repo, append([]string{"worktree", "repair"}, dirs...)...)
	return err
}

// Reconnect tells the repo of the linked worktree at dir that the
// worktree now lives at dir, after the worktree alone moved (git
// worktree repair, run in it).
func Reconnect(dir string) error {
	_, err := git(dir, "worktree", "repair")
	return err
}

// Dirs are the git directories of a checkout.
type Dirs struct {
	// GitDir is the checkout's own git dir: <repo>/.git/worktrees/<name>
	// for a linked worktree (its HEAD and index), the repo's .git for the
	// main checkout.
	GitDir string
	// CommonDir is where the commits go: the main repo's .git. A
	// sandboxed thread must be able to write both.
	CommonDir string
	// RepoRoot is the main checkout's folder, the common dir's parent;
	// the common dir itself for a bare repo.
	RepoRoot string
}

// GitDirs returns the git directories of the checkout dir is in, as
// absolute paths.
func GitDirs(dir string) (Dirs, error) {
	out, err := git(dir, "rev-parse", "--path-format=absolute", "--git-dir", "--git-common-dir")
	if err != nil {
		return Dirs{}, err
	}
	gd, cd, ok := strings.Cut(out, "\n")
	if !ok || !filepath.IsAbs(gd) || !filepath.IsAbs(cd) {
		return Dirs{}, fmt.Errorf("git rev-parse: unexpected output %q", out)
	}
	d := Dirs{GitDir: gd, CommonDir: cd, RepoRoot: cd}
	if filepath.Base(cd) == ".git" {
		d.RepoRoot = filepath.Dir(cd)
	}
	return d, nil
}

// PRState is the state of the PR whose head is branch, from repo's code
// host (codehost.Host.PRState): "MERGED", "OPEN", "CLOSED", or "" when
// there is none or the host can't tell.
func PRState(repo, branch string) string {
	return codehost.Pick(repo, codehost.Config{}).PRState(repo, branch)
}

// DeleteBranch deletes a local branch. Callers check first that its PR
// is merged or MergedBase found it; a squash merge leaves it unmerged in
// git's eyes, hence -D.
func DeleteBranch(repo, branch string) error {
	_, err := git(repo, "branch", "-D", branch)
	return err
}

// MergedBase is the branch that branch is merged into, for resolve:
// origin's default branch as last fetched, else the repo's checked-out
// branch. "" when it is merged into neither. No fetch: a merge that
// only the remote knows about is left to the PR check.
func MergedBase(repo, branch string) string {
	var bases []string
	if ref, err := git(repo, "symbolic-ref", "--quiet", "--short", "refs/remotes/origin/HEAD"); err == nil && ref != "" {
		bases = append(bases, ref)
	}
	if ref, err := git(repo, "symbolic-ref", "--quiet", "--short", "HEAD"); err == nil && ref != "" && ref != branch {
		bases = append(bases, ref)
	}
	for _, b := range bases {
		if _, err := git(repo, "merge-base", "--is-ancestor", "refs/heads/"+branch, b); err == nil {
			return b
		}
	}
	return ""
}

// BranchExists reports whether the local branch exists.
func BranchExists(repo, branch string) bool {
	_, err := git(repo, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch)
	return err == nil
}

// BranchUnsaved says what a resolved thread's branch holds that is
// nowhere else: "<n> unpushed commit(s) on <branch>", or "" for nothing
// (no such branch, or all its commits on a remote; in a repo without
// remotes, merged into another branch).
func BranchUnsaved(repo, branch string) (string, error) {
	if repo == "" || branch == "" || !BranchExists(repo, branch) {
		return "", nil
	}
	remotes, err := git(repo, "remote")
	if err != nil {
		return "", err
	}
	if remotes == "" {
		if MergedBase(repo, branch) != "" {
			return "", nil
		}
		return "unmerged commits on " + branch, nil
	}
	n, err := git(repo, "rev-list", "--count", "refs/heads/"+branch, "--not", "--remotes")
	if err != nil {
		return "", err
	}
	switch n {
	case "0":
		return "", nil
	case "1":
		return "1 unpushed commit on " + branch, nil
	}
	return n + " unpushed commits on " + branch, nil
}

// Unsaved says what removing a worktree would lose: "uncommitted
// changes", "<n> unpushed commit(s)", or "" for nothing. Commits count as
// pushed when a remote-tracking branch, or pushed (a commit the remote
// had, e.g. a merged PR's head, whose branch the merge deleted), has
// them. A repo without remotes has nowhere to push: its commits stay on
// the branch, which resolve keeps. A worktree already gone loses
// nothing.
func Unsaved(dir, pushed string) (string, error) {
	if _, err := os.Stat(dir); errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	out, err := git(dir, "status", "--porcelain")
	if err != nil {
		return "", err
	}
	if out != "" {
		return "uncommitted changes", nil
	}
	if remotes, err := git(dir, "remote"); err != nil || remotes == "" {
		return "", err
	}
	args := []string{"rev-list", "--count", "HEAD", "--not", "--remotes"}
	if pushed != "" {
		if _, err := git(dir, "cat-file", "-e", pushed+"^{commit}"); err == nil {
			args = append(args, pushed)
		}
	}
	n, err := git(dir, args...)
	if err != nil {
		return "", err
	}
	switch n {
	case "0":
		return "", nil
	case "1":
		return "1 unpushed commit", nil
	}
	return n + " unpushed commits", nil
}

// MergeCommit is the commit that merged pull request n into the
// default branch as last fetched (origin's, else the checked-out
// branch), by git alone (codehost.Host.MergeCommit). "" when there is
// none: not merged, squashed, or not fetched yet. No fetch.
func MergeCommit(repo string, n int) string {
	return codehost.Pick(repo, codehost.Config{}).MergeCommit(repo, n)
}

// Place is where a directory sits in git, for a thread adopted in it
// (docs/SPEC.md §9, Adopt).
type Place struct {
	Top    string // the checkout's top directory
	Repo   string // the repository's main checkout
	Branch string // the branch checked out; "" when HEAD is detached
	Linked bool   // Top is a linked worktree, not the main checkout
}

// Locate tells where dir sits in git; ok is false outside a repository
// (or in a bare one).
func Locate(dir string) (pl Place, ok bool) {
	out, err := git(dir, "rev-parse", "--path-format=absolute", "--show-toplevel", "--git-dir", "--git-common-dir")
	if err != nil {
		return Place{}, false
	}
	f := strings.Split(out, "\n")
	if len(f) != 3 || f[0] == "" {
		return Place{}, false
	}
	gitDir, common := filepath.Clean(f[1]), filepath.Clean(f[2])
	pl = Place{Top: filepath.Clean(f[0]), Linked: gitDir != common}
	pl.Repo = pl.Top
	if pl.Linked {
		if filepath.Base(common) != ".git" {
			return Place{}, false // a bare repository's worktree: no main checkout
		}
		pl.Repo = filepath.Dir(common)
	}
	pl.Branch, _ = git(dir, "symbolic-ref", "--quiet", "--short", "HEAD")
	return pl, true
}
