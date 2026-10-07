// Package codehost is where a repo's pull requests live (docs/SPEC.md
// §7.5): GitHub through gh today, Azure DevOps through az next. The
// ticker, resolve and doctor ask a Host, picked per repo from its origin
// URL (Pick), so the rest of tm keeps one set of fixed words for a PR
// (PR) whatever the host. It imports no other tm package, so worktree,
// ticker and doctor can all use it.
package codehost

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"regexp"
	"strings"
)

// Host kinds (Host.Kind, Config.CodeHost).
const (
	GitHubKind = "github"
	AzureKind  = "azure"
)

// ErrNoPR means the host works but has no PR for what was asked. Errors
// that wrap exec.ErrNotFound mean its CLI isn't installed (tm doctor
// says so); every other error is a failed call.
var ErrNoPR = errors.New("no pull request found")

// ErrNoRef means there was nothing to look a PR up by: no PR URL of this
// host and no usable branch. Nothing ran.
var ErrNoRef = errors.New("nothing to look a pull request up by")

// Ref names a PR to look up: by Number when set, else by URL when it is
// one of this host's PR URLs, else by the Branch it comes from.
type Ref struct {
	URL    string
	Branch string
	Number int
}

// Hints are the commands a prompt names for reading PR n's checks and
// its review (fixed text with the number).
type Hints struct {
	Checks string // `gh pr checks 12`
	Review string // `gh pr view 12 --comments`
}

// Check is one tm doctor line about a host's tooling; the doctor
// package puts it in its toolchain group.
type Check struct {
	Name   string
	OK     bool // false is a warning
	Detail string
}

// DoctorDeps is what Doctor may touch: doctor.Deps' LookPath and Run.
type DoctorDeps struct {
	LookPath func(string) (string, error)
	Run      func(dir, name string, args ...string) (string, error)
}

// Host is one pull-request provider. Every method takes the repo (a
// checkout of it) the PR belongs to.
type Host interface {
	// Kind is GitHubKind or AzureKind.
	Kind() string
	// PR looks a PR up. ErrNoRef when ref names nothing, ErrNoPR when the
	// host has none; other errors are failures (see ErrNoPR).
	PR(repo string, ref Ref) (PR, error)
	// FailedLog is the failing CI job of pr's head and an excerpt of its
	// log, "" when there is none or it can't be had.
	FailedLog(repo string, pr PR) (job, log string)
	// PRState is the state ("MERGED", "OPEN", "CLOSED") of the PR whose
	// head is branch, "" when there is none or the host can't tell.
	PRState(repo, branch string) string
	// PRHead is the state, number and head branch of the PR at url; all
	// zero when the host can't tell.
	PRHead(repo, url string) (state string, number int, head string)
	// MergeCommit is the commit that merged PR n into the default branch
	// as last fetched, by git alone; "" for none.
	MergeCommit(repo string, n int) string
	// MergedPR is the PR number commit's subject says it merged, 0 for
	// none, by git alone.
	MergedPR(repo, commit string) int
	// Hints are the commands prompts name for PR n.
	Hints(n int) Hints
	// Doctor checks the host's CLI and its login.
	Doctor(d DoctorDeps) []Check
	// ParsePRURL is the number of one of this host's PR URLs.
	ParsePRURL(s string) (n int, ok bool)
}

// Config is a project's override of the host (PROJECT.md code_host and
// azure_url, docs/SPEC.md §5.1). Zero picks by the origin URL.
type Config struct {
	// CodeHost is GitHubKind, AzureKind or "" (by origin).
	CodeHost string
	// AzureURL is the Azure DevOps organization (or collection) URL, for
	// a server whose host Detect doesn't know.
	AzureURL string
}

// Target is what Detect makes of a repo: its host kind and, for Azure
// DevOps, where the repo is.
type Target struct {
	Kind    string
	OrgURL  string // https://dev.azure.com/org, https://org.visualstudio.com
	Project string
	Repo    string
}

// Detect tells which host repo's PRs live on, from cfg, else its origin
// URL (ParseRemote); GitHub when neither says Azure DevOps.
func Detect(repo string, cfg Config) Target {
	url := origin(repo)
	switch cfg.CodeHost {
	case GitHubKind:
		return Target{Kind: GitHubKind}
	case AzureKind:
		t, _ := ParseRemote(url)
		t.Kind = AzureKind
		if cfg.AzureURL != "" {
			t.OrgURL = strings.TrimRight(cfg.AzureURL, "/")
			if t.Project == "" {
				t.Project, t.Repo = gitPath(url)
			}
		}
		return t
	}
	if t, ok := ParseRemote(url); ok {
		return t
	}
	return Target{Kind: GitHubKind}
}

// Pick is the Host for repo (Detect). A repo on Azure DevOps gets GitHub
// too until the Azure DevOps host is registered (newAzure), as before.
func Pick(repo string, cfg Config) Host {
	if newAzure != nil {
		if t := Detect(repo, cfg); t.Kind == AzureKind {
			return newAzure(t)
		}
	}
	return GitHub{}
}

// newAzure makes the Azure DevOps host for a target; nil while there is
// none.
var newAzure func(Target) Host

// origin is repo's origin URL, "" for none.
func origin(repo string) string {
	if repo == "" {
		return ""
	}
	out, err := git(repo, "remote", "get-url", "origin")
	if err != nil {
		return ""
	}
	return out
}

// git runs git in dir and returns its trimmed stdout.
func git(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return "", err
	}
	return strings.TrimSpace(out.String()), nil
}

// Advice is what to tell the user about a code host call that failed
// with err, in fixed words: the CLI that failed ("gh", "az"), what went
// wrong ("" when tm can't tell) and what the user does about it.
func Advice(err error) (cli, problem, advice string) {
	var ce *CLIError
	if errors.As(err, &ce) {
		cli, problem, advice = ce.CLI, ce.Problem, ce.Advice
	}
	switch {
	case cli == "":
		cli = "gh"
	case !cliRE.MatchString(cli):
		cli, problem, advice = "the code host CLI", "", ""
	}
	if advice == "" {
		advice = "the user checks " + cli + " auth status in a terminal (tm doctor)"
		if cli == "az" {
			advice = "the user checks az login and az repos pr list in a terminal (tm doctor)"
		}
	}
	return cli, problem, advice
}

var cliRE = regexp.MustCompile(`^[a-z]{1,8}$`)
