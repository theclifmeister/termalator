package codehost

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Azure is the Azure DevOps host, through the az CLI and its
// azure-devops extension, logged in with az login (docs/SPEC.md §7.5).
// Every call names the organization, and the project and repo where az
// takes them, from Target: never what az would detect from the
// checkout.
type Azure struct {
	Target Target
	// Run runs az in dir; nil runs the real one (RunAZ).
	Run func(dir string, args ...string) ([]byte, error)
}

func init() {
	newAzure = func(t Target) Host { return Azure{Target: t} }
}

func (Azure) Kind() string { return AzureKind }

func (a Azure) az(dir string, args ...string) ([]byte, error) {
	if a.Run != nil {
		return a.Run(dir, args...)
	}
	return RunAZ(dir, args...)
}

// azSlots bounds the az processes running at once: each is a Python
// start of a second or so, and several threads' polls may overlap.
var azSlots = make(chan struct{}, 4)

// azTimeout is how long one az call may take.
const azTimeout = 45 * time.Second

// RunAZ runs the real az in dir with JSON output, without prompts,
// colour, warnings or installing a missing extension, for at most
// azTimeout and at most cap(azSlots) at once. Its errors are CLIErrors.
func RunAZ(dir string, args ...string) ([]byte, error) {
	azSlots <- struct{}{}
	defer func() { <-azSlots }()
	ctx, cancel := context.WithTimeout(context.Background(), azTimeout)
	defer cancel()
	args = append(slices.Clip(args), "--only-show-errors", "--output", "json")
	cmd := exec.CommandContext(ctx, "az", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"AZURE_CORE_NO_COLOR=true",
		"AZURE_CORE_ONLY_SHOW_ERRORS=true",
		"AZURE_CORE_SURVEY_MESSAGE=false",
		"AZURE_EXTENSION_USE_DYNAMIC_INSTALL=no",
	)
	var errb bytes.Buffer
	cmd.Stderr = &errb
	out, err := cmd.Output()
	if err != nil {
		if ctx.Err() != nil {
			err = fmt.Errorf("%w after %s", ctx.Err(), azTimeout)
		}
		return nil, azError(fmt.Errorf("az %s: %w %s", strings.Join(args, " "), err, strings.TrimSpace(errb.String())))
	}
	return out, nil
}

// CLIError is a code host CLI's failure with what tm makes of it in
// fixed words: Problem ("not logged in") and Advice ("run az login"),
// "" when it can't tell. The CLI's own text stays in Err, for the
// server log only.
type CLIError struct {
	CLI     string
	Problem string
	Advice  string
	Err     error
}

func (e *CLIError) Error() string { return e.Err.Error() }
func (e *CLIError) Unwrap() error { return e.Err }

// azProblems are the az failures tm can name, first match wins.
var azProblems = []struct {
	re              *regexp.Regexp
	problem, advice string
}{
	{regexp.MustCompile(`(?i)az login|AADSTS|not logged in|refresh token|interactive authentication is needed|need to run the login command`),
		"az is not logged in", "the user runs az login in a terminal"},
	{regexp.MustCompile(`(?i)'(repos|devops)' is misspelled or not recognized|extension .*(not installed|is required)|azure-devops`),
		"az lacks the azure-devops extension", "the user runs az extension add --name azure-devops"},
	{regexp.MustCompile(`(?i)\b40[13]\b|TF400813|unauthori[sz]ed|forbidden|not authorized|does not have permissions?|access denied`),
		"Azure DevOps refused access (401/403)", "the user checks in a terminal that their az login can read this repo (tm doctor)"},
	{regexp.MustCompile(`(?i)TF200016|TF401019|project .* does not exist|repository .* does not exist|could not be found`),
		"Azure DevOps doesn't know this organization, project or repo", "the user checks the repo's origin URL or the project's azure_url"},
}

// azError wraps err in a CLIError naming what went wrong.
func azError(err error) error {
	e := &CLIError{CLI: "az", Err: err}
	if errors.Is(err, exec.ErrNotFound) {
		e.Problem, e.Advice = "az is not installed", "the user installs the Azure CLI (tm doctor)"
		return e
	}
	for _, p := range azProblems {
		if p.re.MatchString(err.Error()) {
			e.Problem, e.Advice = p.problem, p.advice
			break
		}
	}
	return e
}

// azNoPR matches the errors of an az that works but has no such PR.
var azNoPR = regexp.MustCompile(`(?i)TF401180|pull request .*(not found|does not exist)|could not find .*pull request`)

// org is the --organization argument.
func (a Azure) org() []string { return []string{"--organization", a.Target.OrgURL} }

