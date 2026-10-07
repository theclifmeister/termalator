package codehost

import "testing"

func TestParsePRURL(t *testing.T) {
	for _, c := range []struct {
		in   string
		n    int
		kind string
		ok   bool
	}{
		{"https://github.com/o/r/pull/12", 12, GitHubKind, true},
		{"https://github.com/o/r/pull/0", 0, "", false},
		{"https://github.com/o/r/pull/1234567890", 0, "", false},
		{"https://github.com/o/r/pull/12?x=1", 0, "", false},
		{"https://github.com/o/pull/12", 0, "", false},
		{"https://evil.example/o/r/pull/12", 0, "", false},
		{"http://github.com/o/r/pull/12", 0, "", false},
		{"https://dev.azure.com/org/proj/_git/repo/pullrequest/7", 7, AzureKind, true},
		{"https://dev.azure.com/org/_git/repo/pullrequest/7", 7, AzureKind, true},
		{"https://dev.azure.com/org/My%20Proj/_git/repo/pullrequest/7", 7, AzureKind, true},
		{"https://org.visualstudio.com/proj/_git/repo/pullrequest/7", 7, AzureKind, true},
		{"https://org.visualstudio.com/DefaultCollection/proj/_git/repo/pullrequest/7", 7, AzureKind, true},
		{"https://dev.azure.com/org/proj/_git/repo/pullrequest/0", 0, "", false},
		{"https://dev.azure.com/org/proj/_git/repo/pullrequest/7/", 0, "", false},
		{"https://dev.azure.com/org/proj/_git/repo/pullrequest/7?_a=files", 0, "", false},
		{"https://dev.azure.com/org/proj/repo/pullrequest/7", 0, "", false},
		{"https://dev.azure.com/org/proj/_git/repo/pull/7", 0, "", false},
		{"https://example.com/org/proj/_git/repo/pullrequest/7", 0, "", false},
		{"", 0, "", false},
	} {
		n, kind, ok := ParsePRURL(c.in)
		if n != c.n || kind != c.kind || ok != c.ok {
			t.Errorf("ParsePRURL(%q) = %d, %q, %v; want %d, %q, %v", c.in, n, kind, ok, c.n, c.kind, c.ok)
		}
	}
}

func TestOwnsPRURL(t *testing.T) {
	az := Target{Kind: AzureKind, OrgURL: "https://dev.azure.com/Org", Project: "Proj", Repo: "Repo"}
	vs := Target{Kind: AzureKind, OrgURL: "https://org.visualstudio.com", Project: "proj", Repo: "repo"}
	for _, c := range []struct {
		t    Target
		in   string
		want bool
	}{
		{az, "https://dev.azure.com/org/proj/_git/repo/pullrequest/3", true},
		{az, "https://org.visualstudio.com/proj/_git/repo/pullrequest/3", true},
		{vs, "https://dev.azure.com/ORG/proj/_git/repo/pullrequest/3", true},
		{az, "https://dev.azure.com/other/proj/_git/repo/pullrequest/3", false},
		{az, "https://dev.azure.com/org/other/_git/repo/pullrequest/3", false},
		{az, "https://dev.azure.com/org/proj/_git/other/pullrequest/3", false},
		{az, "https://github.com/o/r/pull/3", false},
		{Target{Kind: GitHubKind}, "https://github.com/o/r/pull/3", true},
		{Target{Kind: GitHubKind}, "https://dev.azure.com/org/proj/_git/repo/pullrequest/3", false},
	} {
		if got := OwnsPRURL(c.t, c.in); got != c.want {
			t.Errorf("OwnsPRURL(%+v, %q) = %v; want %v", c.t, c.in, got, c.want)
		}
	}
}

func FuzzParsePRURL(f *testing.F) {
	for _, s := range []string{"https://github.com/o/r/pull/1", "https://dev.azure.com/o/p/_git/r/pullrequest/2", "https://o.visualstudio.com/DefaultCollection/p/_git/r/pullrequest/3", "", "https://dev.azure.com//_git//pullrequest/1"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		n, kind, ok := ParsePRURL(s)
		if ok && (n < 1 || n > 999999999 || (kind != GitHubKind && kind != AzureKind)) {
			t.Fatalf("ParsePRURL(%q) = %d, %q, true", s, n, kind)
		}
		if !ok && (n != 0 || kind != "") {
			t.Fatalf("ParsePRURL(%q) = %d, %q, false", s, n, kind)
		}
	})
}
