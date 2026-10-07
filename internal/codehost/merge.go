package codehost

// Merges as git history shows them: each host writes its own subject on
// the commit that merges a PR, so these are the fallback when its API
// can't be asked (docs/SPEC.md §7.5). A rebase merge writes none; only
// the PR's state says it merged.

import (
	"fmt"
	"regexp"
	"strconv"
)

// subjectPR is the PR number re's first non-empty group finds in
// commit's subject, 0 for none. Only the number is taken from it.
func subjectPR(repo, commit string, re *regexp.Regexp) int {
	if !commitRE.MatchString(commit) {
		return 0
	}
	s, err := git(repo, "log", "-1", "--format=%s", commit)
	if err != nil {
		return 0
	}
	m := re.FindStringSubmatch(s)
	for i := 1; i < len(m); i++ {
		if n, err := strconv.Atoi(m[i]); err == nil {
			return n
		}
	}
	return 0
}

// grepMerge is the newest commit on the default branch as last fetched
// (origin's, else the checked-out branch) whose message matches the
// extended regexp pattern, merge commits only when merges; "" for none.
// No fetch.
func grepMerge(repo, pattern string, merges bool) string {
	for _, base := range []string{"refs/remotes/origin/HEAD", "HEAD"} {
		args := []string{"log", "-1", "--format=%H", "-E", "--grep", pattern}
		if merges {
			args = append(args, "--merges")
		}
		out, err := git(repo, append(args, base, "--")...)
		if err == nil && out != "" {
			return out
		}
	}
	return ""
}

// azureMergedRE finds the PR number in the subject Azure DevOps writes
// when it completes a PR, "Merged PR 12: title", for a merge commit and
// a squash commit alike ("Merge PR 12: …" in some docs). A custom
// message has none.
var azureMergedRE = regexp.MustCompile(`^Merged? PR ([0-9]{1,9}): `)

// MergeCommit is the commit Azure DevOps wrote completing PR n, "Merged
// PR n: …", a merge or a squash, on the default branch as last fetched.
// "" when there is none: not completed, rebased, a custom message, or
// not fetched yet. No fetch.
func (Azure) MergeCommit(repo string, n int) string {
	if n <= 0 {
		return ""
	}
	return grepMerge(repo, fmt.Sprintf("^Merged? PR %d: ", n), false)
}

// MergedPR reads the PR number from the commit's subject, Azure DevOps'
// "Merged PR 12: …".
func (Azure) MergedPR(repo, commit string) int {
	return subjectPR(repo, commit, azureMergedRE)
}
