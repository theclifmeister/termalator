package codehost

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestChecksConcurrent: the repos are checked at once, never more than
// cap(azSlots) of them, and the lines keep the order of the repos.
func TestChecksConcurrent(t *testing.T) {
	var hosts []RepoHost
	for i := range 2 * cap(azSlots) {
		hosts = append(hosts, RepoHost{Repo: fmt.Sprintf("/r/%d", i),
			Target: Target{Kind: AzureKind, OrgURL: "https://dev.azure.com/acme", Project: "Shop", Repo: fmt.Sprintf("web%d", i)}})
	}
	var mu sync.Mutex
	in, most := 0, 0
	d := DoctorDeps{
		LookPath: func(string) (string, error) { return "/bin/az", nil },
		Run: func(dir, name string, args ...string) (string, error) {
			if name != "git" {
				return `{"name":"web"}`, nil
			}
			mu.Lock()
			in++
			most = max(most, in)
			mu.Unlock()
			time.Sleep(20 * time.Millisecond)
			mu.Lock()
			in--
			mu.Unlock()
			return "", nil
		},
	}
	cs := Checks(d, hosts)
	if most < 2 || most > cap(azSlots) {
		t.Errorf("%d repos at once, want 2..%d", most, cap(azSlots))
	}
	var names []string
	for _, c := range cs {
		names = append(names, c.Name)
	}
	want := []string{"az", "az login"}
	for i := range hosts {
		want = append(want, fmt.Sprintf("az repo Shop/web%d", i), fmt.Sprintf("git origin Shop/web%d", i))
	}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Errorf("order:\n%s\nwant\n%s", strings.Join(names, ","), strings.Join(want, ","))
	}
}

// TestGitTimeout: a git that hangs is given up on after gitTimeout.
func TestGitTimeout(t *testing.T) {
	defer func(d time.Duration) { gitTimeout = d }(gitTimeout)
	gitTimeout = 100 * time.Millisecond
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte("#!/bin/sh\nexec /bin/sleep 30\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	start := time.Now()
	if _, err := git(t.TempDir(), "log"); err == nil {
		t.Fatal("no error")
	}
	if took := time.Since(start); took > 5*time.Second {
		t.Fatalf("took %s", took)
	}
}

// TestChecksNoHost: repos with no PR host add no gh line (so doctor can't
// warn about a gh nothing uses), but a fresh install, with no repos, still
// gets one.
func TestChecksNoHost(t *testing.T) {
	d := DoctorDeps{
		LookPath: func(string) (string, error) { return "", errors.New("not found") },
		Run:      func(dir, name string, args ...string) (string, error) { return "", nil },
	}
	for _, c := range Checks(d, []RepoHost{{Repo: "/r", Target: Target{Kind: NoKind}}}) {
		if c.Name == "gh" || c.Name == "gh auth" {
			t.Errorf("gh checked for a repo with no PR host: %+v", c)
		}
	}
	var gh bool
	for _, c := range Checks(d, nil) {
		gh = gh || c.Name == "gh"
	}
	if !gh {
		t.Error("no gh line on a fresh install")
	}
}
