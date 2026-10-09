package e2e

// M6 scenarios: threads (docs/SPEC.md §15 M6). The fake agent plays both
// the coordinator and the thread under the real claude.toml; their tm
// calls go through the server, which tells them apart by process tree.

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
)

// threadScripts are the fake agent's scripts for these scenarios. $OUT is
// a folder outside the project and the worktree where run steps leave
// exit codes and output for the test.
var threadScripts = map[string]string{
	"coord-delegate": `
[[step]]
do = "run"
cmd = '"$TERMINATR_BIN" task delegate T1 > "$OUT/delegate1" 2>&1; echo "exit $?" >> "$OUT/delegate1"'

[[step]]
do = "run"
cmd = '"$TERMINATR_BIN" task delegate T1 --approved-by-user > "$OUT/delegate2" 2>&1; echo "exit $?" >> "$OUT/delegate2"'

[[step]]
do = "run"
cmd = '"$TERMINATR_BIN" thread prompt t-0001 "run thread-work" > "$OUT/prompt" 2>&1; echo "exit $?" >> "$OUT/prompt"'

[[step]]
do = "stream"
ms = 50
text = "COORD-DONE"
`,
	"coord-path": `
[[step]]
do = "run"
cmd = 'echo "$(command -v tm) $TERMINATR_BIN" > "$OUT/path"; echo "exit $?" >> "$OUT/path"'

[[step]]
do = "stream"
ms = 50
text = "PATH-DONE"
`,
	"coord-agents": `
[[step]]
do = "run"
cmd = '"$TERMINATR_BIN" context > "$OUT/context" 2>&1'

[[step]]
do = "run"
cmd = '"$TERMINATR_BIN" task delegate T1 --approved-by-user > "$OUT/delegate" 2>&1; echo "exit $?" >> "$OUT/delegate"'

[[step]]
do = "run"
cmd = '"$TERMINATR_BIN" thread start "By hand" --agent claude --approved-by-user > "$OUT/override" 2>&1; echo "exit $?" >> "$OUT/override"'

[[step]]
do = "stream"
ms = 50
text = "COORD-DONE"
`,
	"thread-work": `
[[step]]
do = "run"
cmd = '"$TERMINATR_BIN" done > "$OUT/done1" 2>&1; echo "exit $?" >> "$OUT/done1"'

[[step]]
do = "run"
cmd = '"$TERMINATR_BIN" task steps T1 add "Write the code" && "$TERMINATR_BIN" task steps T1 add "Test it"'

[[step]]
do = "todo_create"
subject = "alpha"
description = "first"
active_form = "Doing alpha"

[[step]]
do = "todo_create"
subject = "beta"
description = "second"
active_form = "Doing beta"

[[step]]
do = "todo_update"
id = "1"
status = "completed"

[[step]]
do = "run"
cmd = '"$TERMINATR_BIN" task steps T1 check 1 && cp "$TERMINATR_HOME/projects/demo/threads/t-0001/STATUS.md" "$OUT/status-mid"'

[[step]]
do = "run"
cmd = '"$TERMINATR_BIN" task status T1 review > "$OUT/taskstatus" 2>&1; echo "exit $?" >> "$OUT/taskstatus"'

[[step]]
do = "write"
path = "$FAKEAGENT_DENY_PATH/TASKS.md"
text = "overwritten\n"

[[step]]
do = "run"
cmd = 'printf "## Report\nSome work.\n" | "$TERMINATR_BIN" report > "$OUT/badreport" 2>&1; echo "exit $?" >> "$OUT/badreport"'

[[step]]
do = "run"
cmd = 'printf "notes\n" > "$OUT/notes.md"; printf "PR: https://github.com/o/r/pull/7\n\n## Report\nDid it.\n\n## Next\nMerge the PR\n\n## Remember\n- a lesson\n" | "$TERMINATR_BIN" report --attach "$OUT/notes.md" > "$OUT/report" 2>&1; echo "exit $?" >> "$OUT/report"'

[[step]]
do = "run"
cmd = '"$TERMINATR_BIN" done "all good" > "$OUT/done2" 2>&1; echo "exit $?" >> "$OUT/done2"'

[[step]]
do = "stream"
ms = 50
text = "THREAD-DONE"
`,
}

