package ticker

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/theclifmeister/termilator/internal/proto"
	"github.com/theclifmeister/termilator/internal/thread"
)

func gitT(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@t", "-c", "init.defaultBranch=main"}, args...)...)
	cmd.Dir = dir
	b, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, b)
	}
	return strings.TrimSpace(string(b))
}

func commitT(t *testing.T, dir, file, body, msg string) string {
	t.Helper()
	os.WriteFile(filepath.Join(dir, file), []byte(body), 0o644)
	gitT(t, dir, "add", file)
	gitT(t, dir, "commit", "-q", "-m", msg)
	return gitT(t, dir, "rev-parse", "HEAD")
}

// gitFixture is an origin, the user's checkout of it on main ("repo")
// and another clone ("other") that moves origin's main on.
func gitFixture(t *testing.T) (repo, other string) {
	t.Helper()
	root := t.TempDir()
	origin := filepath.Join(root, "origin.git")
	repo, other = filepath.Join(root, "repo"), filepath.Join(root, "other")
	gitT(t, root, "init", "--bare", "-q", "-b", "main", origin)
	gitT(t, root, "clone", "-q", origin, repo)
	commitT(t, repo, "f", "one\n", "first")
	gitT(t, repo, "push", "-q", "origin", "main")
	gitT(t, repo, "remote", "set-head", "origin", "main")
	gitT(t, root, "clone", "-q", origin, other)
	return repo, other
}

// push commits to other's main and pushes it.
func push(t *testing.T, other, file, body, msg string) string {
	t.Helper()
	h := commitT(t, other, file, body, msg)
	gitT(t, other, "push", "-q", "origin", "main")
	return h
}

func (r *rig) journal() string {
	lines, _, _ := r.p.JournalTail(50)
	return strings.Join(lines, "\n")
}

// TestSyncCheckout: on its poll the ticker fast-forwards a clean
// checkout of main and journals it; a dirty one, or one with the
// setting off, stays put and tm context says how far behind it is.
func TestSyncCheckout(t *testing.T) {
	repo, other := gitFixture(t)
	r := newRigIn(t, repo, []string{repo})
	r.sweep(0)
	if c := Checkouts(r.tk.o.State, "demo"); len(c) != 0 {
		t.Fatalf("current checkout has notes: %v", c)
	}
	head := push(t, other, "g", "two\n", "Merge pull request #61 from a/b")
	r.sweep(time.Minute) // not due yet
	if gitT(t, repo, "rev-parse", "HEAD") == head {
		t.Fatal("synced before the poll")
	}
	r.sweep(2 * time.Minute)
	if got := gitT(t, repo, "rev-parse", "HEAD"); got != head {
		t.Fatalf("not fast-forwarded: %s", got)
	}
	if j := r.journal(); !strings.Contains(j, "ticker repo.fast-forward "+repo+" main ") {
		t.Fatalf("journal:\n%s", j)
	}

	os.WriteFile(filepath.Join(repo, "f"), []byte("mine\n"), 0o644)
	push(t, other, "g", "three\n", "more")
	r.sweep(2 * time.Minute)
	want := "local main is 1 behind origin (uncommitted changes)"
	if c := Checkouts(r.tk.o.State, "demo"); c[repo] != want {
		t.Fatalf("notes %v", c)
	}
	if s := Seen(r.tk.o.State, "demo"); s.Checkouts[repo] != want {
		t.Fatalf("seen %+v", s)
	}
	if gitT(t, repo, "rev-parse", "HEAD") != head {
		t.Fatal("moved a dirty checkout")
	}

	gitT(t, repo, "checkout", "-q", "--", "f")
	os.WriteFile(filepath.Join(os.Getenv("TERMILATOR_HOME"), "config.toml"), []byte("[projects.demo]\nfast_forward_checkout = false\n"), 0o600)
	r.sweep(2 * time.Minute)
	if c := Checkouts(r.tk.o.State, "demo"); c[repo] != "local main is 1 behind origin (fast-forward is off)" {
		t.Fatalf("notes %v", c)
	}
	if gitT(t, repo, "rev-parse", "HEAD") != head {
		t.Fatal("fast-forwarded with the setting off")
	}
}

