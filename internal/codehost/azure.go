package codehost

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os/exec"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/theclifmeister/terminatr/internal/plat/shell"
)

// Azure is the Azure DevOps host: its REST API, asked through az rest
// with az login's token, else with AZURE_DEVOPS_EXT_PAT (azure_rest.go,
// docs/SPEC.md §7.5). Every URL is built from Target: never what az
// would detect from the checkout.
type Azure struct {
	Target Target
	// Run runs az in dir; nil runs the real one (RunAZ).
	Run func(dir string, args ...string) ([]byte, error)
	// PATGet asks a URL with a PAT; nil runs PATGet.
	PATGet func(url, pat string) ([]byte, error)
	// Getenv reads the environment; nil is os.Getenv.
	Getenv func(string) string
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
	cmd := shell.CLICommand(ctx, "az", args...)
	cmd.Dir = dir
	cmd.Env = append(cmd.Environ(),
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
	// auth: az can't sign in, or Azure DevOps refuses whom it signs in
	// as; a PAT may do (Azure.get).
	auth bool
}

func (e *CLIError) Error() string { return e.Err.Error() }
func (e *CLIError) Unwrap() error { return e.Err }

// azProblems are the az failures tm can name, first match wins. az
// rest follows a 401 with "Interactive authentication is needed. Please
// run: az logout, az login": Azure DevOps' refusals come before that.
var azProblems = []struct {
	re              *regexp.Regexp
	problem, advice string
	auth            bool
}{
	{regexp.MustCompile(`(?i)please run 'az login'|AADSTS|not logged in|refresh token|need to run the login command`),
		"az is not logged in", "the user runs az login in a terminal", true},
	// A user the organization doesn't have: az login's account isn't a
	// member, or is of another tenant.
	{regexp.MustCompile(`TF400813`),
		"Azure DevOps doesn't know the user az signs in as (TF400813)", "the user checks that their az login account is in the organization, or runs az login --tenant with the organization's tenant (tm doctor)", true},
	{regexp.MustCompile(`(?i)\b40[13]\b|unauthori[sz]ed|forbidden|not authorized|does not have permissions?|access denied`),
		"Azure DevOps refused access (401/403)", "the user checks in a terminal that their az login can read this repo (tm doctor)", true},
	{regexp.MustCompile(`(?i)interactive authentication is needed`),
		"az is not logged in", "the user runs az login in a terminal", true},
	{regexp.MustCompile(`(?i)TF200016|TF401019|project .* does not exist|repository .* does not exist|could not be found`),
		"Azure DevOps doesn't know this organization, project or repo", "the user checks the repo's origin URL or the project's azure_url", false},
}

// azError wraps err in a CLIError naming what went wrong.
func azError(err error) error {
	e := &CLIError{CLI: "az", Err: err}
	if errors.Is(err, exec.ErrNotFound) {
		e.Problem, e.Advice, e.auth = "az is not installed", "the user installs the Azure CLI (tm doctor)", true
		return e
	}
	for _, p := range azProblems {
		if p.re.MatchString(err.Error()) {
			e.Problem, e.Advice, e.auth = p.problem, p.advice, p.auth
			break
		}
	}
	return e
}

// azNoPR matches the errors of an az that works but has no such PR.
var azNoPR = regexp.MustCompile(`(?i)TF401180|pull request .*(not found|does not exist)|could not find .*pull request`)

// projectURL is the REST URL of the Target's project, repoURL its
// repo's.
func (a Azure) projectURL() string {
	return a.Target.OrgURL + "/" + url.PathEscape(a.Target.Project)
}

func (a Azure) repoURL() string {
	return a.projectURL() + "/_apis/git/repositories/" + url.PathEscape(a.Target.Repo)
}

// prURL is the REST URL of PR n of the Target's repo.
func (a Azure) prURL(n int) string {
	return a.repoURL() + "/pullrequests/" + strconv.Itoa(n) + "?api-version=7.1"
}

// PR asks Azure DevOps about ref: the PR by number (or a PR URL of this
// repo), else the repo's PRs by source branch, preferring an active PR,
// else the newest. An open PR's policy evaluations and statuses are
// asked too, at once. A PR of another repo is ErrNoPR.
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
		out, err := a.get(repo, a.prURL(n))
		if err != nil {
			if azNoPR.MatchString(err.Error()) {
				return PR{}, fmt.Errorf("%w: %v", ErrNoPR, err)
			}
			return PR{}, err
		}
		raw = out
	} else {
		out, err := a.get(repo, a.repoURL()+"/pullrequests?searchCriteria.sourceRefName="+url.QueryEscape("refs/heads/"+branch)+
			"&searchCriteria.status=all&$top=20&api-version=7.1")
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
		// The policies are the project's by id, which the PR names.
		if pid := head.Repository.Project.ID; guidRE.MatchString(pid) {
			wg.Go(func() {
				policies, perr = a.get(repo, a.projectURL()+"/_apis/policy/evaluations?artifactId="+
					url.QueryEscape("vstfs:///CodeReview/CodeReviewId/"+pid+"/"+strconv.Itoa(head.ID))+"&api-version=7.1-preview.1")
			})
		}
		wg.Go(func() {
			// External statuses are extra: without them the policies
			// still say what blocks the PR.
			statuses, _ = a.get(repo, fmt.Sprintf("%s/pullRequests/%d/statuses?api-version=7.1-preview.1", a.repoURL(), head.ID))
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

// pickAzurePR is the PR to follow of a list of PRs ({"value":[…]}, or a
// bare list): the active one with the highest id, else the highest id;
// nil for none.
func pickAzurePR(list []byte) []byte {
	prs := azValues(list)
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
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"project"`
	} `json:"repository"`
}

// azValues is the items of a REST list, {"count":n,"value":[…]}, or of
// a bare list; nil for anything else.
func azValues(b []byte) []json.RawMessage {
	var obj struct {
		Value []json.RawMessage `json:"value"`
	}
	if json.Unmarshal(b, &obj) == nil && obj.Value != nil {
		return obj.Value
	}
	var list []json.RawMessage
	if json.Unmarshal(b, &list) == nil {
		return list
	}
	return nil
}

// azPolicy is one PolicyEvaluationRecord.
type azPolicy struct {
	Status string `json:"status"`
	// Context names a build policy's build, nil before one is queued;
	// IsExpired once the target branch moved past it (it isn't requeued
	// by itself).
	Context *struct {
		BuildID   int  `json:"buildId"`
		IsExpired bool `json:"isExpired"`
	} `json:"context"`
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

// isStatusPolicy reports whether p is a status policy (an external
// service's status, not a build).
func isStatusPolicy(p azPolicy) bool {
	t := p.Configuration.Type
	return strings.EqualFold(t.ID, policyStatus) || t.ID == "" && t.DisplayName == "Status"
}

// azurePR maps Azure DevOps' answers onto the fixed fields
// (docs/SPEC.md §7.5): pr is a GitPullRequest, policies the PR's policy
// evaluation records and statuses its statuses (both lists, either
// form of azValues, and may be nil). t is the repo it was asked for: the URL is built from it, never
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
//     optional build may never be queued) and, for a queued build
//     policy, only while it names a build that hasn't expired (a draft
//     or a manual-queue policy has none, and an expired one isn't
//     requeued by itself); approved, succeeded => pass;
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
	for _, raw := range azValues(policies) {
		var p azPolicy
		if json.Unmarshal(raw, &p) == nil {
			pols = append(pols, p)
		}
	}
	var sts struct{ Value []azStatus }
	for _, raw := range azValues(statuses) {
		var st azStatus
		if json.Unmarshal(raw, &st) == nil {
			sts.Value = append(sts.Value, st)
		}
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
			if isStatusPolicy(p) {
				covered[strings.ToLower(p.Configuration.Settings.StatusGenre+"/"+p.Configuration.Settings.StatusName)] = true
			}
			switch p.Status {
			case "rejected", "broken":
				out.Failed++
				ran = true
			case "queued", "running":
				// A build policy is queued without a build on a draft
				// (drafts aren't built) or a manual-queue policy, and
				// with an expired one once the target branch moved: no
				// check is running then, and none may until someone
				// queues it or the PR is updated.
				noBuild := policyKind(p) == "ci" && !isStatusPolicy(p) && (p.Context == nil || p.Context.BuildID <= 0 || p.Context.IsExpired)
				if blocking && !(p.Status == "queued" && noBuild) {
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

// ParsePRURL is the number of a PR URL of this repo (OwnsPRURL); a PR
// URL of any other organization, project or repo is not this host's.
func (a Azure) ParsePRURL(s string) (int, bool) {
	if !OwnsPRURL(a.Target, s) {
		return 0, false
	}
	l, _ := ParsePR(s)
	return l.Number, true
}

// PRState is the state of the PR whose head is branch (PR by branch).
func (a Azure) PRState(repo, branch string) string {
	pr, err := a.PR(repo, Ref{Branch: branch})
	if err != nil {
		return ""
	}
	return pr.State
}

// PRHead asks Azure DevOps for the state, number and source branch of
// the PR at url, one of this repo's.
func (a Azure) PRHead(repo, prURL string) (state string, number int, head string) {
	n, ok := a.ParsePRURL(prURL)
	if !ok {
		return "", 0, ""
	}
	out, err := a.get(repo, a.prURL(n))
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

// azNameRE is an organization URL, project or repo name a prompt may
// show; any other leaves the command out.
var (
	azNameRE   = regexp.MustCompile(`^[A-Za-z0-9._-][A-Za-z0-9._ -]{0,63}$`)
	azOrgURLRE = regexp.MustCompile(`^https://[A-Za-z0-9.-]+(/[A-Za-z0-9._-]+){0,3}$`)
)

// Hints are az rest GETs a thread can run: the PR's builds (its merge
// ref's) and its people's comments. Single-quoted URLs: names are of
// azNameRE, spaces escaped.
func (a Azure) Hints(n int) Hints {
	t := a.Target
	if !azOrgURLRE.MatchString(t.OrgURL) || !azNameRE.MatchString(t.Project) || !azNameRE.MatchString(t.Repo) {
		return Hints{
			Checks: fmt.Sprintf("the checks on PR %d's page", n),
			Review: fmt.Sprintf("the comments on PR %d's page", n),
		}
	}
	return Hints{
		Checks: fmt.Sprintf("az rest --resource %s --url '%s/_apis/build/builds?branchName=refs/pull/%d/merge&api-version=7.1' --query 'value[].{build:id,status:status,result:result}' -o table",
			azResource, a.projectURL(), n),
		Review: fmt.Sprintf("az rest --resource %s --url '%s/pullRequests/%d/threads?api-version=7.1' --query \"value[].comments[?commentType=='text'][].{author:author.displayName,text:content}\" -o table",
			azResource, a.repoURL(), n),
	}
}

// Doctor checks that az is installed and logged in, or that
// AZURE_DEVOPS_EXT_PAT is set (never printed): without one the ticker's
// PR polls fail. tm doesn't need the azure-devops extension: it asks the
// REST API with az rest, and so may threads. Access to one repo is Access.
func (a Azure) Doctor(d DoctorDeps) []Check {
	pat := d.Getenv != nil && d.Getenv(patEnv) != ""
	p, err := d.LookPath("az")
	if err != nil {
		if pat {
			return []Check{{Name: "az", OK: true, Detail: "not found; " + patEnv + " is set, which tm asks Azure DevOps with"}}
		}
		return []Check{{Name: "az", Detail: "not found; PRs on Azure DevOps aren't followed: install the Azure CLI and run az login"}}
	}
	checks := []Check{{Name: "az", OK: true, Detail: "found"}}
	auth := Check{Name: "az login", OK: true, Detail: "logged in"}
	if _, err := d.Run("", p, "account", "show", "--only-show-errors", "--output", "none"); err != nil {
		if pat {
			auth.Detail = patEnv + " is set"
		} else {
			auth = Check{Name: "az login", Detail: "not logged in: run az login (PR follow-up, auto-close and completing tasks need it)"}
		}
	}
	return append(checks, auth)
}

// Access checks that tm can read this repo as it asks Azure DevOps (az
// rest, else the PAT), which proves the login reaches its organization
// and project, and names the cure when it can't: az login, the
// organization's tenant, the origin URL. A Target az can't be given
// safely (an odd name) is skipped.
func (a Azure) Access(d DoctorDeps) []Check {
	t := a.Target
	if !azOrgURLRE.MatchString(t.OrgURL) || !azNameRE.MatchString(t.Project) || !azNameRE.MatchString(t.Repo) {
		return nil
	}
	p, lerr := d.LookPath("az")
	getenv := d.Getenv
	if getenv == nil {
		getenv = func(string) string { return "" }
	}
	patGet := d.PATGet
	if patGet == nil {
		patGet = a.PATGet
	}
	b := Azure{Target: t, Getenv: getenv, PATGet: patGet, Run: func(dir string, args ...string) ([]byte, error) {
		if lerr != nil {
			return nil, azError(fmt.Errorf("az: %w", exec.ErrNotFound))
		}
		out, err := d.Run(dir, p, append(slices.Clip(args), "--only-show-errors", "--output", "json")...)
		if err != nil {
			return nil, azError(err)
		}
		return []byte(out), nil
	}}
	name := "az repo " + t.Project + "/" + t.Repo
	u := b.repoURL() + "?api-version=7.1"
	_, err := b.get("", u)
	if err == nil {
		return []Check{{Name: name, OK: true, Detail: "readable"}}
	}
	var ce *CLIError
	errors.As(err, &ce)
	problem := ""
	if ce != nil {
		problem = ce.Problem
	}
	detail := "can't read it: check az login, and that your account is in " + t.OrgURL + " with access to the project"
	switch {
	case problem == "az is not installed":
		detail = "can't read it: install the Azure CLI and run az login, or set " + patEnv
	case problem == "az is not logged in":
		detail = "can't read it: az isn't logged in: run az login, or set " + patEnv
	case strings.Contains(problem, patEnv):
		detail = "can't read it with " + patEnv + " (401/403): check that it hasn't expired and has the Code and Build read scopes"
	case isRefused(err):
		detail = "Azure DevOps refuses the user az signs in as (" + refusal(problem) + "): check that your az login account is in " + t.OrgURL
		if _, tenant, has := b.tenantAccount(""); tenant != "" && !has {
			detail += "; the organization is in Microsoft Entra tenant " + tenant + ", which az login has no account in: run az login --tenant " + tenant
		}
	case problem != "":
		detail = "can't read it: " + problem + " (" + t.OrgURL + "/" + t.Project + "/_git/" + t.Repo + "): check the repo's origin URL or the project's azure_url"
	}
	return []Check{{Name: name, Detail: detail}}
}

// refusal is the code a refusal problem names.
func refusal(problem string) string {
	if strings.Contains(problem, "TF400813") {
		return "TF400813"
	}
	return "401/403"
}