// threadEnv sets up a project with a git repo (a clone of a bare origin)
// and the fake agent, before the server starts.
func threadEnv(t *testing.T) (env *Env, projDir, out string) {
	env = New(t)
	env.FakeClaude()
	scripts := t.TempDir()
	for name, s := range threadScripts {
		os.WriteFile(filepath.Join(scripts, name+".toml"), []byte(s), 0o644)
	}
	env.Setenv("FAKEAGENT_SCRIPTS", scripts)
	out = t.TempDir()
	env.Setenv("OUT", out)

	root := t.TempDir()
	origin, repo := filepath.Join(root, "origin.git"), filepath.Join(root, "repo")
	git := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@t", "-c", "init.defaultBranch=main"}, args...)...)
		cmd.Dir = dir
		if b, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, b)
		}
	}
	git(root, "init", "--bare", "-q", origin)
	git(root, "clone", "-q", origin, repo)
	os.WriteFile(filepath.Join(repo, "README"), []byte("hi\n"), 0o644)
	git(repo, "add", ".")
	git(repo, "commit", "-q", "-m", "first")
	git(repo, "push", "-q", "origin", "HEAD:main")
	git(repo, "remote", "set-head", "origin", "main")

	var p struct{ Slug, Dir string }
	if err := json.Unmarshal([]byte(env.MustCLI("project", "new", "demo", "--repo", repo, "--json")), &p); err != nil {
		t.Fatal(err)
	}
	real, _ := filepath.EvalSymlinks(p.Dir)
	env.Setenv("FAKEAGENT_DENY_PATH", real)
	// Not the worktrees: the server trusts each thread's own (§8.6).
	env.Trust(p.Dir)
	return env, p.Dir, out
}

// TestAgentSettings: with a second agent installed, the settings pick
// the agent a new coordinator and a new thread run (docs/SPEC.md §11.2):
// the coordinator runs all projects' coordinator_agent, a delegated
// thread the project's thread_agent, and --agent overrides it; tm
// context names them.
func TestAgentSettings(t *testing.T) {
	env, _, out := threadEnv(t)
	env.FakeAgent("other")
	writeConfig(t, env, "[defaults]\ncoordinator_agent = \"other\"\n\n[projects.demo]\nthread_agent = \"other\"\n")
	env.MustCLI("task", "add", "Fix the login", "--status", "ready", "--project", "demo")

	coord := &Session{ID: strings.TrimSpace(env.MustCLI("project", "open", "demo"))}
	info, _ := env.Info(coord)
	coord.PID = info.PID
	env.track(info.PID, "coordinator "+coord.ID)
	if info.Agent != "other" {
		t.Fatalf("coordinator runs %q", info.Agent)
	}
	env.WaitState(coord, "idle", agentWait)
	env.Prompt(coord, "run coord-agents")
	if got := readOut(t, out, "context"); !strings.Contains(got, "Thread agent: other (config.toml; the human's): tm thread start runs it; --agent may name another: claude") {
		t.Errorf("context:\n%s", got)
	}
	if got := readOut(t, out, "delegate"); !strings.Contains(got, "exit 0") {
		t.Fatalf("delegate: %q", got)
	}
	if got := readOut(t, out, "override"); !strings.Contains(got, "exit 0") {
		t.Fatalf("thread start --agent claude: %q", got)
	}
	ran := map[string]string{}
	for _, s := range env.Sessions() {
		if s.Thread != "" {
			ran[s.Thread] = s.Agent
		}
	}
	if ran["t-0001"] != "other" || ran["t-0002"] != "claude" {
		t.Fatalf("threads ran %v", ran)
	}
}