// TestFollowMain: when main moves, a thread whose open PR is behind
// gets one prompt per main head; one that conflicts also raises a
// pr-conflict item, which says when the thread could not be prompted.
func TestFollowMain(t *testing.T) {
	repo, other := gitFixture(t)
	gitT(t, repo, "checkout", "-q", "-b", "tm/demo/t-0001-fix-it")
	prHead := commitT(t, repo, "h", "thread\n", "thread work")
	gitT(t, repo, "push", "-q", "origin", "HEAD")
	gitT(t, repo, "checkout", "-q", "main")
	r := newRigIn(t, repo, []string{repo})
	pr := fmt.Sprintf(`{"number":9,"url":"https://github.com/o/r/pull/9","state":"OPEN","reviewDecision":"","statusCheckRollup":[],"headRefOid":%q,"baseRefName":"main","mergeable":"MERGEABLE","mergeStateStatus":"CLEAN"}`, prHead)
	for range 20 {
		r.gh = append(r.gh, pr)
	}
	r.sweep(0)
	if len(r.host.prompts) != 0 {
		t.Fatalf("prompted a current PR: %q", r.host.prompts)
	}
	r.handleAll()

	main := push(t, other, "g", "two\n", "Merge pull request #61 from a/b")
	r.sweep(2 * time.Minute)
	want := fmt.Sprintf("s-2 [tm] main moved to %s (#61 merged), and your PR #9 is behind it. Merge origin/main into your branch (no rebase, no force-push)", main[:7])
	if len(r.host.prompts) != 1 || !strings.HasPrefix(r.host.prompts[0], want) {
		t.Fatalf("prompts %q", r.host.prompts)
	}
	if k := r.kinds(); k != "" {
		t.Fatalf("items for a PR that is only behind: %s", k)
	}
	r.sweep(2 * time.Minute)
	if len(r.host.prompts) != 1 {
		t.Fatalf("prompted twice for one head: %q", r.host.prompts)
	}

	main = push(t, other, "h", "main's\n", "Clash (#62)")
	r.sweep(2 * time.Minute)
	if len(r.host.prompts) != 2 || !strings.Contains(r.host.prompts[1], "main moved to "+main[:7]+" (#62 merged), and your PR #9 conflicts with it.") {
		t.Fatalf("prompts %q", r.host.prompts)
	}
	if s := r.summaries(); r.kinds() != KindPRConflict || !strings.Contains(s, "PR #9 of t-0001 (T1 Fix it) conflicts with main at "+main[:7]+"; the thread was asked to merge it") {
		t.Fatalf("items %s: %s", r.kinds(), s)
	}
	r.handleAll()

	// The thread's session is gone: no prompt, and the item says so.
	r.host.sessions = r.host.sessions[:1]
	main = push(t, other, "h", "main's again\n", "Again")
	r.sweep(2 * time.Minute)
	if len(r.host.prompts) != 2 {
		t.Fatalf("prompted a thread without a session: %q", r.host.prompts)
	}
	if s := r.summaries(); !strings.Contains(s, "conflicts with main at "+main[:7]+"; the thread was not prompted") {
		t.Fatalf("items: %s", s)
	}
}

// TestFollowMainByGitHub: without the PR's commits in the repo, GitHub's
// mergeable state decides; UNKNOWN waits for a later sweep.
func TestFollowMainByGitHub(t *testing.T) {
	repo, other := gitFixture(t)
	r := newRigIn(t, repo, []string{repo})
	missing := strings.Repeat("a", 40)
	pr := func(mergeable, state string) string {
		return fmt.Sprintf(`{"number":9,"url":"https://github.com/o/r/pull/9","state":"OPEN","statusCheckRollup":[],"headRefOid":%q,"baseRefName":"main","mergeable":%q,"mergeStateStatus":%q}`, missing, mergeable, state)
	}
	r.gh = []string{pr("UNKNOWN", "UNKNOWN"), pr("CONFLICTING", "DIRTY"), pr("CONFLICTING", "DIRTY")}
	r.sweep(0)
	push(t, other, "g", "two\n", "two")
	r.sweep(time.Minute) // a merge seen elsewhere: the poll is not due
	if len(r.host.prompts) != 0 {
		t.Fatalf("prompts %q", r.host.prompts)
	}
	r.sweep(2 * time.Minute)
	if len(r.host.prompts) != 1 || !strings.Contains(r.host.prompts[0], "conflicts with it") {
		t.Fatalf("prompts %q", r.host.prompts)
	}
}

