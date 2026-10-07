package worktree

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func out(t *testing.T, dir string, args ...string) string {
	t.Helper()
	b, err := exec.Command("git", append([]string{"-C", dir}, args...)...).Output()
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return strings.TrimSpace(string(b))
}

func commit(t *testing.T, dir, file, body, msg string) {
	t.Helper()
	os.WriteFile(filepath.Join(dir, file), []byte(body), 0o644)
	run(t, dir, "add", file)
	run(t, dir, "commit", "-q", "-m", msg)
}

// fixture is an origin with one commit, the user's checkout of it
// ("repo", on main) and a second clone ("other") that moves origin on.
func fixture(t *testing.T) (repo, other string) {
	t.Helper()
	root := t.TempDir()
	origin := filepath.Join(root, "origin.git")
	repo, other = filepath.Join(root, "repo"), filepath.Join(root, "other")
	run(t, root, "init", "--bare", "-q", "-b", "main", origin)
	run(t, root, "clone", "-q", origin, repo)
	commit(t, repo, "f", "one\n", "first")
	run(t, repo, "push", "-q", "origin", "main")
	run(t, repo, "remote", "set-head", "origin", "main")
	run(t, repo, "branch", "-q", "--set-upstream-to", "origin/main")
	run(t, root, "clone", "-q", origin, other)
	return repo, other
}

// advance moves origin's main on by n commits.
func advance(t *testing.T, other string, n int) {
	t.Helper()
	for i := range n {
		commit(t, other, "g", strings.Repeat("x", i+1), "Merge pull request #4"+string(rune('0'+i))+" from someone/branch")
	}
	run(t, other, "push", "-q", "origin", "main")
}

func TestSyncCleanFastForward(t *testing.T) {
	repo, other := fixture(t)
	if c, err := Sync(repo, true); err != nil || c.Behind != 0 || c.Note() != "" || c.From != "" {
		t.Fatalf("current: %+v %v", c, err)
	}
	advance(t, other, 3)
	os.WriteFile(filepath.Join(repo, "untracked"), []byte("mine"), 0o644) // not in the way
	before := out(t, repo, "rev-parse", "HEAD")
	c, err := Sync(repo, true)
	if err != nil {
		t.Fatal(err)
	}
	head := out(t, other, "rev-parse", "HEAD")
	if c.Branch != "main" || c.Behind != 0 || c.From != before || c.To != head || c.Origin != head || c.Held != "" {
		t.Fatalf("got %+v", c)
	}
	if got := out(t, repo, "rev-parse", "HEAD"); got != head {
		t.Fatalf("HEAD %s, want %s", got, head)
	}
	if b, _ := os.ReadFile(filepath.Join(repo, "untracked")); string(b) != "mine" {
		t.Fatal("untracked file touched")
	}
	if n := MergedPR(repo, c.Origin); n != 42 {
		t.Fatalf("merged PR %d", n)
	}
}

func TestSyncDirtyTree(t *testing.T) {
	repo, other := fixture(t)
	advance(t, other, 2)
	os.WriteFile(filepath.Join(repo, "f"), []byte("changed\n"), 0o644)
	before := out(t, repo, "rev-parse", "HEAD")
	c, err := Sync(repo, true)
	if err != nil || c.Behind != 2 || c.Held != HeldDirty || c.From != "" {
		t.Fatalf("got %+v %v", c, err)
	}
	if c.Note() != "local main is 2 behind origin (uncommitted changes)" {
		t.Fatalf("note %q", c.Note())
	}
	if out(t, repo, "rev-parse", "HEAD") != before {
		t.Fatal("moved a dirty checkout")
	}
	// Staged counts too.
	run(t, repo, "add", "f")
	if c, _ := Sync(repo, true); c.Held != HeldDirty {
		t.Fatalf("staged: %+v", c)
	}
}

func TestSyncUntrackedInTheWay(t *testing.T) {
	repo, other := fixture(t)
	advance(t, other, 1) // adds g
	os.WriteFile(filepath.Join(repo, "g"), []byte("mine"), 0o644)
	c, err := Sync(repo, true)
	if err == nil || c.Held != HeldRefused || c.Behind != 1 {
		t.Fatalf("got %+v %v", c, err)
	}
	if b, _ := os.ReadFile(filepath.Join(repo, "g")); string(b) != "mine" {
		t.Fatal("untracked file overwritten")
	}
}

