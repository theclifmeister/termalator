package e2e

// T40: tm thread adopt (docs/SPEC.md §9, Adopt). An agent session the
// user started outside the projects becomes a thread: its tm calls are
// the thread's, its todos and report reach the project, and resolve
// treats it as a started thread, keeping a repository's own checkout.

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
)

const adoptScript = `
[[step]]
do = "todo_create"
subject = "alpha"
description = "first"
active_form = "Doing alpha"

[[step]]
do = "run"
cmd = '"$TERMILATOR_BIN" task status T1 review > "$OUT/adopt-taskstatus" 2>&1; echo "exit $?" >> "$OUT/adopt-taskstatus"'

[[step]]
do = "run"
cmd = 'printf "## Report\nAdopted and done.\n\n## Next\nReview it\n" | "$TERMILATOR_BIN" report > "$OUT/adopt-report" 2>&1; echo "exit $?" >> "$OUT/adopt-report"'

[[step]]
do = "run"
cmd = '"$TERMILATOR_BIN" done > "$OUT/adopt-done" 2>&1; echo "exit $?" >> "$OUT/adopt-done"'
`

func TestThreadAdopt(t *testing.T) {
	env, projDir, out := threadEnv(t)
	env.scripts(map[string]string{"adopted-work": adoptScript})
	repo := firstRepo(t, projDir)
	wt := filepath.Join(t.TempDir(), "by-hand")
	if b, err := exec.Command("git", "-C", repo, "worktree", "add", "-q", "-b", "fix-login", wt).CombinedOutput(); err != nil {
		t.Fatalf("worktree add: %v\n%s", err, b)
	}
	wt, _ = filepath.EvalSymlinks(wt)
	env.Trust(wt, repo)
	env.MustCLI("task", "add", "Fix the login", "--status", "ready", "--project", "demo")

	// What can't be adopted.
	shell := env.Start("shell")
	if r := env.CLI("thread", "adopt", shell.ID, "--project", "demo"); r.Code != 1 || !strings.Contains(r.Stderr+r.Stdout, "no agent runs") {
		t.Fatalf("adopt a shell: %+v", r)
	}
	if r := env.CLI("thread", "adopt", "s-99", "--project", "demo"); r.Code != 1 {
		t.Fatalf("adopt a missing session: %+v", r)
	}

	s := env.StartAgent("claude", wt)
	env.WaitState(s, "idle", agentWait)
	got := env.MustCLI("thread", "adopt", s.ID, "--task", "T1", "--project", "demo")
	if !strings.Contains(got, "adopted session "+s.ID+" as t-0001 in "+wt+" on fix-login") {
		t.Fatalf("adopt: %q", got)
	}
	if r := env.CLI("thread", "adopt", s.ID, "--project", "demo"); r.Code != 1 || !strings.Contains(r.Stderr+r.Stdout, "in-project") {
		t.Fatalf("adopt twice: %+v", r)
	}

	tdir := filepath.Join(projDir, "threads", "t-0001")
	var rec struct {
		Title, Task, Repo, Branch, Worktree, Session, State string
		Adopted, Checkout                                   bool
	}
	readTOML(t, filepath.Join(tdir, "thread.toml"), &rec)
	if rec.Title != "Fix the login" || rec.Task != "T1" || rec.Repo != repo || rec.Branch != "fix-login" || rec.Worktree != wt ||
		rec.Session != s.ID || rec.State != "running" || !rec.Adopted || rec.Checkout {
		t.Fatalf("record %+v (repo %s)", rec, repo)
	}
	info, _ := env.Info(s)
	if info.Role != "thread" || info.Project != "demo" || info.Thread != "t-0001" {
		t.Fatalf("session after adopt: %+v", info)
	}
	if task := env.MustCLI("task", "show", "T1", "--project", "demo"); !strings.Contains(task, "status: started") || !strings.Contains(task, "thread: t-0001") {
		t.Errorf("task:\n%s", task)
	}
	// It is told it is a thread now, with its brief.
	env.WaitFake("prompt", agentWait, func(r FakeRecord) bool {
		return strings.Contains(r.Str("text"), "You are now thread t-0001") && strings.Contains(r.Str("text"), filepath.Join(tdir, "brief.md"))
	})

	// Its tm calls are the thread's.
	env.WaitState(s, "idle", agentWait)
	env.MustCLI("thread", "prompt", "t-0001", "run adopted-work", "--project", "demo")
	if got := readOut(t, out, "adopt-taskstatus"); !strings.Contains(got, "coordinator-only") {
		t.Errorf("adopted thread set a task status: %q", got)
	}
	if got := readOut(t, out, "adopt-report"); !strings.Contains(got, "stored report 1 for t-0001") {
		t.Errorf("report: %q", got)
	}
	if got := readOut(t, out, "adopt-done"); !strings.Contains(got, "exit 0") {
		t.Errorf("done: %q", got)
	}
	Poll(agentWait, func() bool {
		b, _ := os.ReadFile(filepath.Join(tdir, "STATUS.md"))
		return strings.Contains(string(b), "alpha")
	})
	var list []struct {
		ID, Report string
		Done       bool
		Adopted    bool
	}
	json.Unmarshal([]byte(env.MustCLI("thread", "list", "--json", "--project", "demo")), &list)
	if len(list) != 1 || list[0].Report != "new" || !list[0].Done || !list[0].Adopted {
		t.Fatalf("thread list: %+v", list)
	}

	// A server restart resumes it as the thread's.
	env.MustCLI("server", "restart", "--yes")
	if !Poll(agentWait, func() bool {
		info, ok := env.Info(s)
		return ok && info.Role == "thread" && info.Thread == "t-0001" && info.Agent == "claude"
	}) {
		t.Fatalf("after a server restart: %+v", env.Sessions())
	}

	// Resolve as for a started thread: the clean worktree goes, and the
	// branch, whose commits are all on main.
	res := env.MustCLI("thread", "resolve", "t-0001", "--project", "demo")
	if !strings.Contains(res, "removed worktree "+wt) || !strings.Contains(res, "deleted branch fix-login (merged into origin/main)") {
		t.Fatalf("resolve: %q", res)
	}

	// Adopted in the repository's own checkout: resolve keeps it.
	s2 := env.StartAgent("claude", repo)
	env.WaitState(s2, "idle", agentWait)
	env.MustCLI("thread", "adopt", s2.ID, "--title", "In the checkout", "--project", "demo")
	var rec2 struct {
		Worktree string
		Checkout bool
	}
	readTOML(t, filepath.Join(projDir, "threads", "t-0002", "thread.toml"), &rec2)
	if !rec2.Checkout || rec2.Worktree != repo {
		t.Fatalf("checkout record %+v", rec2)
	}
	res = env.MustCLI("thread", "resolve", "t-0002", "--project", "demo")
	if !strings.Contains(res, "kept checkout "+repo) {
		t.Fatalf("resolve checkout: %q", res)
	}
	if _, err := os.Stat(filepath.Join(repo, "README")); err != nil {
		t.Fatalf("checkout gone: %v", err)
	}
	journal, _ := os.ReadFile(filepath.Join(projDir, "JOURNAL.md"))
	if !strings.Contains(string(journal), "human thread.adopt t-0001 T1 Fix the login (session "+s.ID+")") {
		t.Errorf("journal:\n%s", journal)
	}
}

// firstRepo is the project's first repository, as PROJECT.md lists it.
func firstRepo(t *testing.T, projDir string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(projDir, "PROJECT.md"))
	if err != nil {
		t.Fatal(err)
	}
	_, front, _ := strings.Cut(string(b), "+++\n")
	front, _, _ = strings.Cut(front, "+++")
	var meta struct{ Repos []string }
	if _, err := toml.Decode(front, &meta); err != nil || len(meta.Repos) == 0 {
		t.Fatalf("PROJECT.md repos: %v %+v", err, meta)
	}
	r, _ := filepath.EvalSymlinks(meta.Repos[0])
	return r
}
