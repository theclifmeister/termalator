package codehost

import (
	"errors"
	"os"
	"os/exec"
	"testing"
)

func TestParseRemote(t *testing.T) {
	az := func(org, project, repo string) Target {
		return Target{Kind: AzureKind, OrgURL: org, Project: project, Repo: repo}
	}
	for _, c := range []struct {
		url  string
		want Target // zero: not Azure DevOps
	}{
		{"https://dev.azure.com/acme/Shop/_git/web", az("https://dev.azure.com/acme", "Shop", "web")},
		{"https://acme@dev.azure.com/acme/Shop/_git/web", az("https://dev.azure.com/acme", "Shop", "web")},
		{"https://dev.azure.com/acme/My%20Project/_git/My%20Repo", az("https://dev.azure.com/acme", "My Project", "My Repo")},
		{"https://dev.azure.com/acme/_git/web", az("https://dev.azure.com/acme", "web", "web")},
		{"https://acme.visualstudio.com/Shop/_git/web", az("https://acme.visualstudio.com", "Shop", "web")},
		{"https://acme.visualstudio.com/DefaultCollection/Shop/_git/web", az("https://acme.visualstudio.com", "Shop", "web")},
		{"https://acme.visualstudio.com/_git/web", az("https://acme.visualstudio.com", "web", "web")},
		{"git@ssh.dev.azure.com:v3/acme/Shop/web", az("https://dev.azure.com/acme", "Shop", "web")},
		{"git@ssh.dev.azure.com:v3/acme/My%20Project/web", az("https://dev.azure.com/acme", "My Project", "web")},
		{"ssh://git@ssh.dev.azure.com/v3/acme/Shop/web", az("https://dev.azure.com/acme", "Shop", "web")},
		{"acme@vs-ssh.visualstudio.com:v3/acme/Shop/web", az("https://acme.visualstudio.com", "Shop", "web")},
		{"HTTPS://Dev.Azure.com/acme/Shop/_git/web", az("https://dev.azure.com/acme", "Shop", "web")},
		// not Azure DevOps, or not a repo URL
		{"https://github.com/o/r.git", Target{}},
		{"git@github.com:o/r.git", Target{}},
		{"https://ghe.example.com/o/r", Target{}},
		{"https://tfs.example.com/tfs/Coll/Shop/_git/web", Target{}},
		{"https://dev.azure.com/acme/Shop/web", Target{}},
		{"https://dev.azure.com/acme", Target{}},
		{"git@ssh.dev.azure.com:v2/acme/Shop/web", Target{}},
		{"git@ssh.dev.azure.com:v3/acme/web", Target{}},
		{"https://a.b.visualstudio.com/Shop/_git/web", Target{}},
		{"https://dev.azure.com/acme/Shop/_git/bad%zz", Target{}},
		{"/local/path/repo", Target{}},
		{"", Target{}},
	} {
		got, ok := ParseRemote(c.url)
		if ok != (c.want != Target{}) || got != c.want {
			t.Errorf("ParseRemote(%q) = %+v, %v; want %+v", c.url, got, ok, c.want)
		}
	}
}

func TestGitPath(t *testing.T) {
	for url, want := range map[string][2]string{
		"https://tfs.example.com/tfs/Coll/Shop/_git/web": {"Shop", "web"},
		"https://tfs.example.com/tfs/Coll/_git/web":      {"Coll", "web"},
		"https://tfs.example.com/_git/My%20Repo":         {"My Repo", "My Repo"},
		"https://github.com/o/r":                         {"", ""},
	} {
		if p, r := gitPath(url); p != want[0] || r != want[1] {
			t.Errorf("gitPath(%q) = %q, %q", url, p, r)
		}
	}
}

// TestDetect: a repo's origin picks its host, and the project's
// override wins.
func TestDetect(t *testing.T) {
	repo := t.TempDir()
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	run("init", "-q")
	if got := Detect(repo, Config{}); got != (Target{Kind: NoKind}) {
		t.Fatalf("no origin: %+v", got)
	}
	if _, ok := Pick(repo, Config{}).(NoHost); !ok {
		t.Fatalf("no origin: Pick = %T, want NoHost", Pick(repo, Config{}))
	}
	if _, err := Pick(repo, Config{}).PR(repo, Ref{Branch: "x"}); !errors.Is(err, ErrNoRef) {
		t.Fatalf("no origin: PR err = %v, want ErrNoRef (nothing to poll)", err)
	}
	for url, want := range map[string]string{
		"https://github.com/o/r.git":   GitHubKind,
		"git@ghe.corp.example:o/r.git": GitHubKind, // maybe GitHub Enterprise
		"https://gitlab.com/o/r.git":   NoKind,
		"git@bitbucket.org:o/r.git":    NoKind,
		"/srv/git/r.git":               NoKind,
		"file:///srv/git/r.git":        NoKind,
		"../sibling":                   NoKind,
	} {
		run("config", "remote.origin.url", url)
		if got := Detect(repo, Config{}).Kind; got != want {
			t.Errorf("origin %s: kind %q, want %q", url, got, want)
		}
	}
	run("config", "remote.origin.url", "https://gitlab.com/o/r.git")
	if got := Detect(repo, Config{CodeHost: GitHubKind}).Kind; got != GitHubKind {
		t.Errorf("override on a gitlab origin: %q", got)
	}
	run("remote", "remove", "origin")
	run("remote", "add", "origin", "https://acme@dev.azure.com/acme/Shop/_git/web")
	if got := Detect(repo, Config{}); got != (Target{Kind: AzureKind, OrgURL: "https://dev.azure.com/acme", Project: "Shop", Repo: "web"}) {
		t.Fatalf("azure origin: %+v", got)
	}
	// An insteadOf rewrite (a mirror, a local copy) doesn't change whose
	// PRs they are.
	run("config", "url./srv/mirror/web.insteadOf", "https://acme@dev.azure.com/acme/Shop/_git/web")
	if got := Detect(repo, Config{}); got.Kind != AzureKind || got.Repo != "web" {
		t.Fatalf("rewritten origin: %+v", got)
	}
	if got := Detect(repo, Config{CodeHost: GitHubKind}); got.Kind != GitHubKind {
		t.Fatalf("override github: %+v", got)
	}
	run("remote", "set-url", "origin", "https://tfs.example.com/tfs/Coll/Shop/_git/web")
	if got := Detect(repo, Config{}); got.Kind != GitHubKind {
		t.Fatalf("unknown server: %+v", got)
	}
	got := Detect(repo, Config{CodeHost: AzureKind, AzureURL: "https://tfs.example.com/tfs/Coll/"})
	if got != (Target{Kind: AzureKind, OrgURL: "https://tfs.example.com/tfs/Coll", Project: "Shop", Repo: "web"}) {
		t.Fatalf("server with override: %+v", got)
	}
	if h := Pick(repo, Config{}); h.Kind() != GitHubKind {
		t.Fatalf("Pick: %s", h.Kind())
	}
	if got := Detect(os.DevNull, Config{}); got.Kind != NoKind {
		t.Fatalf("not a repo: %+v", got)
	}
}
