package tui

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/theclifmeister/terminatr/internal/ticker"
)

// TestShipped: a task's PR is merged when the repo's history has its
// merge; without it, the ticker's last look decides.
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
	git("checkout", "-q", "-b", "pr")
	os.WriteFile(filepath.Join(repo, "b"), []byte("b"), 0o644)
	git("add", ".")
	git("commit", "-q", "-m", "b")
	git("checkout", "-q", "main")
	git("merge", "-q", "--no-ff", "-m", "Merge pull request #61 from o/pr", "pr")

	check := func(n int, pr ticker.PR, ship string) {
		t.Helper()
		if s := shipped(repo, n, pr); s != ship {
			t.Errorf("shipped(#%d, %+v) = %q, want %q", n, pr, s, ship)
		}
	}
	check(61, ticker.PR{}, ShipMerged)
	check(61, ticker.PR{State: "OPEN"}, ShipMerged) // the merge is in the history
	check(62, ticker.PR{State: "OPEN"}, ShipOpen)
	check(62, ticker.PR{State: "CLOSED"}, ShipClosed)
	check(62, ticker.PR{State: "MERGED"}, ShipMerged) // not fetched
	check(62, ticker.PR{}, "")
	check(0, ticker.PR{State: "MERGED"}, "")
	if s := shipped("", 61, ticker.PR{State: "MERGED"}); s != ShipMerged {
		t.Errorf("no repo: %q", s)
	}
	for url, want := range map[string]int{"https://github.com/o/r/pull/61": 61, "": 0, "https://github.com/o/r/pull/x": 0} {
		if got := ticker.PRNumber(url); got != want {
			t.Errorf("PRNumber(%q) = %d", url, got)
		}
	}
}