// PR asks az about ref: `az repos pr show` for a number (or a PR URL of
// this repo), else `az repos pr list` by source branch, preferring an
// active PR, else the newest. An open PR's policy evaluations and
// statuses are asked too, at once. A PR of another repo is ErrNoPR.
func (a Azure) PR(repo string, ref Ref) (PR, error) {
	n := ref.Number
	if n <= 0 {
		n, _ = a.ParsePRURL(ref.URL)
	}
	branch := ref.Branch
	if n <= 0 && (branch == "" || strings.HasPrefix(branch, "-") || !branchRE.MatchString(branch)) {
		return PR{}, ErrNoRef
	}
	if a.Target.OrgURL == "" || a.Target.Project == "" || a.Target.Repo == "" {
		return PR{}, fmt.Errorf("azure devops: no organization, project or repo for %s", repo)
	}
	var raw []byte
	if n > 0 {
		out, err := a.az(repo, append([]string{"repos", "pr", "show", "--id", strconv.Itoa(n)}, a.org()...)...)
		if err != nil {
			if azNoPR.MatchString(err.Error()) {
				return PR{}, fmt.Errorf("%w: %v", ErrNoPR, err)
			}
			return PR{}, err
		}
		raw = out
	} else {
		out, err := a.az(repo, append([]string{"repos", "pr", "list", "--project", a.Target.Project, "--repository", a.Target.Repo,
			"--source-branch", "refs/heads/" + branch, "--status", "all", "--top", "20"}, a.org()...)...)
		if err != nil {
			return PR{}, err
		}
		if raw = pickAzurePR(out); raw == nil {
			return PR{}, ErrNoPR
		}
	}
	var head azPR
	if err := json.Unmarshal(raw, &head); err != nil || head.ID <= 0 || !a.ours(head) {
		return PR{}, ErrNoPR
	}
	var policies, statuses []byte
	if head.Status == "active" {
		var perr error
		var wg sync.WaitGroup
		wg.Go(func() {
			policies, perr = a.az(repo, append([]string{"repos", "pr", "policy", "list", "--id", strconv.Itoa(head.ID)}, a.org()...)...)
		})
		wg.Go(func() {
			// External statuses are extra: without them the policies
			// still say what blocks the PR.
			statuses, _ = a.az(repo, append([]string{"devops", "invoke", "--area", "git", "--resource", "pullRequestStatuses",
				"--route-parameters", "project=" + a.Target.Project, "repositoryId=" + a.Target.Repo, "pullRequestId=" + strconv.Itoa(head.ID),
				"--api-version", "7.1-preview.1", "--http-method", "GET"}, a.org()...)...)
		})
		wg.Wait()
		if perr != nil {
			return PR{}, perr
		}
	}
	pr, err := azurePR(a.Target, raw, policies, statuses)
	if err != nil || pr.Number == 0 {
		return PR{}, ErrNoPR
	}
	return pr, nil
}

// ours reports whether pr is of the Target's project and repo (names
// are case-insensitive on Azure DevOps). A PR number can name any PR of
// the organization; only this repo's are followed.
func (a Azure) ours(pr azPR) bool {
	return strings.EqualFold(pr.Repository.Name, a.Target.Repo) && strings.EqualFold(pr.Repository.Project.Name, a.Target.Project)
}

// pickAzurePR is the PR to follow of `az repos pr list`'s answer: the
// active one with the highest id, else the highest id; nil for none.
func pickAzurePR(list []byte) []byte {
	var prs []json.RawMessage
	if json.Unmarshal(list, &prs) != nil {
		return nil
	}
	var best json.RawMessage
	bestActive, bestID := false, 0
	for _, raw := range prs {
		var p struct {
			ID     int    `json:"pullRequestId"`
			Status string `json:"status"`
		}
		if json.Unmarshal(raw, &p) != nil || p.ID <= 0 {
			continue
		}
		active := p.Status == "active"
		if best == nil || active && !bestActive || active == bestActive && p.ID > bestID {
			best, bestActive, bestID = raw, active, p.ID
		}
	}
	return best
}

// azCommit is a GitCommitRef as a PR names it.
type azCommit struct {
	CommitID string `json:"commitId"`
}

