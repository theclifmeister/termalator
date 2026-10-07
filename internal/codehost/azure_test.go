package codehost

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

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

// TestAzurePRFixtures: az's answers as printed, onto the fixed fields.
func TestAzurePRFixtures(t *testing.T) {
	pr, err := azurePR(shop, fixture(t, "pr-active.json"), fixture(t, "policies.json"), fixture(t, "statuses.json"))
	if err != nil {
		t.Fatal(err)
	}
	want := PR{
		Number: 12, URL: "https://dev.azure.com/acme/Shop/_git/web/pullrequest/12", State: "OPEN",
		// the blocking build rejected; lint's later status succeeded,
		// scan's failure is the approved status policy's, e2e pending;
		// the optional build's queue and the comment policy don't count
		Checks: "fail", Failed: 1,
		Review: "REVIEW_REQUIRED",
		Head:   "0123456789abcdef0123456789abcdef01234567", Base: "main", Mergeable: "MERGEABLE",
	}
	if pr != want {
		t.Fatalf("active:\n got %+v\nwant %+v", pr, want)
	}
	if pr.Summary() != "#12 open, 1 check failed, review required" {
		t.Fatalf("summary %q", pr.Summary())
	}

	pr, err = azurePR(shop, fixture(t, "pr-completed.json"), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	want = PR{
		Number: 12, URL: "https://dev.azure.com/acme/Shop/_git/web/pullrequest/12", State: "MERGED", Review: "APPROVED",
		MergedAt: time.Date(2026, 10, 7, 11, 40, 2, 716023000, time.UTC),
		Head:     "0123456789abcdef0123456789abcdef01234567", Merge: "9a8b7c6d5e4f30211203f4e5d6c7b8a990a1b2c3",
		Base: "main", Mergeable: "MERGEABLE",
	}
	if pr != want {
		t.Fatalf("completed:\n got %+v\nwant %+v", pr, want)
	}
}

// azJSON is a GitPullRequest of shop with the given fields.
func azJSON(status, mergeStatus string, draft bool, votes ...string) string {
	return fmt.Sprintf(`{"pullRequestId":5,"status":%q,"mergeStatus":%q,"isDraft":%v,"closedDate":"2026-10-07T10:00:00.1234567Z",`+
		`"lastMergeCommit":{"commitId":"%s"},"targetRefName":"refs/heads/main","repository":{"name":"web","project":{"name":"Shop"}},"reviewers":[%s]}`,
		status, mergeStatus, draft, strings.Repeat("ab", 20), strings.Join(votes, ","))
}

// policy is one evaluation record: kind build, status, minreviewers,
// comments.
func policy(kind, status string, blocking bool) string {
	ids := map[string]string{"build": policyBuild, "status": policyStatus, "minreviewers": policyMinReviewers, "reviewers": policyReviewers, "comments": "c6a1889d-b943-4856-b76f-9e46bb6b0df2"}
	return fmt.Sprintf(`{"status":%q,"configuration":{"isBlocking":%v,"isEnabled":true,"type":{"id":%q}}}`, status, blocking, ids[kind])
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
	for list, want := range map[string]string{
		string(fixture(t, "pr-list.json")): "12",
		`[{"pullRequestId":3,"status":"completed"},{"pullRequestId":9,"status":"abandoned"}]`:                                     "9",
		`[{"pullRequestId":4,"status":"active"},{"pullRequestId":8,"status":"active"},{"pullRequestId":20,"status":"abandoned"}]`: "8",
		`[]`: "none", `{}`: "none", `[{"pullRequestId":0}]`: "none", ``: "none",
	} {
		if got := id(pickAzurePR([]byte(list))); got != want {
			t.Errorf("%.60s: got %s want %s", list, got, want)
		}
	}
}

// azFake is an Azure host whose az answers from a table by the
// command's first words, and records what it was asked.
type azFake struct {
	answers map[string]string // "repos pr show" => JSON; "!" prefix => error text
	mu      sync.Mutex        // az's calls for one PR run at once
	asked   []string
}

func (f *azFake) host() Azure {
	return Azure{Target: shop, Run: func(dir string, args ...string) ([]byte, error) {
		cmd := strings.Join(args, " ")
		f.mu.Lock()
		defer f.mu.Unlock()
		f.asked = append(f.asked, cmd)
		for _, words := range []int{4, 3} {
			if len(args) < words {
				continue
			}
			if a, ok := f.answers[strings.Join(args[:words], " ")]; ok {
				if e, bad := strings.CutPrefix(a, "!"); bad {
					return nil, azError(errors.New("az " + cmd + ": exit status 1 " + e))
				}
				return []byte(a), nil
			}
		}
		return nil, azError(fmt.Errorf("az %s: exit status 2 unexpected", cmd))
	}}
}

// TestAzurePRLookup: by number, by URL of this repo only, by branch;
// every call names the organization, list names the project and repo.
func TestAzurePRLookup(t *testing.T) {
	f := &azFake{answers: map[string]string{
		"repos pr show":        string(fixture(t, "pr-active.json")),
		"repos pr list":        string(fixture(t, "pr-list.json")),
		"repos pr policy list": string(fixture(t, "policies.json")),
		"devops invoke":        string(fixture(t, "statuses.json")),
	}}
	a := f.host()
	pr, err := a.PR("/r", Ref{Branch: "feature/login"})
	if err != nil || pr.Number != 12 || pr.Checks != "fail" {
		t.Fatalf("by branch: %+v %v", pr, err)
	}
	if len(f.asked) != 3 || f.asked[0] != "repos pr list --project Shop --repository web --source-branch refs/heads/feature/login --status all --top 20 --organization https://dev.azure.com/acme" {
		t.Fatalf("asked %q", f.asked)
	}
	for _, c := range f.asked[1:] {
		switch {
		case c == "repos pr policy list --id 12 --organization https://dev.azure.com/acme":
		case c == "devops invoke --area git --resource pullRequestStatuses --route-parameters project=Shop repositoryId=web pullRequestId=12 --api-version 7.1-preview.1 --http-method GET --organization https://dev.azure.com/acme":
		default:
			t.Fatalf("asked %q", c)
		}
	}

	f.asked = nil
	if pr, err := a.PR("/r", Ref{URL: "https://dev.azure.com/acme/Shop/_git/web/pullrequest/12", Branch: "other"}); err != nil || pr.Number != 12 {
		t.Fatalf("by URL: %+v %v", pr, err)
	}
	if f.asked[0] != "repos pr show --id 12 --organization https://dev.azure.com/acme" {
		t.Fatalf("by URL asked %q", f.asked)
	}
	// Another repo's PR URL isn't followed: the branch is.
	f.asked = nil
	a.PR("/r", Ref{URL: "https://dev.azure.com/evil/Shop/_git/web/pullrequest/12", Branch: "feature/login"})
	if !strings.HasPrefix(f.asked[0], "repos pr list ") {
		t.Fatalf("other org's URL followed: %q", f.asked)
	}
	// A number of another repo of the organization is no PR of ours.
	f.answers["repos pr show"] = strings.Replace(string(fixture(t, "pr-active.json")), `"name": "web"`, `"name": "api"`, 1)
	if _, err := a.PR("/r", Ref{Number: 12}); !errors.Is(err, ErrNoPR) {
		t.Fatalf("other repo's PR: %v", err)
	}

	// A merged PR asks no policies.
	f.answers["repos pr show"] = string(fixture(t, "pr-completed.json"))
	f.asked = nil
	if pr, err := a.PR("/r", Ref{Number: 12}); err != nil || pr.State != "MERGED" || len(f.asked) != 1 {
		t.Fatalf("merged: %+v %v %q", pr, err, f.asked)
	}
	if st, n, head := a.PRHead("/r", "https://dev.azure.com/acme/Shop/_git/web/pullrequest/12"); st != "MERGED" || n != 12 || head != "feature/login" {
		t.Fatalf("PRHead: %s %d %s", st, n, head)
	}
	if st, _, _ := a.PRHead("/r", "https://dev.azure.com/acme/Shop/_git/api/pullrequest/12"); st != "" {
		t.Fatal("PRHead of another repo")
	}
	if st := a.PRState("/r", "feature/login"); st != "OPEN" {
		t.Fatalf("PRState %q", st)
	}
}

// TestAzurePRErrors: the error contract the ticker's host-failing count
// rests on, as for GitHub.
func TestAzurePRErrors(t *testing.T) {
	f := &azFake{answers: map[string]string{"repos pr list": "[]"}}
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
	f.answers["repos pr show"] = "!ERROR: TF401180: The requested pull request was not found."
	if _, err := a.PR("/r", Ref{Number: 4}); !errors.Is(err, ErrNoPR) {
		t.Fatalf("not found: %v", err)
	}
	f.answers["repos pr list"] = "!ERROR: Please run 'az login' to setup account."
	_, err := a.PR("/r", Ref{Branch: "b"})
	var ce *CLIError
	if errors.Is(err, ErrNoPR) || !errors.As(err, &ce) || ce.Problem != "az is not logged in" || ce.CLI != "az" {
		t.Fatalf("logged out: %v", err)
	}
	// policies failing fails the poll; statuses failing doesn't
	f.answers["repos pr list"] = string(fixture(t, "pr-list.json"))
	f.answers["repos pr policy list"] = "!ERROR: TF400813: The user 'x' is not authorized to access this resource."
	f.answers["devops invoke"] = "[]"
	if _, err := a.PR("/r", Ref{Branch: "b"}); !errors.As(err, &ce) || !strings.Contains(ce.Problem, "401/403") {
		t.Fatalf("policies: %v", err)
	}
	f.answers["repos pr policy list"] = "[]"
	f.answers["devops invoke"] = "!boom"
	if pr, err := a.PR("/r", Ref{Branch: "b"}); err != nil || pr.Number != 12 {
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
		"ERROR: 'repos' is misspelled or not recognized by the system.":           "az lacks the azure-devops extension",
		"ERROR: TF400813: The user '' is not authorized to access this resource.": "Azure DevOps refused access (401/403)",
		"ERROR: Operation returned a 403 status code.":                            "Azure DevOps refused access (401/403)",
		"ERROR: TF200016: The following project does not exist: Shop.":            "Azure DevOps doesn't know this organization, project or repo",
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
		"https://dev.azure.com/acme/shop/_git/WEB/pullrequest/12/": 12,
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
	vs := Azure{Target: Target{OrgURL: "https://acme.visualstudio.com", Project: "My Project", Repo: "web"}}
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
	if h.Checks != "az repos pr policy list --id 4 --organization https://dev.azure.com/acme -o table" ||
		h.Review != "az devops invoke --area git --resource pullRequestThreads --route-parameters project='My Project' repositoryId=web pullRequestId=4 --organization https://dev.azure.com/acme" {
		t.Fatalf("%+v", h)
	}
	h = Azure{Target: Target{OrgURL: "https://dev.azure.com/acme", Project: "Shop; rm -rf ~", Repo: "web"}}.Hints(4)
	if strings.Contains(h.Checks+h.Review, "rm") || !strings.HasPrefix(h.Checks, "az repos pr policy list --id 4") {
		t.Fatalf("odd name in a hint: %+v", h)
	}
}

func TestAzureDoctor(t *testing.T) {
	var ran []string
	deps := func(fail string) DoctorDeps {
		return DoctorDeps{
			LookPath: func(string) (string, error) { return "/bin/az", nil },
			Run: func(dir, name string, args ...string) (string, error) {
				ran = append(ran, args[0])
				if args[0] == fail {
					return "", errors.New("exit 1")
				}
				return "", nil
			},
		}
	}
	checks := Azure{Target: shop}.Doctor(deps(""))
	if len(checks) != 3 || !checks[0].OK || !checks[1].OK || !checks[2].OK {
		t.Fatalf("%+v", checks)
	}
	if checks := (Azure{}).Doctor(deps("account")); checks[2].OK || !strings.Contains(checks[2].Detail, "az login") {
		t.Fatalf("%+v", checks)
	}
	if checks := (Azure{}).Doctor(DoctorDeps{LookPath: func(string) (string, error) { return "", exec.ErrNotFound }}); len(checks) != 1 || checks[0].OK {
		t.Fatalf("%+v", checks)
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
		{fmt.Errorf("poll: %w", azError(errors.New("az: boom"))), "az", "", "the user checks az login and az repos pr list in a terminal (tm doctor)"},
		{&CLIError{CLI: "x; rm", Problem: "p", Advice: "a", Err: errors.New("e")}, "the code host CLI", "", "the user checks the code host CLI auth status in a terminal (tm doctor)"},
	} {
		if cli, problem, advice := Advice(c.err); cli != c.cli || problem != c.problem || advice != c.advice {
			t.Errorf("%v: %q %q %q", c.err, cli, problem, advice)
		}
	}
}
