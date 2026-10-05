package ticker

import (
	"encoding/json"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/theclifmeister/termilator/internal/worktree"
)

// PR is the fixed set of fields the ticker keeps of a pull request
// (docs/SPEC.md §7.5): never its title, body or comments.
type PR struct {
	Number int    `json:"number,omitempty"`
	URL    string `json:"url,omitempty"`
	State  string `json:"state,omitempty"`  // OPEN, CLOSED, MERGED
	Checks string `json:"checks,omitempty"` // pass, fail, pending, or "" with no checks
	Failed int    `json:"failed,omitempty"` // failing checks
	Review string `json:"review,omitempty"` // APPROVED, CHANGES_REQUESTED, REVIEW_REQUIRED, ""
	// MergedAt is when it merged (when the ticker first saw it merged,
	// if gh doesn't say); Head is its head commit, Merge the commit its
	// merge made on the base branch.
	MergedAt time.Time `json:"merged_at,omitzero"`
	Head     string    `json:"head,omitempty"`
	Merge    string    `json:"merge,omitempty"`
	// Base is the branch it merges into; Mergeable (MERGEABLE,
	// CONFLICTING, UNKNOWN) and MergeState (BEHIND, DIRTY, CLEAN, …) are
	// GitHub's word on it against that branch.
	Base       string `json:"base,omitempty"`
	Mergeable  string `json:"mergeable,omitempty"`
	MergeState string `json:"merge_state,omitempty"`
}

// prFields is what `gh pr view --json` is asked for.
const prFields = "number,url,state,reviewDecision,statusCheckRollup,mergedAt,headRefOid,mergeCommit,baseRefName,mergeable,mergeStateStatus"

// ghPR is gh's answer. statusCheckRollup mixes check runs (status,
// conclusion) and commit statuses (state).
type ghPR struct {
	Number         int    `json:"number"`
	URL            string `json:"url"`
	State          string `json:"state"`
	ReviewDecision string `json:"reviewDecision"`
	MergedAt       string `json:"mergedAt"`
	HeadRefOid     string `json:"headRefOid"`
	MergeCommit    *struct {
		Oid string `json:"oid"`
	} `json:"mergeCommit"`
	BaseRefName string `json:"baseRefName"`
	Mergeable   string `json:"mergeable"`
	MergeState  string `json:"mergeStateStatus"`
	Rollup      []struct {
		Status     string `json:"status"`
		Conclusion string `json:"conclusion"`
		State      string `json:"state"`
	} `json:"statusCheckRollup"`
}

var (
	upperRE = regexp.MustCompile(`^[A-Z_]{0,32}$`)
	oidRE   = regexp.MustCompile(`^[0-9a-f]{40}$`)
	urlRE   = regexp.MustCompile(`^https://[A-Za-z0-9.-]+(/[A-Za-z0-9._-]+){2}/pull/[0-9]{1,9}$`)
)

