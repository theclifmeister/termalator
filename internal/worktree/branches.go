package worktree

import (
	"fmt"
	"os/exec"
	"sort"
	"strings"
)

// ThreadBranches lists the local branches of thread id in project slug
// by name: tm/<slug>/<id> and tm/<slug>/<id>-…, the thread's own branch
// and any other it named the same way (a second PR's, say).
func ThreadBranches(repo, slug, id string) []string {
	prefix := "refs/heads/tm/" + slug + "/" + id
	refs, err := git(repo, "for-each-ref", "--format=%(refname:short)", prefix, prefix+"-*")
	if err != nil || refs == "" {
		return nil
	}
	var out []string
	for _, b := range strings.Split(refs, "\n") {
		if b = strings.TrimSpace(b); ValidBranch(b) {
			out = append(out, b)
		}
	}
	sort.Strings(out)
	return out
}

// PRHead asks gh for a pull request's state ("MERGED", "OPEN",
// "CLOSED"), number and head branch; all zero when gh can't tell.
func PRHead(repo, pr string) (state string, number int, head string) {
	if pr == "" || strings.HasPrefix(pr, "-") {
		return "", 0, ""
	}
	cmd := exec.Command("gh", "pr", "view", pr, "--json", "state,number,headRefName", "--jq", `"\(.state) \(.number) \(.headRefName)"`)
	cmd.Dir = repo
	b, err := cmd.Output()
	if err != nil {
		return "", 0, ""
	}
	f := strings.Fields(string(b))
	if len(f) != 3 || !ValidBranch(f[2]) {
		return "", 0, ""
	}
	fmt.Sscanf(f[1], "%d", &number)
	return f[0], number, f[2]
}

// CheckedOutIn is the worktree branch is checked out in, "" for none.
func CheckedOutIn(repo, branch string) string {
	list, err := git(repo, "worktree", "list", "--porcelain")
	if err != nil {
		return ""
	}
	dir := ""
	for _, l := range strings.Split(list, "\n") {
		if v, ok := strings.CutPrefix(l, "worktree "); ok {
			dir = v
		} else if l == "branch refs/heads/"+branch {
			return dir
		}
	}
	return ""
}

// DefaultRef is the branch merged work lands on: origin's default
// branch as last fetched ("origin/main"), else the repo's checked-out
// branch; "" when there is neither. No fetch.
func DefaultRef(repo string) string {
	if ref, err := git(repo, "symbolic-ref", "--quiet", "--short", "refs/remotes/origin/HEAD"); err == nil && ref != "" {
		return ref
	}
	if ref, err := git(repo, "symbolic-ref", "--quiet", "--short", "HEAD"); err == nil && ref != "" {
		return ref
	}
	return ""
}

// PruneBranch deletes a local branch only when that loses nothing: it is
// merged into DefaultRef, checked out in no worktree, and has no commit
// that no remote-tracking branch has. It deletes with git branch -d,
// never -D, checked against DefaultRef rather than the checkout's HEAD
// (which may lag origin): the branch's upstream is pointed there for
// the delete, and put back if git refuses. It returns whether it
// deleted the branch, and the base it was merged into or why it was
// kept.
func PruneBranch(repo, branch string) (deleted bool, why string) {
	if !ValidBranch(branch) || !BranchExists(repo, branch) {
		return false, "no such branch"
	}
	base := DefaultRef(repo)
	if base == "" || base == branch {
		return false, "no default branch to check it against"
	}
	if _, err := git(repo, "merge-base", "--is-ancestor", "refs/heads/"+branch, base); err != nil {
		return false, "not merged into " + base
	}
	if wt := CheckedOutIn(repo, branch); wt != "" {
		return false, "checked out in " + wt
	}
	if remotes, err := git(repo, "remote"); err == nil && remotes != "" {
		n, err := git(repo, "rev-list", "--count", "refs/heads/"+branch, "--not", "--remotes")
		if err != nil {
			return false, oneLine(err.Error())
		}
		if n != "0" {
			return false, n + " unpushed commit(s)"
		}
	}
	remote, _ := git(repo, "config", "--get", "branch."+branch+".remote")
	merge, _ := git(repo, "config", "--get", "branch."+branch+".merge")
	if _, err := git(repo, "branch", "--quiet", "--set-upstream-to="+base, branch); err != nil {
		return false, oneLine(err.Error())
	}
	if _, err := git(repo, "branch", "-d", branch); err != nil {
		if remote != "" && merge != "" {
			git(repo, "config", "branch."+branch+".remote", remote)
			git(repo, "config", "branch."+branch+".merge", merge)
		} else {
			git(repo, "branch", "--unset-upstream", branch)
		}
		return false, oneLine(err.Error())
	}
	return true, base
}

// oneLine is a git error's first line.
func oneLine(s string) string {
	s, _, _ = strings.Cut(s, "\n")
	return s
}
