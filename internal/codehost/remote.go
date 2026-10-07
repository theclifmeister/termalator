package codehost

import (
	"net/url"
	"strings"
)

// ParseRemote reads an Azure DevOps remote URL; ok is false for any
// other (GitHub's, a server's Detect doesn't know). The forms:
//
//	https://dev.azure.com/{org}/{project}/_git/{repo}
//	https://{user}@dev.azure.com/{org}/{project}/_git/{repo}
//	https://dev.azure.com/{org}/_git/{repo}                  (project = repo)
//	https://{org}.visualstudio.com/[DefaultCollection/]{project}/_git/{repo}
//	git@ssh.dev.azure.com:v3/{org}/{project}/{repo}         (or ssh://…/v3/…)
//	{org}@vs-ssh.visualstudio.com:v3/{org}/{project}/{repo}
//
// Project and repo names are percent-decoded ("My%20Project").
func ParseRemote(remote string) (t Target, ok bool) {
	host, path, ok := splitRemote(remote)
	if !ok {
		return Target{}, false
	}
	seg := strings.Split(strings.Trim(path, "/"), "/")
	for i, s := range seg {
		d, err := url.PathUnescape(s)
		if err != nil || d == "" {
			return Target{}, false
		}
		seg[i] = d
	}
	t.Kind = AzureKind
	switch {
	case host == "ssh.dev.azure.com" || host == "vs-ssh.visualstudio.com":
		// v3/{org}/{project}/{repo}
		if len(seg) != 4 || seg[0] != "v3" {
			return Target{}, false
		}
		t.OrgURL = "https://dev.azure.com/" + url.PathEscape(seg[1])
		if host == "vs-ssh.visualstudio.com" {
			t.OrgURL = "https://" + seg[1] + ".visualstudio.com"
		}
		t.Project, t.Repo = seg[2], seg[3]
	case host == "dev.azure.com":
		// {org}/[{project}/]_git/{repo}
		if len(seg) < 3 {
			return Target{}, false
		}
		t.OrgURL = "https://dev.azure.com/" + url.PathEscape(seg[0])
		if t.Project, t.Repo, ok = gitSegs(seg[1:]); !ok {
			return Target{}, false
		}
	case strings.HasSuffix(host, ".visualstudio.com"):
		// [DefaultCollection/][{project}/]_git/{repo}
		org := strings.TrimSuffix(host, ".visualstudio.com")
		if org == "" || strings.Contains(org, ".") {
			return Target{}, false
		}
		t.OrgURL = "https://" + host
		if len(seg) > 0 && strings.EqualFold(seg[0], "DefaultCollection") {
			seg = seg[1:]
		}
		if t.Project, t.Repo, ok = gitSegs(seg); !ok {
			return Target{}, false
		}
	default:
		return Target{}, false
	}
	return t, true
}

// gitSegs reads "{project}/_git/{repo}" or "_git/{repo}" (project =
// repo).
func gitSegs(seg []string) (project, repo string, ok bool) {
	switch {
	case len(seg) == 2 && seg[0] == "_git":
		return seg[1], seg[1], true
	case len(seg) == 3 && seg[1] == "_git":
		return seg[0], seg[2], true
	}
	return "", "", false
}

// gitPath is the project and repo of any URL whose path ends in
// "[{project}/]_git/{repo}" (an Azure DevOps Server's, say), "" when
// it doesn't.
func gitPath(remote string) (project, repo string) {
	_, path, ok := splitRemote(remote)
	if !ok {
		return "", ""
	}
	seg := strings.Split(strings.Trim(path, "/"), "/")
	for i := range seg {
		if seg[i] != "_git" || i != len(seg)-2 {
			continue
		}
		var err error
		if repo, err = url.PathUnescape(seg[i+1]); err != nil {
			return "", ""
		}
		project = repo
		if i > 0 {
			if project, err = url.PathUnescape(seg[i-1]); err != nil {
				return "", ""
			}
		}
		return project, repo
	}
	return "", ""
}

// splitRemote is a remote URL's host (lower case, no user or port) and
// path, in URL form (scheme://[user@]host/path) or scp form
// ([user@]host:path).
func splitRemote(remote string) (host, path string, ok bool) {
	remote = strings.TrimSpace(remote)
	if strings.Contains(remote, "://") {
		u, err := url.Parse(remote)
		if err != nil || u.Host == "" {
			return "", "", false
		}
		return strings.ToLower(u.Hostname()), u.EscapedPath(), true
	}
	hostPart, path, ok := strings.Cut(remote, ":")
	if !ok || strings.Contains(hostPart, "/") {
		return "", "", false
	}
	if _, h, at := strings.Cut(hostPart, "@"); at {
		hostPart = h
	}
	if hostPart == "" {
		return "", "", false
	}
	return strings.ToLower(hostPart), path, true
}