// azPR is the part of a GitPullRequest tm reads: no title, description
// or names but the repo's and project's, which are only compared.
type azPR struct {
	ID                    int       `json:"pullRequestId"`
	Status                string    `json:"status"`
	IsDraft               bool      `json:"isDraft"`
	MergeStatus           string    `json:"mergeStatus"`
	SourceRefName         string    `json:"sourceRefName"`
	TargetRefName         string    `json:"targetRefName"`
	ClosedDate            string    `json:"closedDate"`
	LastMergeSourceCommit *azCommit `json:"lastMergeSourceCommit"`
	LastMergeCommit       *azCommit `json:"lastMergeCommit"`
	Reviewers             []struct {
		Vote        int  `json:"vote"`
		IsRequired  bool `json:"isRequired"`
		HasDeclined bool `json:"hasDeclined"`
	} `json:"reviewers"`
	Repository struct {
		Name    string `json:"name"`
		Project struct {
			Name string `json:"name"`
		} `json:"project"`
	} `json:"repository"`
}

// azPolicy is one PolicyEvaluationRecord of `az repos pr policy list`.
type azPolicy struct {
	Status        string `json:"status"`
	Configuration struct {
		IsBlocking *bool `json:"isBlocking"`
		IsEnabled  *bool `json:"isEnabled"`
		Type       struct {
			ID          string `json:"id"`
			DisplayName string `json:"displayName"`
		} `json:"type"`
		Settings struct {
			StatusName  string `json:"statusName"`
			StatusGenre string `json:"statusGenre"`
		} `json:"settings"`
	} `json:"configuration"`
}

// azStatus is one GitPullRequestStatus.
type azStatus struct {
	ID      int    `json:"id"`
	State   string `json:"state"`
	Context struct {
		Name  string `json:"name"`
		Genre string `json:"genre"`
	} `json:"context"`
}

// Policy types by their well-known ids (the same in every
// organization), with their English names for a record without one.
const (
	policyBuild        = "0609b952-1397-4640-95ec-e00a01b2c241"
	policyStatus       = "cbdc66da-9728-4af8-aada-9a5a32e4a226"
	policyMinReviewers = "fa4e907d-c16b-4a4c-9dfa-4906e5d171dd"
	policyReviewers    = "fd2167ab-b0be-447a-8ec8-39368250530e"
)

// policyKind is "ci" for a build or status policy, "review" for a
// reviewer one, "" for the rest (comments, work items, merge
// strategy…), which are no check and no review.
func policyKind(p azPolicy) string {
	switch t := p.Configuration.Type; {
	case strings.EqualFold(t.ID, policyBuild), strings.EqualFold(t.ID, policyStatus),
		t.ID == "" && (t.DisplayName == "Build" || t.DisplayName == "Status"):
		return "ci"
	case strings.EqualFold(t.ID, policyMinReviewers), strings.EqualFold(t.ID, policyReviewers),
		t.ID == "" && (t.DisplayName == "Minimum number of reviewers" || t.DisplayName == "Required reviewers"):
		return "review"
	}
	return ""
}