// ParsePR reads gh's JSON into the fixed fields. Anything outside the
// expected shapes is dropped, not passed on.
func ParsePR(data []byte) (PR, error) {
	var g ghPR
	if err := json.Unmarshal(data, &g); err != nil {
		return PR{}, err
	}
	pr := PR{Number: g.Number}
	if pr.Number < 0 {
		pr.Number = 0
	}
	if urlRE.MatchString(g.URL) {
		pr.URL = g.URL
	}
	if upperRE.MatchString(g.State) {
		pr.State = g.State
	}
	if upperRE.MatchString(g.ReviewDecision) {
		pr.Review = g.ReviewDecision
	}
	if at, err := time.Parse(time.RFC3339, g.MergedAt); err == nil && pr.State == "MERGED" && at.Year() > 2000 {
		pr.MergedAt = at.UTC()
	}
	if oidRE.MatchString(g.HeadRefOid) {
		pr.Head = g.HeadRefOid
	}
	if g.MergeCommit != nil && pr.State == "MERGED" && oidRE.MatchString(g.MergeCommit.Oid) {
		pr.Merge = g.MergeCommit.Oid
	}
	if worktree.ValidBranch(g.BaseRefName) {
		pr.Base = g.BaseRefName
	}
	if upperRE.MatchString(g.Mergeable) {
		pr.Mergeable = g.Mergeable
	}
	if upperRE.MatchString(g.MergeState) {
		pr.MergeState = g.MergeState
	}
	pending := false
	for _, c := range g.Rollup {
		switch {
		case c.State != "": // a commit status
			switch c.State {
			case "SUCCESS":
			case "PENDING", "EXPECTED":
				pending = true
			default: // ERROR, FAILURE
				pr.Failed++
			}
		case c.Status != "" && c.Status != "COMPLETED":
			pending = true
		default:
			switch c.Conclusion {
			case "SUCCESS", "NEUTRAL", "SKIPPED":
			default: // FAILURE, CANCELLED, TIMED_OUT, ACTION_REQUIRED, STARTUP_FAILURE, …
				pr.Failed++
			}
		}
	}
	switch {
	case pr.Failed > 0:
		pr.Checks = "fail"
	case pending:
		pr.Checks = "pending"
	case len(g.Rollup) > 0:
		pr.Checks = "pass"
	}
	return pr, nil
}

// prTarget is what to ask gh about: the PR URL from the thread's report
// when it is a well-formed GitHub PR URL, else the thread's branch.
func prTarget(reportPR, branch string) string {
	if urlRE.MatchString(reportPR) {
		return reportPR
	}
	if branch != "" && !strings.HasPrefix(branch, "-") {
		return branch
	}
	return ""
}

// ref is how items and prompts name a PR: "PR #12".
func (pr PR) ref() string {
	if pr.Number > 0 {
		return "PR #" + strconv.Itoa(pr.Number)
	}
	return "its PR"
}

// Summary is the PR's state in one line of fixed words, as tm thread
// list and tm context show it: "#12 open, checks pass, approved",
// "#12 merged". It is "" before the ticker has seen the PR.
func (pr PR) Summary() string {
	if pr.Number <= 0 {
		return ""
	}
	num := "#" + strconv.Itoa(pr.Number)
	var parts []string
	switch pr.State {
	case "OPEN":
		parts = append(parts, num+" open")
		switch pr.Checks {
		case "pass":
			parts = append(parts, "checks pass")
		case "pending":
			parts = append(parts, "checks pending")
		case "fail":
			if pr.Failed == 1 {
				parts = append(parts, "1 check failed")
			} else {
				parts = append(parts, strconv.Itoa(pr.Failed)+" checks failed")
			}
		}
		if pr.Mergeable == "CONFLICTING" {
			parts = append(parts, "conflicts")
		}
		switch pr.Review {
		case "APPROVED":
			parts = append(parts, "approved")
		case "CHANGES_REQUESTED":
			parts = append(parts, "changes requested")
		case "REVIEW_REQUIRED":
			parts = append(parts, "review required")
		}
	case "MERGED":
		parts = append(parts, num+" merged")
	case "CLOSED":
		parts = append(parts, num+" closed")
	default:
		parts = append(parts, num)
	}
	return strings.Join(parts, ", ")
}

// Summaries is PRs as Summary lines, for project.Context.
func Summaries(path, slug string) map[string]string {
	out := map[string]string{}
	for id, pr := range PRs(path, slug) {
		out[id] = pr.Summary()
	}
	return out
}

// PRs reads the PR fields the ticker keeps in its state file at path for
// the threads of project slug, by thread id. A missing or unreadable
// file gives none: the ticker hasn't polled yet.
func PRs(path, slug string) map[string]PR {
	out := map[string]PR{}
	b, err := os.ReadFile(path)
	if err != nil {
		return out
	}
	var st state
	if json.Unmarshal(b, &st) != nil {
		return out
	}
	for k, m := range st.Threads {
		id, ok := strings.CutPrefix(k, slug+"/")
		if ok && m != nil && m.PR.Number > 0 && !strings.Contains(id, "/") {
			out[id] = m.PR
		}
	}
	return out
}
