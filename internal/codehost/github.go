package codehost

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/theclifmeister/terminatr/internal/plat/shell"
)

// GitHub is the GitHub host, through the gh CLI.
type GitHub struct {
	// Run runs gh in dir; nil runs the real one (RunGH).
	Run func(dir string, args ...string) ([]byte, error)
}

func (GitHub) Kind() string { return GitHubKind }

func (g GitHub) gh(dir string, args ...string) ([]byte, error) {
	if g.Run != nil {
		return g.Run(dir, args...)
	}
	return RunGH(dir, args...)
}

// RunGH runs the real gh in dir, without prompts or colour, for at most
// 30 seconds.
func RunGH(dir string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := shell.CLICommand(ctx, "gh", args...)
	cmd.Dir = dir
	cmd.Env = append(cmd.Environ(), "GH_PROMPT_DISABLED=1", "NO_COLOR=1")
	var errb bytes.Buffer
	cmd.Stderr = &errb
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("gh %s: %w %s", strings.Join(args, " "), err, strings.TrimSpace(errb.String()))
	}
	return out, nil
}

// prFields is what `gh pr view --json` is asked for.
const prFields = "number,url,state,reviewDecision,statusCheckRollup,mergedAt,headRefOid,mergeCommit,baseRefName,mergeable,mergeStateStatus"

// ghNoPR matches the errors of a gh that works but found no PR.
var ghNoPR = regexp.MustCompile(`(?i)no (open )?pull requests found|could not resolve to a pullrequest`)

// PR asks `gh pr view` about ref. A gh that only found no PR, or gave an
// answer without one, is ErrNoPR.
func (g GitHub) PR(repo string, ref Ref) (PR, error) {
	target := prTarget(ref.URL, ref.Branch)
	if ref.Number > 0 {
		target = strconv.Itoa(ref.Number)
	}
	if target == "" {
		return PR{}, ErrNoRef
	}
	out, err := g.gh(repo, "pr", "view", target, "--json", prFields)
	if err != nil {
		if ghNoPR.MatchString(err.Error()) {
			return PR{}, fmt.Errorf("%w: %v", ErrNoPR, err)
		}
		return PR{}, err
	}
	pr, err := parseGitHubPR(out)
	if err != nil || pr.Number == 0 {
		return PR{}, ErrNoPR
	}
	return pr, nil
}

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
	// branchRE is worktree.ValidBranch's: no spaces, control characters
	// or leading dash.
	branchRE = regexp.MustCompile(`^[A-Za-z0-9_.][A-Za-z0-9_./-]{0,99}$`)
)

// parseGitHubPR reads gh's JSON into the fixed fields. Anything outside
// the expected shapes is dropped, not passed on.
func parseGitHubPR(data []byte) (PR, error) {
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
	if branchRE.MatchString(g.BaseRefName) {
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

// FailedLog asks gh for the failing runs of the PR's head commit and
// returns the first failing job's name and an excerpt of its log, or ""
// when it can't (no failed run, gh failed, an empty log).
func (g GitHub) FailedLog(repo string, pr PR) (job, text string) {
	if pr.Head == "" {
		return "", ""
	}
	out, err := g.gh(repo, "run", "list", "--commit", pr.Head, "--status", "failure", "--json", "databaseId", "--jq", ".[].databaseId", "--limit", "10")
	if err != nil {
		return "", ""
	}
	n := 0
	for _, f := range strings.Fields(string(out)) {
		if n++; n > logRuns {
			break
		}
		if !digitsRE.MatchString(f) {
			continue
		}
		log, err := g.gh(repo, "run", "view", f, "--log-failed")
		if err != nil {
			continue
		}
		if job, ex := excerpt(string(log)); ex != "" {
			return job, ex
		}
	}
	return "", ""
}

var digitsRE = regexp.MustCompile(`^[0-9]{1,12}$`)

// PRState asks gh for the state of the PR whose head is branch.
func (g GitHub) PRState(repo, branch string) string {
	out, err := g.gh(repo, "pr", "view", branch, "--json", "state", "--jq", ".state")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// PRHead asks gh for a pull request's state, number and head branch.
func (g GitHub) PRHead(repo, pr string) (state string, number int, head string) {
	if pr == "" || strings.HasPrefix(pr, "-") {
		return "", 0, ""
	}
	b, err := g.gh(repo, "pr", "view", pr, "--json", "state,number,headRefName", "--jq", `"\(.state) \(.number) \(.headRefName)"`)
	if err != nil {
		return "", 0, ""
	}
	f := strings.Fields(string(b))
	if len(f) != 3 || !branchRE.MatchString(f[2]) {
		return "", 0, ""
	}
	fmt.Sscanf(f[1], "%d", &number)
	return f[0], number, f[2]
}

// MergeCommit is the merge commit GitHub writes for PR n, "Merge pull
// request #n from …", on the default branch as last fetched (origin's,
// else the checked-out branch). "" when there is none: not merged,
// squashed, or not fetched yet. No fetch.
func (GitHub) MergeCommit(repo string, n int) string {
	if n <= 0 {
		return ""
	}
	return grepMerge(repo, fmt.Sprintf("^Merge pull request #%d from ", n), true)
}

// commitRE is a full commit id, SHA-1 or SHA-256.
var commitRE = regexp.MustCompile(`^[0-9a-f]{40}([0-9a-f]{24})?$`)

// mergedPRRE finds the PR number in a merge commit's subject: GitHub's
// "Merge pull request #12 from …" or a squash merge's "… (#12)".
var mergedPRRE = regexp.MustCompile(`^Merge pull request #([0-9]{1,9}) |\(#([0-9]{1,9})\)$`)

// MergedPR reads the PR number from the commit's subject. Only the
// number is taken from it.
func (GitHub) MergedPR(repo, commit string) int {
	return subjectPR(repo, commit, mergedPRRE)
}

func (GitHub) Hints(n int) Hints {
	return Hints{
		Checks: fmt.Sprintf("gh pr checks %d", n),
		Review: fmt.Sprintf("gh pr view %d --comments", n),
	}
}

// Doctor checks that gh is installed and logged in: without it the
// ticker's PR polls fail (a gh-failing inbox item, §7.5).
func (GitHub) Doctor(d DoctorDeps) []Check {
	p, err := d.LookPath("gh")
	if err != nil {
		return []Check{{Name: "gh", Detail: "not found; tm thread resolve can't tell whether a PR was merged"}}
	}
	auth := Check{Name: "gh auth", OK: true, Detail: "logged in"}
	if _, err := d.Run("", p, "auth", "status"); err != nil {
		auth = Check{Name: "gh auth", Detail: "not logged in, or its token can't be read: run gh auth login (PR follow-up, auto-close and completing tasks need it)"}
	}
	return []Check{{Name: "gh", OK: true, Detail: "found"}, auth}
}

// ParsePRURL is the number of a GitHub PR URL,
// https://github.com/o/r/pull/12.
func (GitHub) ParsePRURL(s string) (int, bool) {
	if !urlRE.MatchString(s) {
		return 0, false
	}
	_, num, _ := strings.Cut(s, "/pull/")
	n, err := strconv.Atoi(num)
	return n, err == nil
}