// TestSessionPathHasServerTm: a session's `tm` is the server's own binary
// (docs/SPEC.md §3.4), whatever else PATH holds.
func TestSessionPathHasServerTm(t *testing.T) {
	env, _, out := threadEnv(t)
	coord := &Session{ID: strings.TrimSpace(env.MustCLI("project", "open", "demo"))}
	info, _ := env.Info(coord)
	coord.PID = info.PID
	env.track(info.PID, "coordinator "+coord.ID)
	env.WaitState(coord, "idle", agentWait)
	env.Prompt(coord, "run coord-path")
	f := strings.Fields(readOut(t, out, "path"))
	if len(f) < 2 || f[0] != f[1] || filepath.Base(f[0]) != "tm" {
		t.Fatalf("command -v tm vs TERMINATR_BIN: %v", f)
	}
}

func readTOML(t *testing.T, path string, v any) {
	t.Helper()
	if _, err := toml.DecodeFile(path, v); err != nil {
		t.Fatal(err)
	}
}

func readOut(t *testing.T, out, name string) string {
	t.Helper()
	var b []byte
	Poll(agentWait, func() bool {
		var err error
		b, err = os.ReadFile(filepath.Join(out, name))
		return err == nil && strings.Contains(string(b), "exit ")
	})
	return string(b)
}

