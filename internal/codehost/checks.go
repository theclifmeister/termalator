package codehost

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// RepoHost is one repo in use and where its PRs live.
type RepoHost struct {
	Repo   string // the checkout
	Target Target
}

// Checks is every code-host line tm doctor shows, for the repos in use:
// GitHub's gh once (also when there are no repos, as on a fresh
// install), Azure DevOps' az once, then each Azure repo's access and
// git's credentials for its origin. Both tm doctor and the server run
// it, the server so that it answers from the context its sessions have
// (docs/SPEC.md §3.3 server.codehost).
func Checks(d DoctorDeps, hosts []RepoHost) []Check {
	github, azure := len(hosts) == 0, false
	for _, h := range hosts {
		switch h.Target.Kind {
		case AzureKind:
			azure = true
		default:
			github = true
		}
	}
	var out []Check
	if github {
		out = append(out, GitHub{}.Doctor(d)...)
	}
	if !azure {
		return out
	}
	out = append(out, Azure{}.Doctor(d)...)
	seen := map[Target]bool{}
	for _, h := range hosts {
		if h.Target.Kind != AzureKind || seen[h.Target] {
			continue
		}
		seen[h.Target] = true
		out = append(out, Azure{Target: h.Target}.Access(d)...)
		out = append(out, gitCredentials(d, h))
	}
	return out
}

// gitCredentials proves git can read the repo's origin without asking
// for a password (Exec sets GIT_TERMINAL_PROMPT=0): what fetches and
// pushes of a thread's branch need.
func gitCredentials(d DoctorDeps, h RepoHost) Check {
	name := "git origin " + h.Target.Project + "/" + h.Target.Repo
	if _, err := d.Run(h.Repo, "git", "ls-remote", "origin", "HEAD"); err != nil {
		first, _, _ := strings.Cut(strings.TrimSpace(err.Error()), "\n")
		return Check{Name: name, Detail: "git can't read origin without a prompt: set up a git credential helper for Azure DevOps (" + first + ")"}
	}
	return Check{Name: name, OK: true, Detail: "readable"}
}

// execTimeout bounds every program Exec runs.
const execTimeout = 10 * time.Second

// Exec runs a program in dir and returns its combined, trimmed output;
// the error carries its first line.
func Exec(dir, name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), execTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	s := strings.TrimSpace(out.String())
	if err != nil {
		if s == "" {
			s = err.Error()
		}
		first, _, _ := strings.Cut(s, "\n")
		return s, fmt.Errorf("%s %s: %s", name, strings.Join(args, " "), first)
	}
	return s, nil
}

// SystemDeps is DoctorDeps on the real system, in this process's context.
func SystemDeps() DoctorDeps {
	return DoctorDeps{LookPath: exec.LookPath, Run: Exec, Getenv: os.Getenv}
}
