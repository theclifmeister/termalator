package codehost

import (
	"fmt"
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