// TestSmokeThreadLifecycle is M6's "Try it": the coordinator delegates a
// task, the thread writes its plan as steps, works through todos and
// steps, is refused the project folder and task status, hands in a
// report and calls done; its worktree is removed by hand and nothing is
// lost; resolve cleans up.
func TestSmokeThreadLifecycle(t *testing.T) {
	env, projDir, out := threadEnv(t)
	env.MustCLI("task", "add", "Fix the login", "--status", "ready", "--project", "demo")

	coord := env.StartAgent("claude", projDir, "--role", "coordinator", "--project", "demo")
	env.WaitState(coord, "idle", agentWait)
	env.Prompt(coord, "run coord-delegate")

	if got := readOut(t, out, "delegate1"); !strings.Contains(got, "needs-approval") || !strings.Contains(got, "exit 1") {
		t.Fatalf("delegate without approval: %q", got)
	}
	if got := readOut(t, out, "delegate2"); !strings.Contains(got, "started t-0001") || !strings.Contains(got, "exit 0") {
		t.Fatalf("delegate: %q", got)
	}
	if got := readOut(t, out, "prompt"); !strings.Contains(got, "exit 0") {
		t.Fatalf("thread prompt: %q", got)
	}

	tdir := filepath.Join(projDir, "threads", "t-0001")
	var rec struct{ Worktree, Branch, Session string }
	readTOML(t, filepath.Join(tdir, "thread.toml"), &rec)
	if !strings.HasPrefix(rec.Worktree, filepath.Join(env.Home, "worktrees", "demo", "t-0001-fix-the-login")) || rec.Branch != "tm/demo/t-0001-fix-the-login" {
		t.Fatalf("record %+v", rec)
	}

	// The brief has absolute paths, and reaches the thread at start.
	brief, _ := os.ReadFile(filepath.Join(tdir, "brief.md"))
	for _, w := range []string{projDir + "/: PROJECT.md", "TASKS.md", rec.Worktree, "tm skill thread", "# T1 Fix the login"} {
		if !strings.Contains(string(brief), w) {
			t.Errorf("brief lacks %q:\n%s", w, brief)
		}
	}
	env.WaitFake("prompt", agentWait, func(r FakeRecord) bool {
		return r.Str("via") == "kickoff" && strings.Contains(r.Str("text"), filepath.Join(tdir, "brief.md"))
	})
	// It got there without a trust screen: the server trusted the
	// worktree, and only it, in the agent's config.
	var claudeCfg struct {
		Projects map[string]struct {
			HasTrustDialogAccepted bool
		}
	}
	b, _ := os.ReadFile(filepath.Join(env.HomeDir(), ".claude.json"))
	// Keys use forward slashes (Claude Code's on Windows).
	wt := filepath.ToSlash(rec.Worktree)
	if err := json.Unmarshal(b, &claudeCfg); err != nil || !claudeCfg.Projects[wt].HasTrustDialogAccepted {
		t.Errorf("worktree not trusted (%v): %s", err, b)
	}
	for d := range claudeCfg.Projects {
		if r, _ := filepath.EvalSymlinks(rec.Worktree); d != wt && d != filepath.ToSlash(r) && strings.HasPrefix(d, filepath.ToSlash(filepath.Join(env.Home, "worktrees"))) {
			t.Errorf("trusted more than the worktree: %s", d)
		}
	}
	env.WaitFake("context", agentWait, func(r FakeRecord) bool {
		return r.Str("source") == "startup" && strings.Contains(r.Str("text"), "tm skill thread") && strings.Contains(r.Str("text"), "Task T1 Fix the login")
	})

	// The thread works, reports and finishes.
	if got := readOut(t, out, "done1"); !strings.Contains(got, "no-report") || !strings.Contains(got, "exit 1") {
		t.Errorf("done before a report: %q", got)
	}
	if got := readOut(t, out, "taskstatus"); !strings.Contains(got, "coordinator-only") {
		t.Errorf("thread set a task status: %q", got)
	}
	if got := readOut(t, out, "badreport"); !strings.Contains(got, "missing ## Next") || !strings.Contains(got, "exit 1") {
		t.Errorf("bad report: %q", got)
	}
	if got := readOut(t, out, "report"); !strings.Contains(got, "stored report 1") {
		t.Errorf("report: %q", got)
	}
	if got := readOut(t, out, "done2"); !strings.Contains(got, "exit 0") {
		t.Errorf("done: %q", got)
	}
	if w := env.WaitFake("write", agentWait, nil); w["allowed"] != false {
		t.Errorf("thread wrote into the project: %+v", w)
	}
	if b, _ := os.ReadFile(filepath.Join(projDir, "TASKS.md")); strings.Contains(string(b), "overwritten") {
		t.Fatal("TASKS.md overwritten")
	}

	// Derived progress: 1 of 2 steps, 1 of 2 todos → (1 + 1/2) / 2 = 75 %.
	mid, _ := os.ReadFile(filepath.Join(out, "status-mid"))
	for _, w := range []string{"percent = 75", `percent_source = "steps+todos"`, "- [x] alpha", "- [ ] beta"} {
		if !strings.Contains(string(mid), w) {
			t.Errorf("STATUS.md mid-run lacks %q:\n%s", w, mid)
		}
	}
	var list []struct {
		ID         string
		AgentState string `json:"agent_state"`
		Report, PR string
		Done       bool
		Next       []string
		Status     struct {
			Percent    int
			StepsDone  int `json:"steps_done"`
			StepsTotal int `json:"steps_total"`
		}
	}
	json.Unmarshal([]byte(env.MustCLI("thread", "list", "--json", "--project", "demo")), &list)
	if len(list) != 1 || list[0].Report != "new" || !list[0].Done || list[0].Status.Percent != 100 ||
		list[0].Status.StepsTotal != 2 || list[0].PR != "https://github.com/o/r/pull/7" || len(list[0].Next) != 1 {
		t.Fatalf("thread list: %+v", list)
	}
	inbox := env.MustCLI("inbox", "list", "--project", "demo")
	if !strings.Contains(inbox, "report: T1 Fix the login (t-0001) handed in report 1") {
		t.Errorf("inbox lacks the report:\n%s", inbox)
	}
	if strings.Contains(inbox, "thread-done") {
		t.Errorf("tm done raised an item:\n%s", inbox)
	}
	task := env.MustCLI("task", "show", "T1", "--project", "demo")
	if !strings.Contains(task, "status: started") || !strings.Contains(task, "thread: t-0001") || !strings.Contains(task, "[x] Write the code") {
		t.Errorf("task:\n%s", task)
	}

	// Nothing terminatr-owned in the worktree: it is a clean checkout.
	if st, err := exec.Command("git", "-C", rec.Worktree, "status", "--porcelain", "--ignored").Output(); err != nil || len(st) != 0 {
		t.Errorf("worktree not clean (%v): %q", err, st)
	}
	// The attachment is named, by name only, for the coordinator to
	// point the user at. The task id names the task's open thread, and
	// the thread leads with its task, its id in brackets and details.
	show := env.MustCLI("thread", "show", "T1", "--project", "demo")
	for _, w := range []string{"attached:  notes.md", "thread:    t-0001"} {
		if !strings.Contains(show, w) {
			t.Errorf("thread show lacks %q:\n%s", w, show)
		}
	}
	if !strings.HasPrefix(show, "T1 (t-0001) Fix the login  ") {
		t.Errorf("thread show doesn't lead with the task:\n%s", show)
	}
	if r := env.CLI("thread", "show", "T2", "--project", "demo"); r.Code != 1 || !strings.Contains(r.Stderr, "T2 has no thread") {
		t.Errorf("thread show T2: %+v", r)
	}
	env.MustCLI("thread", "ack", "t-0001", "--project", "demo")

	// Remove the worktree by hand: the report is still there, and
	// resolve copes.
	env.MustCLI("thread", "stop", "t-0001", "--project", "demo")
	if err := os.RemoveAll(rec.Worktree); err != nil {
		t.Fatal(err)
	}
	if r := env.MustCLI("report", "--show", "--thread", "t-0001", "--project", "demo"); !strings.Contains(r, "Did it.") {
		t.Fatalf("report after removing the worktree: %q", r)
	}
	res := env.MustCLI("thread", "resolve", "t-0001", "--project", "demo")
	if !strings.Contains(res, "worktree was already gone") || !strings.Contains(res, "deleted branch tm/demo/t-0001-fix-the-login (merged into origin/main)") {
		t.Fatalf("resolve: %q", res)
	}
	if again := env.MustCLI("thread", "resolve", "t-0001", "--project", "demo"); !strings.Contains(again, "already resolved") {
		t.Fatalf("resolve again: %q", again)
	}
	journal, _ := os.ReadFile(filepath.Join(projDir, "JOURNAL.md"))
	for _, w := range []string{"coordinator thread.start t-0001 T1 Fix the login (approved by the user)", "coordinator thread.prompt t-0001",
		"t-0001 task.steps.add T1", "t-0001 thread.report t-0001", "t-0001 thread.done t-0001", "human thread.resolve t-0001"} {
		if !strings.Contains(string(journal), w) {
			t.Errorf("journal lacks %q:\n%s", w, journal)
		}
	}
}

