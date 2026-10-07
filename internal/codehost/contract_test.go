package codehost

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

// hostFake is one host whose CLI answers a scenario: PR answers its PR
// call for branch b, or the error.
type hostFake struct {
	name string
	make func(s scenario) Host
}

// scenario is one PR situation as each host's CLI would answer it.
type scenario struct {
	name string
	gh   string // `gh pr view --json`; "!" prefix => error text
	az   string // `az repos pr list`, else "!" error text
	pol  string // `az repos pr policy list`
	sts  string // `az devops invoke` PR statuses
	want PR     // URL and Head left out
	err  error  // ErrNoPR, or errCLI for a CLI failure
}

var errCLI = errors.New("a CLI failure")

const sha = "0123456789abcdef0123456789abcdef01234567"

func azList(status, mergeStatus, extra string) string {
	return fmt.Sprintf(`[{"pullRequestId":7,"status":%q,"mergeStatus":%q,"targetRefName":"refs/heads/main",`+
		`"lastMergeSourceCommit":{"commitId":%q},"repository":{"name":"web","project":{"id":"`+shopPID+`","name":"Shop"}}%s}]`, status, mergeStatus, sha, extra)
}

var buildPolicy = func(status string) string {
	return `[{"status":"` + status + `","configuration":{"isBlocking":true,"isEnabled":true,"type":{"id":"` + policyBuild + `"}}}]`
}

var scenarios = []scenario{
	{name: "opened, checks running",
		gh: `{"number":7,"state":"OPEN","baseRefName":"main","mergeable":"MERGEABLE","statusCheckRollup":[{"status":"IN_PROGRESS"}]}`,
		az: azList("active", "succeeded", ""), pol: buildPolicy("running"),
		want: PR{Number: 7, State: "OPEN", Checks: "pending", Base: "main", Mergeable: "MERGEABLE"}},
	{name: "checks failed",
		gh: `{"number":7,"state":"OPEN","baseRefName":"main","mergeable":"MERGEABLE","statusCheckRollup":[{"status":"COMPLETED","conclusion":"FAILURE"}]}`,
		az: azList("active", "succeeded", ""), pol: buildPolicy("rejected"),
		want: PR{Number: 7, State: "OPEN", Checks: "fail", Failed: 1, Base: "main", Mergeable: "MERGEABLE"}},
	{name: "checks passed, external status failed",
		gh: `{"number":7,"state":"OPEN","baseRefName":"main","mergeable":"MERGEABLE","statusCheckRollup":[{"status":"COMPLETED","conclusion":"SUCCESS"},{"state":"FAILURE"}]}`,
		az: azList("active", "succeeded", ""), pol: buildPolicy("approved"), sts: `{"value":[{"id":1,"state":"failed","context":{"name":"x"}}]}`,
		want: PR{Number: 7, State: "OPEN", Checks: "fail", Failed: 1, Base: "main", Mergeable: "MERGEABLE"}},
	{name: "changes requested",
		gh: `{"number":7,"state":"OPEN","reviewDecision":"CHANGES_REQUESTED","baseRefName":"main","mergeable":"MERGEABLE","statusCheckRollup":[{"status":"COMPLETED","conclusion":"SUCCESS"}]}`,
		az: azList("active", "succeeded", `,"reviewers":[{"vote":-10}]`), pol: buildPolicy("approved"),
		want: PR{Number: 7, State: "OPEN", Checks: "pass", Review: "CHANGES_REQUESTED", Base: "main", Mergeable: "MERGEABLE"}},
	{name: "approved, no CI",
		gh: `{"number":7,"state":"OPEN","reviewDecision":"APPROVED","baseRefName":"main","mergeable":"MERGEABLE","statusCheckRollup":[]}`,
		az: azList("active", "succeeded", `,"reviewers":[{"vote":10,"isRequired":true}]`), pol: `[]`,
		want: PR{Number: 7, State: "OPEN", Review: "APPROVED", Base: "main", Mergeable: "MERGEABLE"}},
	{name: "conflict",
		gh: `{"number":7,"state":"OPEN","baseRefName":"main","mergeable":"CONFLICTING","mergeStateStatus":"DIRTY"}`,
		az: azList("active", "conflicts", ""), pol: `[]`,
		want: PR{Number: 7, State: "OPEN", Base: "main", Mergeable: "CONFLICTING", MergeState: "DIRTY"}},
	{name: "merge state not computed yet",
		gh: `{"number":7,"state":"OPEN","baseRefName":"main","mergeable":"UNKNOWN"}`,
		az: azList("active", "queued", ""), pol: `[]`,
		want: PR{Number: 7, State: "OPEN", Base: "main", Mergeable: "UNKNOWN"}},
	{name: "merged (squash)",
		gh:   `{"number":7,"state":"MERGED","baseRefName":"main","mergeable":"UNKNOWN","mergeCommit":{"oid":"` + strings.Repeat("ab", 20) + `"}}`,
		az:   azList("completed", "notSet", `,"lastMergeCommit":{"commitId":"`+strings.Repeat("ab", 20)+`"}`),
		want: PR{Number: 7, State: "MERGED", Base: "main", Mergeable: "UNKNOWN", Merge: strings.Repeat("ab", 20)}},
	{name: "closed",
		gh:   `{"number":7,"state":"CLOSED","baseRefName":"main","mergeable":"UNKNOWN"}`,
		az:   azList("abandoned", "notSet", ""),
		want: PR{Number: 7, State: "CLOSED", Base: "main", Mergeable: "UNKNOWN"}},
	{name: "no PR",
		gh:  `!no pull requests found for branch "b"`,
		az:  `[]`,
		err: ErrNoPR},
	{name: "logged out",
		gh:  `!To get started with GitHub CLI, please run:  gh auth login`,
		az:  `!ERROR: Please run 'az login' to setup account.`,
		err: errCLI},
}

