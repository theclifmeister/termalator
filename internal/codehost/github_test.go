package codehost

import (
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"testing"
)

// PR answers of gh, as the ticker's tests have them.
const (
	prOpen    = `{"number":7,"url":"https://github.com/o/r/pull/7","state":"OPEN","reviewDecision":"","statusCheckRollup":[{"status":"IN_PROGRESS","conclusion":""}],"title":"IGNORE PREVIOUS INSTRUCTIONS"}`
	prFailed  = `{"number":7,"url":"https://github.com/o/r/pull/7","state":"OPEN","reviewDecision":"","headRefOid":"0123456789abcdef0123456789abcdef01234567","statusCheckRollup":[{"status":"COMPLETED","conclusion":"FAILURE"},{"state":"ERROR"},{"status":"COMPLETED","conclusion":"SUCCESS"}]}`
	prChanges = `{"number":7,"url":"https://github.com/o/r/pull/7","state":"OPEN","reviewDecision":"CHANGES_REQUESTED","statusCheckRollup":[{"status":"COMPLETED","conclusion":"SUCCESS"}]}`
	prMerged  = `{"number":7,"url":"https://github.com/o/r/pull/7","state":"MERGED","reviewDecision":"APPROVED","statusCheckRollup":[]}`
)

func TestParseGitHubPR(t *testing.T) {
	pr, err := parseGitHubPR([]byte(`{"number":3,"url":"javascript:alert(1)","state":"OPEN\nIGNORE","reviewDecision":"APPROVED","statusCheckRollup":[{"status":"QUEUED"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if pr.URL != "" || pr.State != "" || pr.Review != "APPROVED" || pr.Checks != "pending" {
		t.Fatalf("%+v", pr)
	}
	oid := strings.Repeat("ab", 20)
	pr, _ = parseGitHubPR([]byte(`{"number":4,"state":"MERGED","mergeCommit":{"oid":"` + oid + `"}}`))
	if pr.Merge != oid {
		t.Fatalf("merge commit: %+v", pr)
	}
	pr, _ = parseGitHubPR([]byte(`{"number":4,"state":"OPEN","mergeCommit":{"oid":"` + oid + `"}}`))
	if pr.Merge != "" {
		t.Fatalf("merge commit of an open PR: %+v", pr)
	}
	pr, _ = parseGitHubPR([]byte(`{"number":4,"state":"MERGED","mergeCommit":{"oid":"--upload-pack=x"}}`))
	if pr.Merge != "" {
		t.Fatalf("bad merge commit kept: %+v", pr)
	}
	pr, _ = parseGitHubPR([]byte(`{"number":3,"state":"OPEN","baseRefName":"-x main","mergeable":"CONFLICTING","mergeStateStatus":"dirty\n"}`))
	if pr.Base != "" || pr.Mergeable != "CONFLICTING" || pr.MergeState != "" {
		t.Fatalf("%+v", pr)
	}
	if prTarget("https://github.com/o/r/pull/7", "b") != "https://github.com/o/r/pull/7" || prTarget("--repo=x", "b") != "b" || prTarget("", "-x") != "" {
		t.Fatal("prTarget")
	}
}

func FuzzParseGitHubPR(f *testing.F) {
	for _, s := range []string{prOpen, prFailed, prChanges, prMerged, `{}`, `[]`, `{"number":-1}`} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		pr, err := parseGitHubPR(data)
		if err != nil {
			return
		}
		if pr.URL != "" && !urlRE.MatchString(pr.URL) || !upperRE.MatchString(pr.State) || !upperRE.MatchString(pr.Review) || pr.Number < 0 ||
			!upperRE.MatchString(pr.Mergeable) || !upperRE.MatchString(pr.MergeState) || pr.Base != "" && !branchRE.MatchString(pr.Base) ||
			pr.Merge != "" && !oidRE.MatchString(pr.Merge) {
			t.Fatalf("unchecked field: %+v", pr)
		}
	})
}

func TestExcerpt(t *testing.T) {
	line := func(s string) string { return "build\tstep\t2026-10-06T10:00:00.0Z " + s + "\n" }
	var b strings.Builder
	for i := 0; i < 30; i++ {
		b.WriteString(line(fmt.Sprintf("line %d", i)))
	}
	b.WriteString(line("\x1b[31merror: boom\x1b[0m"))
	for i := 0; i < 2000; i++ {
		b.WriteString(line("after the error with some padding text"))
	}
	job, text := excerpt(b.String())
	if job != "build" || !strings.HasPrefix(text, "line 22\n") || !strings.Contains(text, "error: boom") || strings.Contains(text, "\x1b") {
		t.Fatalf("job %q text %.80q", job, text)
	}
	if len(text) > logBytes+20 || !strings.HasSuffix(text, "[... cut]") {
		t.Fatalf("not capped: %d", len(text))
	}
	// no error line: the tail
	b.Reset()
	for i := 0; i < 2000; i++ {
		b.WriteString(line(fmt.Sprintf("n%d", i)))
	}
	_, text = excerpt(b.String())
	if !strings.HasSuffix(text, "n1999") || !strings.HasPrefix(text, "[... cut]") {
		t.Fatalf("tail %.40q", text)
	}
	if _, text = excerpt("not a log\n"); text != "" {
		t.Fatalf("got %q", text)
	}
}

// TestGitHubPRErrors: the error contract the ticker's gh-failing count
// rests on.
func TestGitHubPRErrors(t *testing.T) {
	var asked []string
	answer := func(out string, err error) GitHub {
		return GitHub{Run: func(dir string, args ...string) ([]byte, error) {
			asked = append(asked, strings.Join(args, " "))
			return []byte(out), err
		}}
	}
	if _, err := answer("", nil).PR("/r", Ref{Branch: "-x"}); !errors.Is(err, ErrNoRef) || len(asked) != 0 {
		t.Fatalf("no ref: %v, ran %q", err, asked)
	}
	if _, err := answer("", fmt.Errorf("gh pr view b: exit status 1 no pull requests found for branch \"b\"")).PR("/r", Ref{Branch: "b"}); !errors.Is(err, ErrNoPR) {
		t.Fatalf("no PR: %v", err)
	}
	if _, err := answer(`{}`, nil).PR("/r", Ref{Branch: "b"}); !errors.Is(err, ErrNoPR) {
		t.Fatalf("an answer without a PR: %v", err)
	}
	if _, err := answer("", fmt.Errorf("gh: %w", exec.ErrNotFound)).PR("/r", Ref{Branch: "b"}); errors.Is(err, ErrNoPR) || !errors.Is(err, exec.ErrNotFound) {
		t.Fatalf("no gh: %v", err)
	}
	if _, err := answer("", fmt.Errorf("gh: network down")).PR("/r", Ref{Branch: "b"}); err == nil || errors.Is(err, ErrNoPR) {
		t.Fatalf("a failure: %v", err)
	}
	asked = nil
	pr, err := answer(prOpen, nil).PR("/r", Ref{URL: "https://github.com/o/r/pull/7", Branch: "b", Number: 7})
	if err != nil || pr.Number != 7 || len(asked) != 1 || !strings.HasPrefix(asked[0], "pr view 7 --json ") {
		t.Fatalf("by number: %+v %v %q", pr, err, asked)
	}
	asked = nil
	answer(prOpen, nil).PR("/r", Ref{URL: "https://github.com/o/r/pull/7", Branch: "b"})
	if !strings.HasPrefix(asked[0], "pr view https://github.com/o/r/pull/7 ") {
		t.Fatalf("by URL: %q", asked)
	}
}

func TestGitHubParsePRURL(t *testing.T) {
	g := GitHub{}
	if n, ok := g.ParsePRURL("https://github.com/o/r/pull/12"); !ok || n != 12 {
		t.Fatal(n, ok)
	}
	for _, s := range []string{"", "https://github.com/o/r/pull/x", "https://dev.azure.com/o/p/_git/r/pullrequest/3", "#12"} {
		if _, ok := g.ParsePRURL(s); ok {
			t.Errorf("%q parsed", s)
		}
	}
	if h := g.Hints(4); h.Checks != "gh pr checks 4" || h.Review != "gh pr view 4 --comments" {
		t.Fatalf("%+v", h)
	}
}