// TestThreadResolveMergedBranch: resolve deletes a branch whose commits
// are on the default branch without any PR (merge commit on origin, a
// fast-forward in a repo without remote), and keeps a squash-merged or
// unmerged one; the worktree goes in every case.
func TestThreadResolveMergedBranch(t *testing.T) {
	env, projDir, _ := threadEnv(t)
	git := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@t"}, args...)...)
		cmd.Dir = dir
		if b, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, b)
		}
	}
	// thread starts a thread, commits a file in its worktree and returns
	// its record.
	type record struct{ Repo, Worktree, Branch, Session string }
	thread := func(id, title string) record {
		t.Helper()
		env.MustCLI("thread", "start", title, "--project", "demo")
		var rec record
		readTOML(t, filepath.Join(projDir, "threads", id, "thread.toml"), &rec)
		env.WaitState(&Session{ID: rec.Session}, "idle", agentWait)
		os.WriteFile(filepath.Join(rec.Worktree, id), []byte(id), 0o644)
		git(rec.Worktree, "add", ".")
		git(rec.Worktree, "commit", "-q", "-m", title)
		return rec
	}
	resolve := func(rec record, id, want string, gone bool) {
		t.Helper()
		res := env.MustCLI("thread", "resolve", id, "--project", "demo")
		if !strings.Contains(res, "removed worktree "+rec.Worktree) || !strings.Contains(res, want) {
			lg, _ := exec.Command("git", "-C", rec.Repo, "log", "--oneline", "--graph", "--all", "--decorate").CombinedOutput()
			t.Logf("%s", lg)
			t.Fatalf("resolve %s: %q, want %q", id, res, want)
		}
		if _, err := os.Stat(rec.Worktree); !os.IsNotExist(err) {
			t.Fatalf("%s: worktree still there: %v", id, err)
		}
		cmd := exec.Command("git", "rev-parse", "--verify", "--quiet", "refs/heads/"+rec.Branch)
		cmd.Dir = rec.Repo
		if exists := cmd.Run() == nil; exists == gone {
			t.Fatalf("%s: branch %s exists %v after %q", id, rec.Branch, exists, res)
		}
	}

	// Merged on origin with a merge commit, no PR: deleted.
	a := thread("t-0001", "Merge commit")
	git(a.Repo, "merge", "-q", "--no-ff", "-m", "Merge", a.Branch)
	git(a.Repo, "push", "-q", "origin", "main")
	git(a.Repo, "fetch", "-q", "origin")
	resolve(a, "t-0001", "deleted branch "+a.Branch+" (merged into origin/main)", true)

	// Squash-merged, no PR: kept (git can't tell its work is on main).
	b := thread("t-0002", "Squash")
	git(b.Repo, "merge", "-q", "--squash", b.Branch)
	git(b.Repo, "commit", "-q", "-m", "Squash (#2)")
	git(b.Repo, "push", "-q", "origin", "main")
	git(b.Repo, "fetch", "-q", "origin")
	resolve(b, "t-0002", "kept branch "+b.Branch+" (no merged PR found)", false)

	// A thread with more branches of its own (a second PR's): one merged
	// on origin is deleted with git branch -d although the repo's main
	// lags origin's; an unmerged one, and one checked out in another
	// worktree, are kept and the item says why.
	e := thread("t-0003", "Two PRs")
	second, third, fourth := "tm/demo/t-0003-second", "tm/demo/t-0003-third", "tm/demo/t-0003-fourth"
	git(e.Worktree, "checkout", "-q", "-b", second)
	os.WriteFile(filepath.Join(e.Worktree, "second"), []byte("2"), 0o644)
	git(e.Worktree, "add", ".")
	git(e.Worktree, "commit", "-q", "-m", "second")
	git(e.Worktree, "push", "-q", "origin", "HEAD:main")
	git(e.Worktree, "checkout", "-q", "-b", third)
	git(e.Worktree, "commit", "-q", "--allow-empty", "-m", "unmerged")
	git(e.Worktree, "checkout", "-q", e.Branch)
	git(e.Repo, "fetch", "-q", "origin")
	git(e.Repo, "branch", fourth, "origin/main")
	elsewhere := filepath.Join(t.TempDir(), "elsewhere")
	git(e.Repo, "worktree", "add", "-q", elsewhere, fourth)
	resolve(e, "t-0003", "deleted branch "+second+" (merged into origin/main)", true)
	res := env.MustCLI("inbox", "list", "--project", "demo")
	for _, w := range []string{"kept branch " + third + " (not merged into origin/main)", "kept branch " + fourth + " (checked out in "} {
		if !strings.Contains(res, w) {
			t.Errorf("resolve item lacks %q:\n%s", w, res)
		}
	}
	for br, want := range map[string]bool{second: false, third: true, fourth: true} {
		cmd := exec.Command("git", "rev-parse", "--verify", "--quiet", "refs/heads/"+br)
		cmd.Dir = e.Repo
		if exists := cmd.Run() == nil; exists != want {
			t.Errorf("branch %s exists %v, want %v", br, exists, want)
		}
	}
	journal, _ := os.ReadFile(filepath.Join(projDir, "JOURNAL.md"))
	for _, w := range []string{"human branch.delete t-0003 " + e.Branch + " (merged into origin/main)", "human branch.delete t-0003 " + second + " (merged into origin/main)"} {
		if !strings.Contains(string(journal), w) {
			t.Errorf("journal lacks %q:\n%s", w, journal)
		}
	}
	git(e.Repo, "worktree", "remove", elsewhere)

	// A repo without remote, merged by fast-forward (tm's own todo
	// project): deleted; an unmerged branch is kept.
	c := thread("t-0004", "Local")
	d := thread("t-0005", "Unmerged")
	git(c.Repo, "remote", "remove", "origin")
	git(c.Repo, "merge", "-q", "--ff-only", c.Branch)
	resolve(c, "t-0004", "deleted branch "+c.Branch+" (merged into main)", true)
	resolve(d, "t-0005", "kept branch "+d.Branch+" (no merged PR found)", false)
}

