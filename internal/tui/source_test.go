package tui

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/theclifmeister/termilator/internal/ticker"
)

// TestShipped: a task's PR is open, merged but in no tag yet, or
// released, from the repo's history; without the merge in it, the
// ticker's last look decides.
func TestShipped(t *testing.T) {
	repo := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@t", "-c", "init.defaultBranch=main"}, args...)...)
		cmd.Dir = repo
		if b, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, b)
		}
	}
	git("init", "-q")
	os.WriteFile(filepath.Join(repo, "a"), []byte("a"), 0o644)
	git("add", ".")
	git("commit", "-q", "-m", "a")
	git("tag", "v0.1.0")
	git("checkout", "-q", "-b", "pr")
	os.WriteFile(filepath.Join(repo, "b"), []byte("b"), 0o644)
	git("add", ".")
	git("commit", "-q", "-m", "b")
	git("checkout", "-q", "main")
	git("merge", "-q", "--no-ff", "-m", "Merge pull request #61 from o/pr", "pr")

	check := func(n int, pr ticker.PR, ship, tag string) {
		t.Helper()
		if s, g := shipped(repo, n, pr); s != ship || g != tag {
			t.Errorf("shipped(#%d, %+v) = %q %q, want %q %q", n, pr, s, g, ship, tag)
		}
	}
	check(61, ticker.PR{}, ShipUnreleased, "")
	check(62, ticker.PR{State: "OPEN"}, ShipOpen, "")
	check(62, ticker.PR{State: "CLOSED"}, ShipClosed, "")
	check(62, ticker.PR{State: "MERGED"}, ShipMerged, "") // not fetched: can't tell the release
	check(62, ticker.PR{}, "", "")
	check(0, ticker.PR{State: "MERGED"}, "", "")
	git("tag", "v0.2.0")
	check(61, ticker.PR{State: "MERGED"}, ShipReleased, "v0.2.0")
	if s, _ := shipped("", 61, ticker.PR{State: "MERGED"}); s != ShipMerged {
		t.Errorf("no repo: %q", s)
	}
	for url, want := range map[string]int{"https://github.com/o/r/pull/61": 61, "": 0, "https://github.com/o/r/pull/x": 0} {
		if got := prNumber(url); got != want {
			t.Errorf("prNumber(%q) = %d", url, got)
		}
	}
}