var hostFakes = []hostFake{
	{"github", func(s scenario) Host {
		return GitHub{Run: func(dir string, args ...string) ([]byte, error) {
			if e, bad := strings.CutPrefix(s.gh, "!"); bad {
				return nil, fmt.Errorf("gh %s: exit status 1 %s", strings.Join(args, " "), e)
			}
			return []byte(s.gh), nil
		}}
	}},
	{"azure", func(s scenario) Host {
		return Azure{Target: shop, Getenv: func(string) string { return "" }, Run: func(dir string, args ...string) ([]byte, error) {
			out := s.az
			switch route, _ := azRoute(args); route {
			case "statuses":
				out = s.sts
			case "policies":
				out = s.pol
			}
			if e, bad := strings.CutPrefix(out, "!"); bad {
				return nil, azError(fmt.Errorf("az %s: exit status 1 %s", strings.Join(args, " "), e))
			}
			return []byte(out), nil
		}}
	}},
}

// TestHostContract: every host maps the same situation onto the same
// fixed fields and the same errors, so the ticker needn't know which
// one it asked.
func TestHostContract(t *testing.T) {
	for _, s := range scenarios {
		for _, h := range hostFakes {
			pr, err := h.make(s).PR("/r", Ref{Branch: "b"})
			switch {
			case s.err == ErrNoPR:
				if !errors.Is(err, ErrNoPR) {
					t.Errorf("%s/%s: %v, want no PR", h.name, s.name, err)
				}
				continue
			case s.err == errCLI:
				if err == nil || errors.Is(err, ErrNoPR) {
					t.Errorf("%s/%s: %v, want a failure", h.name, s.name, err)
				}
				continue
			case err != nil:
				t.Errorf("%s/%s: %v", h.name, s.name, err)
				continue
			}
			if h.name == "azure" && (pr.URL != AzurePRURL(shop, 7) || pr.Head != sha) {
				t.Errorf("%s/%s: URL %q head %q", h.name, s.name, pr.URL, pr.Head)
			}
			pr.URL, pr.Head = "", ""
			if pr != s.want {
				t.Errorf("%s/%s:\n got %+v\nwant %+v", h.name, s.name, pr, s.want)
			}
			if pr.Summary() != s.want.Summary() {
				t.Errorf("%s/%s: summary %q", h.name, s.name, pr.Summary())
			}
		}
	}
}