// TestThreadRestartResumes: a thread that worked on a prompt is resumed
// with its agent session id on restart; its brief says a previous
// attempt exists. A dirty worktree survives resolve.
func TestThreadRestartResumes(t *testing.T) {
	env, projDir, _ := threadEnv(t)
	env.MustCLI("thread", "start", "Small fix", "--project", "demo")
	var rec struct{ Worktree, Session string }
	tdir := filepath.Join(projDir, "threads", "t-0001")
	readTOML(t, filepath.Join(tdir, "thread.toml"), &rec)
	s := &Session{ID: rec.Session}
	env.WaitState(s, "idle", agentWait)
	env.MustCLI("thread", "prompt", "t-0001", "hello", "--project", "demo")
	env.WaitFake("prompt", agentWait, func(r FakeRecord) bool { return r.Str("text") == "hello" })
	env.WaitState(s, "idle", agentWait)
	var after struct {
		Prompted bool
		SID      string `toml:"agent_session_id"`
	}
	Poll(agentWait, func() bool { readTOML(t, filepath.Join(tdir, "thread.toml"), &after); return after.Prompted })
	if !after.Prompted || after.SID == "" {
		t.Fatalf("record not prompted: %+v", after)
	}

	out := env.MustCLI("thread", "restart", "t-0001", "--project", "demo")
	if !strings.Contains(out, "resumed "+after.SID) {
		t.Fatalf("restart: %q", out)
	}
	env.WaitFake("start", agentWait, func(r FakeRecord) bool { return r.Str("resume") == after.SID })
	if b, _ := os.ReadFile(filepath.Join(tdir, "brief.md")); !strings.Contains(string(b), "A previous attempt exists") {
		t.Fatalf("restart brief:\n%s", b)
	}

	os.WriteFile(filepath.Join(rec.Worktree, "wip.txt"), []byte("x"), 0o644)
	res := env.MustCLI("thread", "resolve", "t-0001", "--project", "demo")
	if !strings.Contains(res, "kept worktree") {
		t.Fatalf("resolve of a dirty worktree: %q", res)
	}
	if _, err := os.Stat(filepath.Join(rec.Worktree, "wip.txt")); err != nil {
		t.Fatal("dirty worktree removed")
	}
	if items := env.MustCLI("inbox", "list", "--project", "demo"); !strings.Contains(items, "thread-resolved: t-0001 (Small fix) resolved: kept worktree") {
		t.Fatalf("inbox:\n%s", items)
	}
}
