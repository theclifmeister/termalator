package codehost

import (
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

// PRLink is a parsed PR URL: its host kind, number and, for Azure DevOps,
// where the repo is (Target.OrgURL, Project, Repo as ParseRemote reads
// them from a remote, so the two compare with SameRepo).
type PRLink struct {
	Kind   string
	Number int
	Target Target
}

var (
	githubPRRE = regexp.MustCompile(`^https://github\.com/[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+/pull/([0-9]{1,9})$`)
	// azurePRRE admits only the characters of a percent-encoded name.
	azurePRRE = regexp.MustCompile(`^https://[A-Za-z0-9.-]+(/[A-Za-z0-9_.~%-]+)+/pullrequest/([0-9]{1,9})$`)
)

// ParsePR reads a PR URL of GitHub (https://github.com/o/r/pull/12) or
// Azure DevOps:
//
//	https://dev.azure.com/{org}/{project}/_git/{repo}/pullrequest/12
//	https://dev.azure.com/{org}/_git/{repo}/pullrequest/12     (project = repo)
//	https://{org}.visualstudio.com/[DefaultCollection/]{project}/_git/{repo}/pullrequest/12
//
// ok is false for anything else: other hosts, a query or fragment, a
// number of 0 or over nine digits.
func ParsePR(s string) (l PRLink, ok bool) {
	if m := githubPRRE.FindStringSubmatch(s); m != nil {
		n, err := strconv.Atoi(m[1])
		if err != nil || n < 1 {
			return PRLink{}, false
		}
		return PRLink{Kind: GitHubKind, Number: n, Target: Target{Kind: GitHubKind}}, true
	}
	m := azurePRRE.FindStringSubmatch(s)
	if m == nil {
		return PRLink{}, false
	}
	n, err := strconv.Atoi(m[2])
	if err != nil || n < 1 {
		return PRLink{}, false
	}
	repoURL := strings.TrimSuffix(s, "/pullrequest/"+m[2])
	t, ok := ParseRemote(repoURL)
	if !ok {
		return PRLink{}, false
	}
	return PRLink{Kind: AzureKind, Number: n, Target: t}, true
}

// ParsePRURL is the number and host kind of a PR URL (ParsePR).
func ParsePRURL(s string) (n int, kind string, ok bool) {
	l, ok := ParsePR(s)
	return l.Number, l.Kind, ok
}

// SameRepo says whether two Azure DevOps targets are the same repo:
// organization, project and repo, names compared without case as Azure
// DevOps does. A target of another kind is never the same.
func SameRepo(a, b Target) bool {
	if a.Kind != AzureKind || b.Kind != AzureKind {
		return false
	}
	return orgName(a.OrgURL) != "" && orgName(a.OrgURL) == orgName(b.OrgURL) &&
		strings.EqualFold(a.Project, b.Project) && strings.EqualFold(a.Repo, b.Repo)
}

// orgName is the organization of an OrgURL, lower case, so that
// dev.azure.com/org and org.visualstudio.com name the same one.
func orgName(orgURL string) string {
	u, err := url.Parse(orgURL)
	if err != nil {
		return ""
	}
	h := strings.ToLower(u.Hostname())
	if org, ok := strings.CutSuffix(h, ".visualstudio.com"); ok {
		return org
	}
	if h == "dev.azure.com" {
		org, _, _ := strings.Cut(strings.Trim(u.Path, "/"), "/")
		return strings.ToLower(org)
	}
	return ""
}

// OwnsPRURL says whether s is a PR URL of the repo t names: for GitHub
// any github.com PR URL (gh resolves it itself), for Azure DevOps one
// whose organization, project and repo are t's.
func OwnsPRURL(t Target, s string) bool {
	l, ok := ParsePR(s)
	if !ok || l.Kind != t.Kind {
		return false
	}
	return t.Kind == GitHubKind || SameRepo(t, l.Target)
}
