package worktree

// Keeping the user's own checkout of a project repo current with
// origin's default branch (docs/SPEC.md §7.5 "Checkout sync"), and
// telling whether a thread's PR head is behind or conflicts with it.

import (
	"errors"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/theclifmeister/terminatr/internal/codehost"
)

// fetchTimeout bounds Sync's fetch, so a hung remote can't stall the
// ticker.
const fetchTimeout = time.Minute

// Why a checkout that is behind origin was not fast-forwarded: fixed
// words, shown in tm context and the dashboard.
const (
	HeldOff      = "fast-forward is off"
	HeldOther    = "another branch is checked out"
	HeldDirty    = "uncommitted changes"
	HeldDiverged = "it has commits origin lacks"
	HeldRefused  = "git refused the fast-forward"
)

// Checkout is how a repo's local default branch stands against origin's,
// after Sync.
type Checkout struct {
	// Branch is origin's default branch, e.g. "main"; "" when the repo
	// has no origin or its default branch can't be told.
	Branch string `json:"branch,omitempty"`
	// Origin is origin/<Branch>'s commit after the fetch.
	Origin string `json:"origin,omitempty"`
	// Behind and Ahead count the local branch's commits against
	// origin/<Branch>; both 0 without a local branch.
	Behind int `json:"behind,omitempty"`
	Ahead  int `json:"ahead,omitempty"`
	// From and To are the fast-forward Sync made; "" when it made none.
	From string `json:"from,omitempty"`
	To   string `json:"to,omitempty"`
	// Held says why a checkout still behind was not fast-forwarded.
	Held string `json:"held,omitempty"`
}

// Note is the checkout's state in fixed words for tm context and the
// dashboard: "local main is 3 behind origin (uncommitted changes)", or
// "" when it is current.
func (c Checkout) Note() string {
	if c.Branch == "" || c.Behind <= 0 {
		return ""
	}
	s := "local " + c.Branch + " is " + strconv.Itoa(c.Behind) + " behind origin"
	if c.Held != "" {
		s += " (" + c.Held + ")"
	}
	return s
}

// DefaultBranch is origin's default branch as last fetched ("main"), ""
// when the repo has no origin/HEAD and the remote can't say.
func DefaultBranch(repo string) string {
	ref, err := git(repo, "symbolic-ref", "--quiet", "--short", "refs/remotes/origin/HEAD")
	if err != nil || ref == "" {
		git(repo, "remote", "set-head", "origin", "--auto")
		if ref, err = git(repo, "symbolic-ref", "--quiet", "--short", "refs/remotes/origin/HEAD"); err != nil {
			return ""
		}
	}
	b, ok := strings.CutPrefix(ref, "origin/")
	if !ok || !branchRE.MatchString(b) {
		return ""
	}
	return b
}

// ValidBranch reports whether b is a branch name tm shows and passes to
// git.
func ValidBranch(b string) bool { return branchRE.MatchString(b) }

// branchRE is a branch name tm shows and passes to git: no spaces,
// control characters or leading dash.
var branchRE = regexp.MustCompile(`^[A-Za-z0-9_.][A-Za-z0-9_./-]{0,99}$`)