// TestFollowMainOwnMerge: main moving to the merge of a thread's own PR
// prompts nobody but the threads whose PRs it left behind, even while
// gh still calls the merged PR open.
func TestFollowMainOwnMerge(t *testing.T) {
	repo, other := gitFixture(t)
	heads := map[string]string{}
	for _, b := range []struct{ branch, file string }{{"tm/demo/t-0001-fix-it", "h"}, {"tm/demo/t-0002-other", "i"}} {
		gitT(t, repo, "checkout", "-q", "-b", b.branch, "main")
		heads[b.branch] = commitT(t, repo, b.file, "thread\n", "thread work")
		gitT(t, repo, "push", "-q", "origin", "HEAD")
	}
	gitT(t, repo, "checkout", "-q", "main")
	r := newRigIn(t, repo, []string{repo})
	if _, err := thread.Create(r.p, thread.Record{Title: "Other", Task: "T2", Agent: "claude", Repo: repo,
		Branch: "tm/demo/t-0002-other", State: thread.Running, Session: "s-3"}); err != nil {
		t.Fatal(err)
	}
	r.host.sessions = append(r.host.sessions, proto.SessionInfo{ID: "s-3", Role: proto.RoleThread, Project: "demo", Thread: "t-0002", State: "idle"})
	num := map[string]int{"tm/demo/t-0001-fix-it": 9, "tm/demo/t-0002-other": 10}
	r.ghFor = func(target string) string {
		// GitHub hasn't caught up: #9 still reads open after its merge.
		return fmt.Sprintf(`{"number":%d,"url":"https://github.com/o/r/pull/%d","state":"OPEN","statusCheckRollup":[],"headRefOid":%q,"baseRefName":"main","mergeable":"MERGEABLE","mergeStateStatus":"CLEAN"}`, num[target], num[target], heads[target])
	}
	r.sweep(0)
	r.handleAll()

	gitT(t, other, "fetch", "-q", "origin")
	gitT(t, other, "-c", "user.name=t", "-c", "user.email=t@t", "merge", "-q", "--no-ff", "-m", "Merge pull request #9 from o/tm/demo/t-0001-fix-it", "origin/tm/demo/t-0001-fix-it")
	gitT(t, other, "push", "-q", "origin", "main")
	r.sweep(2 * time.Minute)
	if len(r.host.prompts) != 1 || !strings.HasPrefix(r.host.prompts[0], "s-3 [tm] main moved to ") || !strings.Contains(r.host.prompts[0], "(#9 merged), and your PR #10 is behind it.") {
		t.Fatalf("prompts %q", r.host.prompts)
	}
}

// TestFollowMainRecheck: a PR the last poll called behind is asked
// about again before the prompt; merged by then, it gets none.
func TestFollowMainRecheck(t *testing.T) {
	repo, other := gitFixture(t)
	r := newRigIn(t, repo, []string{repo})
	missing := strings.Repeat("a", 40)
	pr := func(state, mergeState string) string {
		return fmt.Sprintf(`{"number":9,"url":"https://github.com/o/r/pull/9","state":%q,"statusCheckRollup":[],"headRefOid":%q,"baseRefName":"main","mergeable":"MERGEABLE","mergeStateStatus":%q}`, state, missing, mergeState)
	}
	r.gh = []string{pr("OPEN", "CLEAN"), pr("OPEN", "BEHIND"), pr("MERGED", "UNKNOWN")}
	r.sweep(0)
	r.handleAll()
	push(t, other, "g", "two\n", "Merge pull request #9 from a/b")
	r.sweep(2 * time.Minute)
	if len(r.host.prompts) != 0 {
		t.Fatalf("prompted a merged PR: %q", r.host.prompts)
	}
	if r.ghN != 3 {
		t.Fatalf("asked gh %d times", r.ghN)
	}
	if k := r.kinds(); k != KindPRMerged {
		t.Fatalf("items %s", k)
	}
}