// azurePR maps az's answers onto the fixed fields (docs/SPEC.md §7.5):
// pr is a GitPullRequest, policies `az repos pr policy list`'s records
// and statuses `az devops invoke`'s list of PR statuses (both may be
// nil). t is the repo it was asked for: the URL is built from it, never
// taken from the answer. Anything outside the expected shapes is
// dropped, not passed on.
//
//   - status active, completed, abandoned => OPEN, MERGED, CLOSED; a
//     draft is OPEN.
//   - mergeStatus succeeded or rejectedByPolicy => MERGEABLE,
//     conflicts => CONFLICTING (MergeState DIRTY), the rest (notSet,
//     queued, failure) => UNKNOWN.
//   - Checks are the build and status policies and the statuses no
//     status policy covers: rejected, broken, failed, error => fail;
//     queued, running, pending => pending, of a blocking policy only (an
//     optional build may never be queued); approved, succeeded => pass;
//     notApplicable => nothing. Reviewer, comment and other policies are
//     no checks.
//   - Review: any vote of -5 (waiting for the author) or -10 (rejected)
//     => CHANGES_REQUESTED; else a blocking reviewer policy not yet
//     approved, or a required reviewer without a vote of 5 or 10 =>
//     REVIEW_REQUIRED (not for a draft, whose reviewers aren't asked
//     yet); else any vote of 5 or 10 => APPROVED; else "".
//   - closedDate => MergedAt, lastMergeCommit => Merge (both MERGED
//     only), lastMergeSourceCommit => Head, targetRefName => Base.
func azurePR(t Target, pr, policies, statuses []byte) (PR, error) {
	var a azPR
	if err := json.Unmarshal(pr, &a); err != nil {
		return PR{}, err
	}
	var out PR
	if a.ID > 0 && a.ID < 1e9 {
		out.Number = a.ID
		out.URL = AzurePRURL(t, a.ID)
	}
	switch a.Status {
	case "active":
		out.State = "OPEN"
	case "completed":
		out.State = "MERGED"
	case "abandoned":
		out.State = "CLOSED"
	}
	switch a.MergeStatus {
	case "succeeded", "rejectedByPolicy":
		out.Mergeable = "MERGEABLE"
	case "conflicts":
		out.Mergeable, out.MergeState = "CONFLICTING", "DIRTY"
	default:
		out.Mergeable = "UNKNOWN"
	}
	if at, err := time.Parse(time.RFC3339Nano, a.ClosedDate); err == nil && out.State == "MERGED" && at.Year() > 2000 {
		out.MergedAt = at.UTC()
	}
	if c := a.LastMergeSourceCommit; c != nil && oidRE.MatchString(c.CommitID) {
		out.Head = c.CommitID
	}
	if c := a.LastMergeCommit; c != nil && out.State == "MERGED" && oidRE.MatchString(c.CommitID) {
		out.Merge = c.CommitID
	}
	if b, ok := strings.CutPrefix(a.TargetRefName, "refs/heads/"); ok && branchRE.MatchString(b) {
		out.Base = b
	}

	var pols []azPolicy
	json.Unmarshal(policies, &pols) // none when it isn't a list of them
	var sts struct {
		Value []azStatus `json:"value"`
	}
	if json.Unmarshal(statuses, &sts) != nil {
		json.Unmarshal(statuses, &sts.Value) // a bare list
	}

	pending, ran := false, false
	reviewBlocked := false
	covered := map[string]bool{} // genre/name of the status policies
	for _, p := range pols {
		if p.Configuration.IsEnabled != nil && !*p.Configuration.IsEnabled {
			continue
		}
		blocking := p.Configuration.IsBlocking == nil || *p.Configuration.IsBlocking
		switch policyKind(p) {
		case "ci":
			if strings.EqualFold(p.Configuration.Type.ID, policyStatus) || p.Configuration.Type.DisplayName == "Status" {
				covered[strings.ToLower(p.Configuration.Settings.StatusGenre+"/"+p.Configuration.Settings.StatusName)] = true
			}
			switch p.Status {
			case "rejected", "broken":
				out.Failed++
				ran = true
			case "queued", "running":
				if blocking {
					pending, ran = true, true
				}
			case "approved":
				ran = true
			}
		case "review":
			if blocking && p.Status != "approved" && p.Status != "notApplicable" {
				reviewBlocked = true
			}
		}
	}
	// Each status context's latest status (the highest id) counts.
	latest := map[string]azStatus{}
	for _, s := range sts.Value {
		key := strings.ToLower(s.Context.Genre + "/" + s.Context.Name)
		if covered[key] {
			continue
		}
		if old, ok := latest[key]; !ok || s.ID > old.ID {
			latest[key] = s
		}
	}
	for _, s := range latest {
		switch s.State {
		case "failed", "error":
			out.Failed++
			ran = true
		case "pending":
			pending, ran = true, true
		case "succeeded":
			ran = true
		}
	}
	switch {
	case out.Failed > 0:
		out.Checks = "fail"
	case pending:
		out.Checks = "pending"
	case ran:
		out.Checks = "pass"
	}

	approved, waiting, missing := false, false, false
	for _, r := range a.Reviewers {
		switch {
		case r.Vote <= -5:
			waiting = true
		case r.Vote >= 5:
			approved = true
		case r.IsRequired && !r.HasDeclined:
			missing = true
		}
	}
	switch {
	case waiting:
		out.Review = "CHANGES_REQUESTED"
	case (reviewBlocked || missing) && !a.IsDraft:
		out.Review = "REVIEW_REQUIRED"
	case approved && !reviewBlocked && !missing:
		out.Review = "APPROVED"
	}
	return out, nil
}

// AzurePRURL is the web URL of PR n of t's repo,
// https://dev.azure.com/org/project/_git/repo/pullrequest/n.
func AzurePRURL(t Target, n int) string {
	return fmt.Sprintf("%s/%s/_git/%s/pullrequest/%d", t.OrgURL, url.PathEscape(t.Project), url.PathEscape(t.Repo), n)
}

// azPRURLRE is the tail of an Azure DevOps PR web URL.
var azPRURLRE = regexp.MustCompile(`^(https://[^?#]+/_git/[^/?#]+)/pullrequest/([0-9]{1,9})/?$`)

