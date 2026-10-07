package codehost

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// No test asks the network for an organization's tenant; the one that
// tests the retry sets its own.
func init() { orgTenant = func(string) string { return "" } }

// shop is the repo the Azure fixtures are of.
var shop = Target{Kind: AzureKind, OrgURL: "https://dev.azure.com/acme", Project: "Shop", Repo: "web"}

func fixture(t testing.TB, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "azure", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// TestAzurePRFixtures: Azure DevOps' answers as tm asks them (az rest,
// captured from a live organization, testdata/azure/README.md), onto
// the fixed fields.
func TestAzurePRFixtures(t *testing.T) {
	for _, c := range []struct {
		pr, policies, statuses string
		want                   PR
		summary                string
	}{
		// a required reviewer who hasn't voted; the build policy's build
		// expired when main moved (not running, not pending); lint's
		// latest status succeeded, the coverage service's is
		// notApplicable
		{"pr-active.json", "policies-approved.json", "statuses.json",
			PR{Number: 1, State: "OPEN", Checks: "pass", Review: "REVIEW_REQUIRED", Head: "f487d85c7bc862efcca47c4eed97232849a86168", Base: "main", Mergeable: "MERGEABLE"},
			"#1 open, checks pass, review required"},
		// the build policy rejected, lint failed; a vote of -5
		{"pr-failed.json", "policies.json", "statuses-failed.json",
			PR{Number: 2, State: "OPEN", Checks: "fail", Failed: 2, Review: "CHANGES_REQUESTED", Head: "b690915c44588901482bbea21bcab55a6a478b15", Base: "main", Mergeable: "MERGEABLE"},
			"#2 open, 2 checks failed, changes requested"},
		// a draft: its build policy queued with no build
		{"pr-draft.json", "policies-draft.json", "",
			PR{Number: 3, State: "OPEN", Head: "4c2918eb25297e3d7172c1bd00c0a61feb69c28b", Base: "main", Mergeable: "MERGEABLE"}, "#3 open"},
		// abandoned: no mergeStatus at all
		{"pr-abandoned.json", "", "",
			PR{Number: 4, State: "CLOSED", Head: "c002a528a7c4ce4eb756a2f81a941c00dc4ca725", Base: "main", Mergeable: "UNKNOWN"}, "#4 closed"},
		// completed by hand, merge (no fast-forward), "Merged PR 5: …"
		{"pr-completed.json", "", "",
			PR{Number: 5, State: "MERGED", MergedAt: time.Date(2026, 10, 7, 12, 14, 12, 798581000, time.UTC), Head: "c5db4777afb846d0a8e258006b2778d7c4500d91",
				Merge: "aa2ef614651f363bd8c383e119ff4fb884abf64a", Base: "main", Mergeable: "MERGEABLE"}, "#5 merged"},
		// conflicts with its target, another branch
		{"pr-conflicts.json", "", "",
			PR{Number: 6, State: "OPEN", Head: "74013a6fddf1ae08004511adfc0dfc107ce2c6e3", Base: "feature/base", Mergeable: "CONFLICTING", MergeState: "DIRTY"}, "#6 open, conflicts"},
	} {
		var pol, st []byte
		if c.policies != "" {
			pol = fixture(t, c.policies)
		}
		if c.statuses != "" {
			st = fixture(t, c.statuses)
		}
		pr, err := azurePR(shop, fixture(t, c.pr), pol, st)
		if err != nil {
			t.Fatalf("%s: %v", c.pr, err)
		}
		c.want.URL = AzurePRURL(shop, c.want.Number)
		if pr != c.want {
			t.Errorf("%s:\n got %+v\nwant %+v", c.pr, pr, c.want)
		}
		if pr.Summary() != c.summary {
			t.Errorf("%s: summary %q", c.pr, pr.Summary())
		}
	}
}

// azJSON is a GitPullRequest of shop with the given fields.
func azJSON(status, mergeStatus string, draft bool, votes ...string) string {
	return fmt.Sprintf(`{"pullRequestId":5,"status":%q,"mergeStatus":%q,"isDraft":%v,"closedDate":"2026-10-07T10:00:00.1234567Z",`+
		`"lastMergeCommit":{"commitId":"%s"},"targetRefName":"refs/heads/main","repository":{"name":"web","project":{"name":"Shop"}},"reviewers":[%s]}`,
		status, mergeStatus, draft, strings.Repeat("ab", 20), strings.Join(votes, ","))
}

// policy is one evaluation record: kind build, status, minreviewers,
// comments; a build's names its build.
func policy(kind, status string, blocking bool) string {
	ids := map[string]string{"build": policyBuild, "status": policyStatus, "minreviewers": policyMinReviewers, "reviewers": policyReviewers, "comments": "c6a1889d-b943-4856-b76f-9e46bb6b0df2"}
	ctx := "null"
	if kind == "build" {
		ctx = `{"buildId":42}`
	}
	return fmt.Sprintf(`{"status":%q,"configuration":{"isBlocking":%v,"isEnabled":true,"type":{"id":%q}},"context":%s}`, status, blocking, ids[kind], ctx)
}

func vote(v int, required bool) string {
	return fmt.Sprintf(`{"vote":%d,"isRequired":%v}`, v, required)
}

// TestAzurePRMatrix: status x mergeStatus x votes x policies.
func TestAzurePRMatrix(t *testing.T) {
	list := func(p ...string) string { return "[" + strings.Join(p, ",") + "]" }
	for _, c := range []struct {
		name     string
		pr       string
		policies string
		statuses string
		want     PR // Number, URL and Head are checked once below
	}{
		{"active no policies", azJSON("active", "succeeded", false), "[]", "", PR{State: "OPEN", Mergeable: "MERGEABLE"}},
		{"completed", azJSON("completed", "succeeded", false), "", "", PR{State: "MERGED", Mergeable: "MERGEABLE",
			MergedAt: time.Date(2026, 10, 7, 10, 0, 0, 123456700, time.UTC), Merge: strings.Repeat("ab", 20)}},
		{"abandoned", azJSON("abandoned", "notSet", false), "", "", PR{State: "CLOSED", Mergeable: "UNKNOWN"}},
		{"unknown status", azJSON("notSet", "succeeded", false), "", "", PR{Mergeable: "MERGEABLE"}},
		{"queued merge", azJSON("active", "queued", false), "", "", PR{State: "OPEN", Mergeable: "UNKNOWN"}},
		{"conflicts", azJSON("active", "conflicts", false), "", "", PR{State: "OPEN", Mergeable: "CONFLICTING", MergeState: "DIRTY"}},
		{"rejected by policy", azJSON("active", "rejectedByPolicy", false), "", "", PR{State: "OPEN", Mergeable: "MERGEABLE"}},
		{"merge failure", azJSON("active", "failure", false), "", "", PR{State: "OPEN", Mergeable: "UNKNOWN"}},

		{"build running", azJSON("active", "succeeded", false), list(policy("build", "running", true)), "", PR{State: "OPEN", Mergeable: "MERGEABLE", Checks: "pending"}},
		{"build queued", azJSON("active", "succeeded", false), list(policy("build", "queued", true)), "", PR{State: "OPEN", Mergeable: "MERGEABLE", Checks: "pending"}},
		{"build queued, no build yet", azJSON("active", "succeeded", true), `[{"status":"queued","configuration":{"isBlocking":true,"isEnabled":true,"type":{"id":"` + policyBuild + `"}},"context":null}]`, "",
			PR{State: "OPEN", Mergeable: "MERGEABLE"}},
		{"build queued, expired", azJSON("active", "succeeded", false), `[{"status":"queued","configuration":{"isBlocking":true,"isEnabled":true,"type":{"id":"` + policyBuild + `"}},"context":{"buildId":5,"isExpired":true}}]`, "",
			PR{State: "OPEN", Mergeable: "MERGEABLE"}},
		{"status policy queued", azJSON("active", "succeeded", false), list(policy("status", "queued", true)), "", PR{State: "OPEN", Mergeable: "MERGEABLE", Checks: "pending"}},
		{"build approved", azJSON("active", "succeeded", false), list(policy("build", "approved", true)), "", PR{State: "OPEN", Mergeable: "MERGEABLE", Checks: "pass"}},
		{"build broken", azJSON("active", "succeeded", false), list(policy("build", "broken", true)), "", PR{State: "OPEN", Mergeable: "MERGEABLE", Checks: "fail", Failed: 1}},
		{"build not applicable", azJSON("active", "succeeded", false), list(policy("build", "notApplicable", true)), "", PR{State: "OPEN", Mergeable: "MERGEABLE"}},
		{"optional build queued", azJSON("active", "succeeded", false), list(policy("build", "queued", false)), "", PR{State: "OPEN", Mergeable: "MERGEABLE"}},
		{"optional build failed", azJSON("active", "succeeded", false), list(policy("build", "rejected", false), policy("build", "approved", true)), "",
			PR{State: "OPEN", Mergeable: "MERGEABLE", Checks: "fail", Failed: 1}},
		{"two failed, one pending", azJSON("active", "succeeded", false), list(policy("build", "rejected", true), policy("status", "rejected", true), policy("build", "running", true)), "",
			PR{State: "OPEN", Mergeable: "MERGEABLE", Checks: "fail", Failed: 2}},
		{"comments and reviewers aren't checks", azJSON("active", "succeeded", false), list(policy("comments", "queued", true), policy("minreviewers", "approved", true)), "",
			PR{State: "OPEN", Mergeable: "MERGEABLE"}},
		{"disabled policy", azJSON("active", "succeeded", false), `[{"status":"rejected","configuration":{"isEnabled":false,"isBlocking":true,"type":{"id":"` + policyBuild + `"}}}]`, "",
			PR{State: "OPEN", Mergeable: "MERGEABLE"}},
		{"policy named only", azJSON("active", "succeeded", false), `[{"status":"rejected","configuration":{"isBlocking":true,"type":{"displayName":"Build"}}}]`, "",
			PR{State: "OPEN", Mergeable: "MERGEABLE", Checks: "fail", Failed: 1}},
		{"statuses only", azJSON("active", "succeeded", false), "[]", `{"value":[{"id":1,"state":"succeeded","context":{"name":"a"}},{"id":2,"state":"notApplicable","context":{"name":"b"}}]}`,
			PR{State: "OPEN", Mergeable: "MERGEABLE", Checks: "pass"}},
		{"status error", azJSON("active", "succeeded", false), "", `[{"id":1,"state":"error","context":{"name":"a"}}]`,
			PR{State: "OPEN", Mergeable: "MERGEABLE", Checks: "fail", Failed: 1}},

		{"approved", azJSON("active", "succeeded", false, vote(10, true), vote(0, false)), "", "", PR{State: "OPEN", Mergeable: "MERGEABLE", Review: "APPROVED"}},
		{"approved with suggestions", azJSON("active", "succeeded", false, vote(5, false)), "", "", PR{State: "OPEN", Mergeable: "MERGEABLE", Review: "APPROVED"}},
		{"waiting for author", azJSON("active", "succeeded", false, vote(10, true), vote(-5, false)), "", "", PR{State: "OPEN", Mergeable: "MERGEABLE", Review: "CHANGES_REQUESTED"}},
		{"rejected", azJSON("active", "succeeded", false, vote(-10, false)), "", "", PR{State: "OPEN", Mergeable: "MERGEABLE", Review: "CHANGES_REQUESTED"}},
		{"required reviewer not voted", azJSON("active", "succeeded", false, vote(10, false), vote(0, true)), "", "", PR{State: "OPEN", Mergeable: "MERGEABLE", Review: "REVIEW_REQUIRED"}},
		{"declined required reviewer", azJSON("active", "succeeded", false, vote(10, false), `{"vote":0,"isRequired":true,"hasDeclined":true}`), "", "", PR{State: "OPEN", Mergeable: "MERGEABLE", Review: "APPROVED"}},
		{"no votes", azJSON("active", "succeeded", false, vote(0, false)), "", "", PR{State: "OPEN", Mergeable: "MERGEABLE"}},
		{"min reviewers not met", azJSON("active", "succeeded", false, vote(10, false)), list(policy("minreviewers", "queued", true)), "", PR{State: "OPEN", Mergeable: "MERGEABLE", Review: "REVIEW_REQUIRED"}},
		{"min reviewers met", azJSON("active", "succeeded", false, vote(10, false)), list(policy("minreviewers", "approved", true)), "", PR{State: "OPEN", Mergeable: "MERGEABLE", Review: "APPROVED"}},
		{"optional reviewers policy", azJSON("active", "succeeded", false, vote(10, false)), list(policy("reviewers", "queued", false)), "", PR{State: "OPEN", Mergeable: "MERGEABLE", Review: "APPROVED"}},
		{"required reviewers policy rejected", azJSON("active", "succeeded", false), list(policy("reviewers", "rejected", true)), "", PR{State: "OPEN", Mergeable: "MERGEABLE", Review: "REVIEW_REQUIRED"}},

		{"draft waits for no review", azJSON("active", "succeeded", true, vote(0, true)), list(policy("minreviewers", "queued", true), policy("build", "running", true)), "",
			PR{State: "OPEN", Mergeable: "MERGEABLE", Checks: "pending"}},
		{"draft with changes requested", azJSON("active", "succeeded", true, vote(-10, false)), list(policy("build", "rejected", true)), "",
			PR{State: "OPEN", Mergeable: "MERGEABLE", Review: "CHANGES_REQUESTED", Checks: "fail", Failed: 1}},
		{"draft approved", azJSON("active", "succeeded", true, vote(10, false)), "", "", PR{State: "OPEN", Mergeable: "MERGEABLE", Review: "APPROVED"}},
	} {
		got, err := azurePR(shop, []byte(c.pr), []byte(c.policies), []byte(c.statuses))
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		if got.Number != 5 || got.URL != "https://dev.azure.com/acme/Shop/_git/web/pullrequest/5" || got.Base != "main" {
			t.Errorf("%s: number, URL or base: %+v", c.name, got)
		}
		got.Number, got.URL, got.Base = 0, "", ""
		if got != c.want {
			t.Errorf("%s:\n got %+v\nwant %+v", c.name, got, c.want)
		}
	}
}

// TestAzurePRSanitising: nothing from the answer but fixed words,
// commit ids and a branch name gets through.
func TestAzurePRSanitising(t *testing.T) {
	pr, err := azurePR(Target{OrgURL: "https://dev.azure.com/acme", Project: "My Project", Repo: "web"},
		[]byte(`{"pullRequestId":3,"status":"completed","mergeStatus":"IGNORE","closedDate":"yesterday",`+
			`"lastMergeCommit":{"commitId":"--upload-pack=x"},"lastMergeSourceCommit":{"commitId":"HEAD"},"targetRefName":"refs/heads/-x main",`+
			`"repository":{"webUrl":"javascript:alert(1)"}}`), []byte(`{"not":"a list"}`), []byte(`"x"`))
	if err != nil {
		t.Fatal(err)
	}
	want := PR{Number: 3, URL: "https://dev.azure.com/acme/My%20Project/_git/web/pullrequest/3", State: "MERGED", Mergeable: "UNKNOWN"}
	if pr != want {
		t.Fatalf("got %+v", pr)
	}
	if pr, _ := azurePR(shop, []byte(`{"pullRequestId":-4}`), nil, nil); pr.Number != 0 || pr.URL != "" {
		t.Fatalf("negative id: %+v", pr)
	}
	if _, err := azurePR(shop, []byte(`[`), nil, nil); err == nil {
		t.Fatal("bad JSON took")
	}
}

func FuzzParseAzurePR(f *testing.F) {
	f.Add(fixture(f, "pr-active.json"), fixture(f, "policies.json"), fixture(f, "statuses.json"))
	f.Add(fixture(f, "pr-completed.json"), []byte(nil), []byte(nil))
	f.Add([]byte(azJSON("active", "conflicts", true, vote(-5, true))), []byte(`[`+policy("build", "rejected", false)+`]`), []byte(`[]`))
	f.Add([]byte(`{}`), []byte(`{}`), []byte(`{}`))
	f.Fuzz(func(t *testing.T, data, policies, statuses []byte) {
		pr, err := azurePR(shop, data, policies, statuses)
		if err != nil {
			return
		}
		if pr.Number < 0 || pr.URL != "" && pr.URL != AzurePRURL(shop, pr.Number) || !upperRE.MatchString(pr.State) || !upperRE.MatchString(pr.Review) ||
			!upperRE.MatchString(pr.Mergeable) || !upperRE.MatchString(pr.MergeState) || pr.Base != "" && !branchRE.MatchString(pr.Base) ||
			pr.Merge != "" && !oidRE.MatchString(pr.Merge) || pr.Head != "" && !oidRE.MatchString(pr.Head) ||
			pr.Failed < 0 || pr.Checks != "" && pr.Checks != "pass" && pr.Checks != "fail" && pr.Checks != "pending" {
			t.Fatalf("unchecked field: %+v", pr)
		}
		if n, ok := (Azure{Target: shop}).ParsePRURL(pr.URL); pr.URL != "" && (!ok || n != pr.Number) {
			t.Fatalf("URL %q doesn't parse back", pr.URL)
		}
	})
}

func TestPickAzurePR(t *testing.T) {
	id := func(raw []byte) string {
		var p azPR
		if raw == nil || errorsJSON(raw, &p) {
			return "none"
		}
		return fmt.Sprint(p.ID)
	}
	for _, c := range [][2]string{
		{string(fixture(t, "pr-list.json")), "6"}, // PRs 1-6, 1, 2, 3 and 6 active
		{string(fixture(t, "pr-list-branch.json")), "1"},
		{`[{"pullRequestId":3,"status":"completed"},{"pullRequestId":9,"status":"abandoned"}]`, "9"},
		{`[{"pullRequestId":4,"status":"active"},{"pullRequestId":8,"status":"active"},{"pullRequestId":20,"status":"abandoned"}]`, "8"},
		{`[]`, "none"}, {`{}`, "none"}, {`[{"pullRequestId":0}]`, "none"}, {``, "none"},
	} {
		if got := id(pickAzurePR([]byte(c[0]))); got != c[1] {
			t.Errorf("%.60s: got %s want %s", c[0], got, c[1])
		}
	}
}

// azRoute names the REST route of an az rest command's URL, for the
// fakes: "pr" (one PR), "prs" (the list), "policies", "statuses",
// "builds", "timeline", "logs" (their list), "log" (one log's lines).
func azRoute(args []string) (route, u string) {
	if len(args) == 0 || args[0] != "rest" {
		return strings.Join(args, " "), ""
	}
	for i, a := range args {
		if a == "--url" && i+1 < len(args) {
			u = args[i+1]
		}
	}
	path, _, _ := strings.Cut(u, "?")
	switch {
	case strings.Contains(path, "/policy/evaluations"):
		return "policies", u
	case strings.HasSuffix(path, "/statuses"):
		return "statuses", u
	case strings.HasSuffix(path, "/pullrequests"):
		return "prs", u
	case strings.Contains(path, "/pullrequests/"):
		return "pr", u
	case strings.HasSuffix(path, "/timeline"):
		return "timeline", u
	case strings.HasSuffix(path, "/logs"):
		return "logs", u
	case strings.Contains(path, "/logs/"):
		return "log", u
	case strings.HasSuffix(path, "/_apis/build/builds"):
		return "builds", u
	}
	return "rest " + path, u
}

// azFake is an Azure host whose az answers from a table by REST route
// (azRoute), and records the URLs it was asked.
type azFake struct {
	answers map[string]string // "pr" => JSON; "!" prefix => error text
	mu      sync.Mutex        // the calls for one PR run at once
	asked   []string
}

func (f *azFake) host() Azure {
	return Azure{Target: shop, Getenv: func(string) string { return "" }, Run: func(dir string, args ...string) ([]byte, error) {
		route, u := azRoute(args)
		f.mu.Lock()
		defer f.mu.Unlock()
		f.asked = append(f.asked, u)
		if a, ok := f.answers[route]; ok {
			if e, bad := strings.CutPrefix(a, "!"); bad {
				return nil, azError(errors.New("az " + strings.Join(args, " ") + ": exit status 1 " + e))
			}
			return []byte(a), nil
		}
		return nil, azError(fmt.Errorf("az %s: exit status 2 unexpected", strings.Join(args, " ")))
	}}
}

const (
	shopRepo = "https://dev.azure.com/acme/Shop/_apis/git/repositories/web"
	shopPID  = "6ce954b1-ce1f-45d1-b94d-e6bf2464ba2c"
)

// TestAzurePRLookup: by number, by URL of this repo only, by branch;
// every URL is built from the Target, through az rest with az login's
// token for Azure DevOps.
func TestAzurePRLookup(t *testing.T) {
	f := &azFake{answers: map[string]string{
		"pr":       string(fixture(t, "pr-active.json")),
		"prs":      string(fixture(t, "pr-list-branch.json")),
		"policies": string(fixture(t, "policies.json")),
		"statuses": string(fixture(t, "statuses.json")),
	}}
	var args []string
	a := f.host()
	run := a.Run
	a.Run = func(dir string, as ...string) ([]byte, error) {
		f.mu.Lock()
		args = as
		f.mu.Unlock()
		return run(dir, as...)
	}
	pr, err := a.PR("/r", Ref{Branch: "feature/pass"})
	if err != nil || pr.Number != 1 || pr.Checks != "fail" {
		t.Fatalf("by branch: %+v %v", pr, err)
	}
	if len(f.asked) != 3 || f.asked[0] != shopRepo+"/pullrequests?searchCriteria.sourceRefName=refs%2Fheads%2Ffeature%2Fpass&searchCriteria.status=all&$top=20&api-version=7.1" {
		t.Fatalf("asked %q", f.asked)
	}
	for _, c := range f.asked[1:] {
		switch c {
		case "https://dev.azure.com/acme/Shop/_apis/policy/evaluations?artifactId=vstfs%3A%2F%2F%2FCodeReview%2FCodeReviewId%2F" + shopPID + "%2F1&api-version=7.1-preview.1":
		case shopRepo + "/pullRequests/1/statuses?api-version=7.1-preview.1":
		default:
			t.Fatalf("asked %q", c)
		}
	}
	if strings.Join(args[:5], " ") != "rest --method get --resource "+azResource || !slices.Contains(args, "Accept=application/json") {
		t.Fatalf("args %q", args)
	}

	f.asked = nil
	if pr, err := a.PR("/r", Ref{URL: "https://dev.azure.com/acme/Shop/_git/web/pullrequest/1", Branch: "other"}); err != nil || pr.Number != 1 {
		t.Fatalf("by URL: %+v %v", pr, err)
	}
	if f.asked[0] != shopRepo+"/pullrequests/1?api-version=7.1" {
		t.Fatalf("by URL asked %q", f.asked)
	}
	// Another repo's PR URL isn't followed: the branch is.
	f.asked = nil
	a.PR("/r", Ref{URL: "https://dev.azure.com/evil/Shop/_git/web/pullrequest/1", Branch: "feature/pass"})
	if !strings.HasPrefix(f.asked[0], shopRepo+"/pullrequests?") {
		t.Fatalf("other org's URL followed: %q", f.asked)
	}
	// A number of another repo of the organization is no PR of ours.
	f.answers["pr"] = strings.Replace(string(fixture(t, "pr-active.json")), `"name": "web"`, `"name": "api"`, 1)
	if _, err := a.PR("/r", Ref{Number: 1}); !errors.Is(err, ErrNoPR) {
		t.Fatalf("other repo's PR: %v", err)
	}
	// A PR without its project's id asks no policies.
	f.answers["pr"] = strings.ReplaceAll(string(fixture(t, "pr-active.json")), shopPID, "not-a-guid")
	f.asked = nil
	if _, err := a.PR("/r", Ref{Number: 1}); err != nil || len(f.asked) != 2 {
		t.Fatalf("no project id: %v %q", err, f.asked)
	}

	// A merged PR asks no policies.
	f.answers["pr"] = string(fixture(t, "pr-completed.json"))
	f.asked = nil
	if pr, err := a.PR("/r", Ref{Number: 5}); err != nil || pr.State != "MERGED" || len(f.asked) != 1 {
		t.Fatalf("merged: %+v %v %q", pr, err, f.asked)
	}
	if st, n, head := a.PRHead("/r", "https://dev.azure.com/acme/Shop/_git/web/pullrequest/5"); st != "MERGED" || n != 5 || head != "feature/merge" {
		t.Fatalf("PRHead: %s %d %s", st, n, head)
	}
	if st, _, _ := a.PRHead("/r", "https://dev.azure.com/acme/Shop/_git/api/pullrequest/5"); st != "" {
		t.Fatal("PRHead of another repo")
	}
	if st := a.PRState("/r", "feature/pass"); st != "OPEN" {
		t.Fatalf("PRState %q", st)
	}
}

// TestAzurePRErrors: the error contract the ticker's host-failing count
// rests on, as for GitHub.
func TestAzurePRErrors(t *testing.T) {
	f := &azFake{answers: map[string]string{"prs": `{"count":0,"value":[]}`}}
	a := f.host()
	for _, ref := range []Ref{{}, {Branch: "-x"}, {Branch: "a b"}, {URL: "https://github.com/o/r/pull/1"}} {
		if _, err := a.PR("/r", ref); !errors.Is(err, ErrNoRef) {
			t.Fatalf("%+v: %v", ref, err)
		}
	}
	if len(f.asked) != 0 {
		t.Fatalf("ran %q", f.asked)
	}
	if _, err := a.PR("/r", Ref{Branch: "b"}); !errors.Is(err, ErrNoPR) {
		t.Fatalf("empty list: %v", err)
	}
	f.answers["pr"] = `!ERROR: Not Found({"$id":"1","innerException":null,"message":"TF401180: The requested pull request was not found.","typeName":"Microsoft.TeamFoundation.Git.Server.GitPullRequestNotFoundException, Microsoft.TeamFoundation.Git.Server"})`
	if _, err := a.PR("/r", Ref{Number: 4}); !errors.Is(err, ErrNoPR) {
		t.Fatalf("not found: %v", err)
	}
	f.answers["prs"] = "!ERROR: Please run 'az login' to setup account."
	_, err := a.PR("/r", Ref{Branch: "b"})
	var ce *CLIError
	if errors.Is(err, ErrNoPR) || !errors.As(err, &ce) || ce.Problem != "az is not logged in" || ce.CLI != "az" {
		t.Fatalf("logged out: %v", err)
	}
	// policies failing fails the poll; statuses failing doesn't
	f.answers["prs"] = string(fixture(t, "pr-list-branch.json"))
	f.answers["policies"] = `!ERROR: Forbidden({"message":"TF400409: You do not have permissions."})`
	f.answers["statuses"] = "[]"
	if _, err := a.PR("/r", Ref{Branch: "b"}); !errors.As(err, &ce) || !strings.Contains(ce.Problem, "401/403") {
		t.Fatalf("policies: %v", err)
	}
	f.answers["policies"] = "[]"
	f.answers["statuses"] = "!boom"
	if pr, err := a.PR("/r", Ref{Branch: "b"}); err != nil || pr.Number != 1 {
		t.Fatalf("statuses: %+v %v", pr, err)
	}
	if _, err := (Azure{Target: Target{Kind: AzureKind}}).PR("/r", Ref{Branch: "b"}); err == nil || errors.Is(err, ErrNoPR) {
		t.Fatalf("no target: %v", err)
	}
}

func TestAzError(t *testing.T) {
	for msg, want := range map[string]string{
		"ERROR: Please run 'az login' to setup account.":                        "az is not logged in",
		"ERROR: AADSTS700082: The refresh token has expired due to inactivity.": "az is not logged in",
		"ERROR: Before you can run Azure DevOps commands, you need to run the login command(az login if using AAD/MSA identity else az devops login if using PAT token) to setup credentials.": "az is not logged in",
		"ERROR: TF400813: The user '' is not authorized to access this resource.":                                                                                                              "Azure DevOps doesn't know the user az signs in as (TF400813)",
		"ERROR: Operation returned a 403 status code.":                                                                                                                                         "Azure DevOps refused access (401/403)",
		"ERROR: TF200016: The following project does not exist: Shop.":                                                                                                                         "Azure DevOps doesn't know this organization, project or repo",
		"ERROR: something else": "",
	} {
		var ce *CLIError
		if err := azError(errors.New(msg)); !errors.As(err, &ce) || ce.Problem != want || (want == "") != (ce.Advice == "") {
			t.Errorf("%q: %+v", msg, ce)
		}
	}
	err := azError(fmt.Errorf("az: %w", exec.ErrNotFound))
	if !errors.Is(err, exec.ErrNotFound) {
		t.Fatalf("not found lost: %v", err)
	}
}

func TestAzureParsePRURL(t *testing.T) {
	a := Azure{Target: shop}
	for s, want := range map[string]int{
		"https://dev.azure.com/acme/Shop/_git/web/pullrequest/12":  12,
		"https://dev.azure.com/acme/shop/_git/WEB/pullrequest/12":  12,
		"https://dev.azure.com/acme/Shop/_git/web/pullrequest/12/": 0,
		"https://dev.azure.com/acme/Shop/_git/web/pullrequest/0":   0,
		"https://dev.azure.com/acme/Shop/_git/api/pullrequest/12":  0,
		"https://dev.azure.com/evil/Shop/_git/web/pullrequest/12":  0,
		"https://dev.azure.com/acme/Shop/_git/web/pullrequest/x":   0,
		"https://github.com/o/r/pull/12":                           0,
		"":                                                         0,
	} {
		if n, ok := a.ParsePRURL(s); n != want || ok != (want > 0) {
			t.Errorf("%q: %d %v", s, n, ok)
		}
	}
	vs := Azure{Target: Target{Kind: AzureKind, OrgURL: "https://acme.visualstudio.com", Project: "My Project", Repo: "web"}}
	for _, s := range []string{"https://acme.visualstudio.com/My%20Project/_git/web/pullrequest/7", "https://acme.visualstudio.com/DefaultCollection/My%20Project/_git/web/pullrequest/7"} {
		if n, ok := vs.ParsePRURL(s); !ok || n != 7 {
			t.Errorf("%q: %d %v", s, n, ok)
		}
	}
	if n, ok := vs.ParsePRURL(AzurePRURL(vs.Target, 9)); !ok || n != 9 {
		t.Fatal("AzurePRURL doesn't parse back")
	}
}

func TestAzureHints(t *testing.T) {
	h := Azure{Target: Target{OrgURL: "https://dev.azure.com/acme", Project: "My Project", Repo: "web"}}.Hints(4)
	if h.Checks != "az rest --resource "+azResource+" --url 'https://dev.azure.com/acme/My%20Project/_apis/build/builds?branchName=refs/pull/4/merge&api-version=7.1' --query 'value[].{build:id,status:status,result:result}' -o table" ||
		h.Review != "az rest --resource "+azResource+" --url 'https://dev.azure.com/acme/My%20Project/_apis/git/repositories/web/pullRequests/4/threads?api-version=7.1' --query \"value[].comments[?commentType=='text'][].{author:author.displayName,text:content}\" -o table" {
		t.Fatalf("%+v", h)
	}
	h = Azure{Target: Target{OrgURL: "https://dev.azure.com/acme", Project: "Shop; rm -rf ~", Repo: "web"}}.Hints(4)
	if strings.Contains(h.Checks+h.Review, "rm") || h.Checks != "the checks on PR 4's page" {
		t.Fatalf("%+v", h)
	}
}

func TestAzureDoctor(t *testing.T) {
	deps := func(fail string, pat bool) DoctorDeps {
		return DoctorDeps{
			LookPath: func(string) (string, error) { return "/bin/az", nil },
			Run: func(dir, name string, args ...string) (string, error) {
				if args[0] == fail {
					return "", errors.New("exit 1")
				}
				return "", nil
			},
			Getenv: func(k string) string {
				if pat && k == patEnv {
					return "s3cret"
				}
				return ""
			},
		}
	}
	checks := Azure{Target: shop}.Doctor(deps("", false))
	if len(checks) != 3 || !checks[0].OK || !checks[1].OK || !checks[2].OK || checks[1].Name != "az login" {
		t.Fatalf("%+v", checks)
	}
	// the extension is optional
	if checks := (Azure{}).Doctor(deps("extension", false)); !checks[2].OK || !strings.Contains(checks[2].Detail, "optional") {
		t.Fatalf("%+v", checks)
	}
	if checks := (Azure{}).Doctor(deps("account", false)); checks[1].OK || !strings.Contains(checks[1].Detail, "az login") {
		t.Fatalf("%+v", checks)
	}
	if checks := (Azure{}).Doctor(deps("account", true)); !checks[1].OK || checks[1].Detail != patEnv+" is set" {
		t.Fatalf("%+v", checks)
	}
	noAZ := func(pat bool) DoctorDeps {
		d := deps("", pat)
		d.LookPath = func(string) (string, error) { return "", exec.ErrNotFound }
		return d
	}
	if checks := (Azure{}).Doctor(noAZ(false)); len(checks) != 1 || checks[0].OK {
		t.Fatalf("%+v", checks)
	}
	if checks := (Azure{}).Doctor(noAZ(true)); len(checks) != 1 || !checks[0].OK || strings.Contains(checks[0].Detail, "s3cret") {
		t.Fatalf("%+v", checks)
	}
}

// TestAzureAccess: a readable repo; az login's twin under the
// extension (the TF400813 of az rest with X-VSS-ForceMsaPassThrough,
// captured from a live organization); refusals with the tenant to log
// in to; the PAT; a wrong name.
func TestAzureAccess(t *testing.T) {
	const tf400813 = `ERROR: Unauthorized({"$id":"1","innerException":null,"message":"TF400813: The user '39b70f09-0000-0000-0000-000000000000' is not authorized to access this resource.","typeName":"Microsoft.TeamFoundation.Framework.Server.UnauthorizedRequestException"})`
	type answer struct {
		out string
		err string
	}
	deps := func(answers map[string]answer, ran *[]string) DoctorDeps {
		return DoctorDeps{
			LookPath: func(string) (string, error) { return "/bin/az", nil },
			Getenv:   func(string) string { return "" },
			Run: func(dir, name string, args ...string) (string, error) {
				cmd := strings.Join(args, " ")
				*ran = append(*ran, cmd)
				key := args[0]
				switch {
				case args[0] == "rest" && strings.Contains(cmd, "X-VSS-ForceMsaPassThrough=true"):
					key = "msa"
				case args[0] == "rest" && strings.Contains(cmd, "--subscription"):
					key = "rest-sub"
				}
				a := answers[key]
				if a.err != "" {
					return a.err, errors.New("/bin/az " + cmd + ": " + a.err)
				}
				return a.out, nil
			},
		}
	}
	var ran []string
	ok := map[string]answer{"rest": {out: `{"id":"x","name":"web"}`}, "extension": {}, "msa": {}}
	if c := (Azure{Target: shop}).Access(deps(ok, &ran)); len(c) != 1 || !c[0].OK || c[0].Name != "az repo Shop/web" {
		t.Fatalf("%+v", c)
	}
	if !strings.HasPrefix(ran[0], "rest --method get --resource "+azResource+" --url "+shopRepo+"?api-version=7.1 ") {
		t.Fatalf("ran %q", ran)
	}
	// the extension's twin
	ok["msa"] = answer{err: tf400813}
	if c := (Azure{Target: shop}).Access(deps(ok, &ran)); len(c) != 2 || !c[0].OK || c[1].OK || !strings.Contains(c[1].Detail, "personal Microsoft account") ||
		!strings.Contains(c[1].Detail, "az devops login --organization "+shop.OrgURL) {
		t.Fatalf("%+v", c)
	}
	// no extension: no probe
	ok["extension"] = answer{err: "ERROR: extension not installed"}
	if c := (Azure{Target: shop}).Access(deps(ok, &ran)); len(c) != 1 || !c[0].OK {
		t.Fatalf("%+v", c)
	}

	// refused, the organization in a tenant az has no account in
	defer func(f func(string) string) { orgTenant = f }(orgTenant)
	orgTenant = func(string) string { return "47388324-0000-0000-0000-000000000000" }
	azSubs.Delete(shop.OrgURL)
	refused := map[string]answer{"rest": {err: tf400813}, "account": {out: `[{"id":"11111111-0000-0000-0000-000000000000","tenantId":"99999999-0000-0000-0000-000000000000","isDefault":true}]`}}
	if c := (Azure{Target: shop}).Access(deps(refused, &ran)); len(c) != 1 || c[0].OK || !strings.Contains(c[0].Detail, "TF400813") ||
		!strings.Contains(c[0].Detail, "az login --tenant 47388324-0000-0000-0000-000000000000") {
		t.Fatalf("%+v", c)
	}
	// az has a subscription in it, not the default: retried with it
	azSubs.Delete(shop.OrgURL)
	refused["account"] = answer{out: `[{"id":"11111111-0000-0000-0000-000000000000","tenantId":"99999999-0000-0000-0000-000000000000","isDefault":true},` +
		`{"id":"22222222-0000-0000-0000-000000000000","tenantId":"47388324-0000-0000-0000-000000000000","isDefault":false}]`}
	refused["rest-sub"] = answer{out: `{"id":"x"}`}
	ran = nil
	if c := (Azure{Target: shop}).Access(deps(refused, &ran)); len(c) != 1 || !c[0].OK || !strings.Contains(strings.Join(ran, "\n"), "--subscription 22222222-0000-0000-0000-000000000000") {
		t.Fatalf("%+v %q", c, ran)
	}
	azSubs.Delete(shop.OrgURL)

	// not logged in, a PAT set: asked with it
	d := deps(map[string]answer{"rest": {err: "ERROR: Please run 'az login' to setup account."}}, &ran)
	d.Getenv = func(k string) string { return map[string]string{patEnv: "s3cret"}[k] }
	var patURL string
	a := Azure{Target: shop, PATGet: func(u, pat string) ([]byte, error) { patURL = u; return []byte(`{}`), nil }}
	if c := a.Access(d); len(c) != 1 || !c[0].OK || patURL != shopRepo+"?api-version=7.1" {
		t.Fatalf("%+v %q", c, patURL)
	}
	a.PATGet = func(u, pat string) ([]byte, error) {
		return nil, &CLIError{CLI: "az", Problem: "Azure DevOps refused " + patEnv + " (401/403)", Err: errors.New("401"), auth: true}
	}
	if c := a.Access(d); len(c) != 1 || c[0].OK || !strings.Contains(c[0].Detail, "hasn't expired") || strings.Contains(c[0].Detail, "s3cret") {
		t.Fatalf("%+v", c)
	}
	// not logged in, no PAT
	if c := (Azure{Target: shop}).Access(deps(map[string]answer{"rest": {err: "ERROR: Please run 'az login' to setup account."}}, &ran)); c[0].OK || !strings.Contains(c[0].Detail, "run az login") {
		t.Fatalf("%+v", c)
	}
	// a repo Azure DevOps doesn't know
	nf := `ERROR: Not Found({"message":"TF401019: The Git repository with name or identifier web does not exist or you do not have permissions for the operation you are attempting."})`
	if c := (Azure{Target: shop}).Access(deps(map[string]answer{"rest": {err: nf}}, &ran)); c[0].OK || !strings.Contains(c[0].Detail, "origin URL") {
		t.Fatalf("%+v", c)
	}
}

func errorsJSON(raw []byte, v any) bool { return json.Unmarshal(raw, v) != nil }

func TestAdvice(t *testing.T) {
	for _, c := range []struct {
		err                  error
		cli, problem, advice string
	}{
		{errors.New("gh pr view: exit status 1"), "gh", "", "the user checks gh auth status in a terminal (tm doctor)"},
		{nil, "gh", "", "the user checks gh auth status in a terminal (tm doctor)"},
		{azError(errors.New("az: Please run 'az login'")), "az", "az is not logged in", "the user runs az login in a terminal"},
		{fmt.Errorf("poll: %w", azError(errors.New("az: boom"))), "az", "", "the user checks az login in a terminal (tm doctor)"},
		{&CLIError{CLI: "x; rm", Problem: "p", Advice: "a", Err: errors.New("e")}, "the code host CLI", "", "the user checks the code host CLI auth status in a terminal (tm doctor)"},
	} {
		if cli, problem, advice := Advice(c.err); cli != c.cli || problem != c.problem || advice != c.advice {
			t.Errorf("%v: %q %q %q", c.err, cli, problem, advice)
		}
	}
}

// buildLogHost answers az rest for FailedLog: the merge ref's failed
// builds, the timeline, the log list and the log lines, by route.
func buildLogHost(t *testing.T, builds string, asked *[]string) Azure {
	return Azure{Target: shop, Getenv: func(string) string { return "" }, Run: func(dir string, args ...string) ([]byte, error) {
		route, u := azRoute(args)
		*asked = append(*asked, u)
		switch route {
		case "builds":
			if builds == "" {
				return nil, azError(errors.New("az: exit status 1 boom"))
			}
			return []byte(builds), nil
		case "timeline":
			return fixture(t, "timeline.json"), nil
		case "logs":
			return []byte(`{"count":2,"value":[{"id":7,"lineCount":5000},{"id":8,"lineCount":3}]}`), nil
		case "log":
			b, _ := json.Marshal(strings.Split(strings.TrimRight(string(fixture(t, "buildlog.txt")), "\n"), "\n"))
			return []byte(`{"count":10,"value":` + string(b) + `}`), nil
		}
		return nil, errors.New("unexpected " + strings.Join(args, " "))
	}}
}

func TestAzureFailedLog(t *testing.T) {
	var asked []string
	a := buildLogHost(t, string(fixture(t, "builds.json")), &asked)
	job, text := a.FailedLog("/repo", PR{Number: 12})
	if job != "Build / Check" {
		t.Fatalf("job %q", job)
	}
	for _, bad := range []string{"##[section]", "##[group]", "2026-10-07", "\x1b"} {
		if strings.Contains(text, bad) {
			t.Errorf("%q left in %q", bad, text)
		}
	}
	if !strings.Contains(text, "checking for FAIL") || !strings.Contains(text, "##[error]FAIL file present: deliberate failure") || !strings.HasSuffix(text, "##[error]Bash exited with code '1'.") {
		t.Errorf("excerpt %q", text)
	}
	// the merge ref's failed builds; the log's last 2000 lines
	b := "https://dev.azure.com/acme/Shop/_apis/build/builds"
	want := []string{
		b + "?branchName=refs/pull/12/merge&resultFilter=failed&queryOrder=queueTimeDescending&$top=" + fmt.Sprint(logRuns) + "&api-version=7.1",
		b + "/6/timeline?api-version=7.1",
		b + "/6/logs?api-version=7.1",
		b + "/6/logs/7?startLine=3001&endLine=5000&api-version=7.1",
	}
	if !slices.Equal(asked, want) {
		t.Errorf("asked:\n%s", strings.Join(asked, "\n"))
	}

	// nothing to find, or az failing: the fixed prompt stays
	a = buildLogHost(t, "", &asked)
	if job, text = a.FailedLog("/repo", PR{Number: 12}); job != "" || text != "" {
		t.Errorf("failure gave %q %q", job, text)
	}
	if job, text = a.FailedLog("/repo", PR{}); job != "" || text != "" {
		t.Errorf("no PR gave %q %q", job, text)
	}
	a = buildLogHost(t, `{"count":0,"value":[]}`, &asked)
	if job, text = a.FailedLog("/repo", PR{Number: 12}); job != "" || text != "" {
		t.Errorf("no builds gave %q %q", job, text)
	}
}

func TestAzureLogShapes(t *testing.T) {
	for in, want := range map[string]string{
		`{"count":2,"value":["a","b"]}`: "a\nb",
		`"a\nb"`:                        "a\nb",
		"a\nb\n":                        "a\nb\n",
	} {
		if got := azLogText([]byte(in)); got != want {
			t.Errorf("%s: %q", in, got)
		}
	}
	if n, id := firstFailedTask([]byte(`{"records":[{"type":"Job","result":"failed","log":{"id":2}}]}`)); n != "" || id != 0 {
		t.Errorf("job rollup picked: %q %d", n, id)
	}
	if n, id := firstFailedTask([]byte(`nope`)); n != "" || id != 0 {
		t.Errorf("garbage picked: %q %d", n, id)
	}
	// a long log keeps from the lead before the first error, capped and fenced
	long := "2026-10-07T10:00:00.1234567Z error: ```x\n" + strings.Repeat("2026-10-07T10:00:00.1234567Z line\n", 600)
	if _, text := azureExcerpt("Run", long); len(text) > logBytes+20 || strings.Contains(text, "```") || !strings.HasSuffix(text, "[... cut]") {
		t.Errorf("cap: %d %q", len(text), text[:40])
	}
}