// Sync fetches origin and, when ff is set, fast-forwards the repo's
// local default branch to origin's, but only when that branch is
// checked out, the tracked files have no changes, and the update is a
// pure fast-forward (git merge --ff-only, which also refuses when an
// untracked file is in the way). It never merges, rebases, resets,
// stashes or touches another branch. A repo without origin gives a zero
// Checkout.
func Sync(repo string, ff bool) (Checkout, error) {
	var c Checkout
	if _, err := git(repo, "remote", "get-url", "origin"); err != nil {
		return c, nil
	}
	if _, err := gitWithin(fetchTimeout, repo, "fetch", "--quiet", "origin"); err != nil {
		return c, err
	}
	c.Branch = DefaultBranch(repo)
	if c.Branch == "" {
		return c, errors.New(repo + ": can't tell origin's default branch")
	}
	remote := "refs/remotes/origin/" + c.Branch
	local := "refs/heads/" + c.Branch
	var err error
	if c.Origin, err = git(repo, "rev-parse", "--verify", "--quiet", remote+"^{commit}"); err != nil {
		return c, err
	}
	if !BranchExists(repo, c.Branch) {
		return c, nil
	}
	if err := c.count(repo, local, remote); err != nil || c.Behind == 0 {
		return c, err
	}
	switch head, _ := git(repo, "symbolic-ref", "--quiet", "HEAD"); {
	case !ff:
		c.Held = HeldOff
	case head != local:
		c.Held = HeldOther
	case c.Ahead > 0:
		c.Held = HeldDiverged
	default:
		if out, err := git(repo, "--no-optional-locks", "status", "--porcelain", "--untracked-files=no"); err != nil || out != "" {
			c.Held = HeldDirty
			return c, err
		}
		from, err := git(repo, "rev-parse", local)
		if err != nil {
			return c, err
		}
		if _, err := git(repo, "merge", "--ff-only", "--quiet", "--no-stat", remote); err != nil {
			c.Held = HeldRefused
			return c, err
		}
		c.From, c.To = from, c.Origin
		return c, c.count(repo, local, remote)
	}
	return c, nil
}

// count sets Behind and Ahead of local against remote.
func (c *Checkout) count(repo, local, remote string) error {
	out, err := git(repo, "rev-list", "--left-right", "--count", local+"..."+remote)
	if err != nil {
		return err
	}
	f := strings.Fields(out)
	if len(f) != 2 {
		return errors.New("git rev-list: unexpected output " + strconv.Quote(out))
	}
	c.Ahead, _ = strconv.Atoi(f[0])
	c.Behind, _ = strconv.Atoi(f[1])
	return nil
}

// HeadState says how a PR head stands against base (origin's default
// branch head, both commits): "behind" when head lacks base, "conflict"
// when merging base into it would conflict, "" when it has base or base
// has it (the PR merged, so there is nothing to merge in). ok is
// false when the repo doesn't have both commits or git can't tell (git
// before 2.38 has no merge-tree --write-tree), so the caller can ask the
// forge instead.
func HeadState(repo, head, base string) (state string, ok bool) {
	for _, c := range []string{head, base} {
		if !oidRE.MatchString(c) {
			return "", false
		}
		if _, err := git(repo, "cat-file", "-e", c+"^{commit}"); err != nil {
			return "", false
		}
	}
	if _, err := git(repo, "merge-base", "--is-ancestor", base, head); err == nil {
		return "", true
	}
	if _, err := git(repo, "merge-base", "--is-ancestor", head, base); err == nil {
		return "", true
	}
	cmd := exec.Command("git", "merge-tree", "--write-tree", "--quiet", head, base)
	cmd.Dir = repo
	err := cmd.Run()
	var ee *exec.ExitError
	switch {
	case err == nil:
		return "behind", true
	case errors.As(err, &ee) && ee.ExitCode() == 1:
		return "conflict", true
	}
	return "", false
}

// Contains reports whether base (origin's default branch head) has
// head, a PR's head commit: so a merge commit or a fast-forward merged
// it, by git alone. False when either is no commit id of the repo; a
// squash or rebase merge leaves head out, so only the host can tell.
func Contains(repo, base, head string) bool {
	if !oidRE.MatchString(head) || !oidRE.MatchString(base) {
		return false
	}
	_, err := git(repo, "merge-base", "--is-ancestor", head, base)
	return err == nil
}

var oidRE = regexp.MustCompile(`^[0-9a-f]{40}([0-9a-f]{24})?$`)

// MergedPR is the PR number the commit's subject says it merged, 0 for
// none, by repo's code host's rules (codehost.Host.MergedPR). Only the
// number is taken from the subject.
func MergedPR(repo, commit string) int {
	return codehost.Pick(repo, codehost.Config{}).MergedPR(repo, commit)
}