// ParsePRURL is the number of a PR URL of this repo (the forms of
// ParseRemote's https ones plus /pullrequest/N); a PR URL of any other
// organization, project or repo is not this host's.
func (a Azure) ParsePRURL(s string) (int, bool) {
	m := azPRURLRE.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return 0, false
	}
	t, ok := ParseRemote(m[1])
	if !ok || !strings.EqualFold(t.OrgURL, a.Target.OrgURL) || !strings.EqualFold(t.Project, a.Target.Project) || !strings.EqualFold(t.Repo, a.Target.Repo) {
		return 0, false
	}
	n, err := strconv.Atoi(m[2])
	return n, err == nil && n > 0
}

// FailedLog: the failing build's log is a later step (build timeline
// and logs through az devops invoke); none yet.
func (Azure) FailedLog(repo string, pr PR) (job, log string) { return "", "" }

// PRState is the state of the PR whose head is branch (PR by branch).
func (a Azure) PRState(repo, branch string) string {
	pr, err := a.PR(repo, Ref{Branch: branch})
	if err != nil {
		return ""
	}
	return pr.State
}

// PRHead asks az for the state, number and source branch of the PR at
// url, one of this repo's.
func (a Azure) PRHead(repo, prURL string) (state string, number int, head string) {
	n, ok := a.ParsePRURL(prURL)
	if !ok {
		return "", 0, ""
	}
	out, err := a.az(repo, append([]string{"repos", "pr", "show", "--id", strconv.Itoa(n)}, a.org()...)...)
	if err != nil {
		return "", 0, ""
	}
	var p azPR
	if json.Unmarshal(out, &p) != nil || p.ID != n || !a.ours(p) {
		return "", 0, ""
	}
	pr, _ := azurePR(a.Target, out, nil, nil)
	b, ok := strings.CutPrefix(p.SourceRefName, "refs/heads/")
	if pr.State == "" || !ok || !branchRE.MatchString(b) {
		return "", 0, ""
	}
	return pr.State, n, b
}

// MergeCommit and MergedPR read Azure DevOps' merge commit subjects
// ("Merged PR 12: …"), a later step; the PR's state says it until then.
func (Azure) MergeCommit(repo string, n int) string { return "" }
func (Azure) MergedPR(repo, commit string) int      { return 0 }

// azNameRE is an organization URL, project or repo name a prompt may
// show; any other leaves the command out.
var (
	azNameRE   = regexp.MustCompile(`^[A-Za-z0-9._-][A-Za-z0-9._ -]{0,63}$`)
	azOrgURLRE = regexp.MustCompile(`^https://[A-Za-z0-9.-]+(/[A-Za-z0-9._-]+){0,3}$`)
)

// shellWord quotes s for a prompt's command when it has a space.
func shellWord(s string) string {
	if strings.Contains(s, " ") {
		return "'" + s + "'"
	}
	return s
}

func (a Azure) Hints(n int) Hints {
	t := a.Target
	if !azOrgURLRE.MatchString(t.OrgURL) || !azNameRE.MatchString(t.Project) || !azNameRE.MatchString(t.Repo) {
		return Hints{
			Checks: fmt.Sprintf("az repos pr policy list --id %d", n),
			Review: fmt.Sprintf("az repos pr show --id %d (comments are on the PR's page)", n),
		}
	}
	return Hints{
		Checks: fmt.Sprintf("az repos pr policy list --id %d --organization %s -o table", n, t.OrgURL),
		Review: fmt.Sprintf("az devops invoke --area git --resource pullRequestThreads --route-parameters project=%s repositoryId=%s pullRequestId=%d --organization %s",
			shellWord(t.Project), shellWord(t.Repo), n, t.OrgURL),
	}
}

// Doctor checks that az is installed, has the azure-devops extension
// and is logged in: without them the ticker's PR polls fail.
func (a Azure) Doctor(d DoctorDeps) []Check {
	p, err := d.LookPath("az")
	if err != nil {
		return []Check{{Name: "az", Detail: "not found; PRs on Azure DevOps aren't followed: install the Azure CLI"}}
	}
	checks := []Check{{Name: "az", OK: true, Detail: "found"}}
	ext := Check{Name: "az azure-devops", OK: true, Detail: "extension installed"}
	if _, err := d.Run("", p, "extension", "show", "--name", "azure-devops", "--only-show-errors", "--output", "none"); err != nil {
		ext = Check{Name: "az azure-devops", Detail: "extension missing: run az extension add --name azure-devops"}
	}
	auth := Check{Name: "az login", OK: true, Detail: "logged in"}
	if _, err := d.Run("", p, "account", "show", "--only-show-errors", "--output", "none"); err != nil {
		auth = Check{Name: "az login", Detail: "not logged in: run az login (PR follow-up, auto-close and completing tasks need it)"}
	}
	return append(checks, ext, auth)
}