func TestSyncDiverged(t *testing.T) {
	repo, other := fixture(t)
	advance(t, other, 2)
	commit(t, repo, "local", "l", "local work")
	before := out(t, repo, "rev-parse", "HEAD")
	c, err := Sync(repo, true)
	if err != nil || c.Behind != 2 || c.Ahead != 1 || c.Held != HeldDiverged {
		t.Fatalf("got %+v %v", c, err)
	}
	if out(t, repo, "rev-parse", "HEAD") != before {
		t.Fatal("moved a diverged branch")
	}
}

func TestSyncOtherBranchCheckedOut(t *testing.T) {
	repo, other := fixture(t)
	advance(t, other, 2)
	run(t, repo, "checkout", "-q", "-b", "feature")
	main := out(t, repo, "rev-parse", "main")
	c, err := Sync(repo, true)
	if err != nil || c.Behind != 2 || c.Held != HeldOther {
		t.Fatalf("got %+v %v", c, err)
	}
	if out(t, repo, "rev-parse", "main") != main || out(t, repo, "symbolic-ref", "--short", "HEAD") != "feature" {
		t.Fatal("touched another branch")
	}
	// Detached HEAD is another branch too.
	run(t, repo, "checkout", "-q", "--detach", "main")
	if c, _ := Sync(repo, true); c.Held != HeldOther {
		t.Fatalf("detached: %+v", c)
	}
}

func TestSyncOff(t *testing.T) {
	repo, other := fixture(t)
	advance(t, other, 1)
	before := out(t, repo, "rev-parse", "HEAD")
	c, err := Sync(repo, false)
	if err != nil || c.Note() != "local main is 1 behind origin (fast-forward is off)" {
		t.Fatalf("got %+v %v", c, err)
	}
	if out(t, repo, "rev-parse", "HEAD") != before {
		t.Fatal("fast-forwarded with the setting off")
	}
}

func TestSyncNoOrigin(t *testing.T) {
	repo := t.TempDir()
	run(t, repo, "init", "-q")
	if c, err := Sync(repo, true); err != nil || c != (Checkout{}) {
		t.Fatalf("got %+v %v", c, err)
	}
}

// TestHeadState: a head that has base is current, one without it is
// behind, and one whose change clashes with base's conflicts.
func TestHeadState(t *testing.T) {
	repo, other := fixture(t)
	run(t, repo, "checkout", "-q", "-b", "tm/x")
	commit(t, repo, "h", "thread\n", "thread work")
	head := out(t, repo, "rev-parse", "HEAD")
	base := out(t, repo, "rev-parse", "origin/main")
	if s, ok := HeadState(repo, head, base); !ok || s != "" {
		t.Fatalf("current: %q %v", s, ok)
	}
	advance(t, other, 1)
	run(t, repo, "fetch", "-q", "origin")
	base = out(t, repo, "rev-parse", "origin/main")
	if s, ok := HeadState(repo, head, base); !ok || s != "behind" {
		t.Fatalf("behind: %q %v", s, ok)
	}
	commit(t, other, "h", "main's\n", "clash")
	run(t, other, "push", "-q", "origin", "main")
	run(t, repo, "fetch", "-q", "origin")
	base = out(t, repo, "rev-parse", "origin/main")
	if s, ok := HeadState(repo, head, base); !ok || s != "conflict" {
		t.Fatalf("conflict: %q %v", s, ok)
	}
	if Contains(repo, base, head) || !Contains(repo, head, out(t, repo, "rev-parse", "HEAD~1")) {
		t.Fatal("Contains: main lacks the head, the head has its parent")
	}
	if Contains(repo, base, strings.Repeat("a", 40)) || Contains(repo, "HEAD", head) || Contains(repo, base, "--help") {
		t.Fatal("Contains judged no commit id")
	}
	if _, ok := HeadState(repo, strings.Repeat("a", 40), base); ok {
		t.Fatal("a missing commit was judged")
	}
	if _, ok := HeadState(repo, "--help", base); ok {
		t.Fatal("not a commit id")
	}
}

func TestMergedPR(t *testing.T) {
	repo, _ := fixture(t)
	for msg, want := range map[string]int{
		"Merge pull request #61 from a/b": 61,
		"Fix the thing (#7)":              7,
		"Plain commit":                    0,
		"Mentions #9 somewhere":           0,
	} {
		commit(t, repo, "m", msg, msg)
		if n := MergedPR(repo, out(t, repo, "rev-parse", "HEAD")); n != want {
			t.Errorf("%q: %d, want %d", msg, n, want)
		}
	}
}
