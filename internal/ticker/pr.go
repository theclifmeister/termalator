package ticker

import (
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
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
}

// prFields is what `gh pr view --json` is asked for.
const prFields = "number,url,state,reviewDecision,statusCheckRollup"

// ghPR is gh's answer. statusCheckRollup mixes check runs (status,
// conclusion) and commit statuses (state).
type ghPR struct {
	Number         int    `json:"number"`
	URL            string `json:"url"`
	State          string `json:"state"`
	ReviewDecision string `json:"reviewDecision"`
	Rollup         []struct {
		Status     string `json:"status"`
		Conclusion string `json:"conclusion"`
		State      string `json:"state"`
	} `json:"statusCheckRollup"`
}

var (
	upperRE = regexp.MustCompile(`^[A-Z_]{0,32}$`)
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
