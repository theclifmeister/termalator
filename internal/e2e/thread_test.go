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
cmd = '"$TERMILATOR_BIN" task delegate T1 > "$OUT/delegate1" 2>&1; echo "exit $?" >> "$OUT/delegate1"'

[[step]]
do = "run"
cmd = '"$TERMILATOR_BIN" task delegate T1 --approved-by-user > "$OUT/delegate2" 2>&1; echo "exit $?" >> "$OUT/delegate2"'

[[step]]
do = "run"
cmd = '"$TERMILATOR_BIN" thread prompt t-0001 "run thread-work" > "$OUT/prompt" 2>&1; echo "exit $?" >> "$OUT/prompt"'

[[step]]
do = "stream"
ms = 50
text = "COORD-DONE"
`,
	"thread-work": `
[[step]]
do = "run"
cmd = '"$TERMILATOR_BIN" done > "$OUT/done1" 2>&1; echo "exit $?" >> "$OUT/done1"'

[[step]]
do = "run"
cmd = '"$TERMILATOR_BIN" task steps T1 add "Write the code" && "$TERMILATOR_BIN" task steps T1 add "Test it"'

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
cmd = '"$TERMILATOR_BIN" task steps T1 check 1 && cp "$TERMILATOR_HOME/projects/demo/threads/t-0001/STATUS.md" "$OUT/status-mid"'

[[step]]
do = "run"
cmd = '"$TERMILATOR_BIN" task status T1 review > "$OUT/taskstatus" 2>&1; echo "exit $?" >> "$OUT/taskstatus"'

[[step]]
do = "write"
path = "$FAKEAGENT_DENY_PATH/TASKS.md"
text = "overwritten\n"

[[step]]
do = "run"
cmd = 'printf "## Report\nSome work.\n" | "$TERMILATOR_BIN" report > "$OUT/badreport" 2>&1; echo "exit $?" >> "$OUT/badreport"'

[[step]]
do = "run"
cmd = 'printf "PR: https://github.com/o/r/pull/7\n\n## Report\nDid it.\n\n## Next\nMerge the PR\n\n## Remember\n- a lesson\n" | "$TERMILATOR_BIN" report > "$OUT/report" 2>&1; echo "exit $?" >> "$OUT/report"'

[[step]]
do = "run"
cmd = '"$TERMILATOR_BIN" done "all good" > "$OUT/done2" 2>&1; echo "exit $?" >> "$OUT/done2"'

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
	if err := json.Unmarshal([]byte(env.MustCLI("project", "new", "Demo", "--repo", repo, "--json")), &p); err != nil {
		t.Fatal(err)
	}
	real, _ := filepath.EvalSymlinks(p.Dir)
	env.Setenv("FAKEAGENT_DENY_PATH", real)
	os.MkdirAll(filepath.Join(env.Home, "worktrees"), 0o755)
	env.Trust(p.Dir, filepath.Join(env.Home, "worktrees"))
	return env, p.Dir, out
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
	for _, w := range []string{filepath.Join(projDir, "TASKS.md"), rec.Worktree, "tm skill thread", "# T1 Fix the login"} {
		if !strings.Contains(string(brief), w) {
			t.Errorf("brief lacks %q:\n%s", w, brief)
		}
	}
	env.WaitFake("prompt", agentWait, func(r FakeRecord) bool {
		return r.Str("via") == "kickoff" && strings.Contains(r.Str("text"), filepath.Join(tdir, "brief.md"))
	})
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
	for _, w := range []string{"report: t-0001 handed in report 1 (T1)", "thread-done: t-0001 is done with T1"} {
		if !strings.Contains(inbox, w) {
			t.Errorf("inbox lacks %q:\n%s", w, inbox)
		}
	}
	task := env.MustCLI("task", "show", "T1", "--project", "demo")
	if !strings.Contains(task, "status: started") || !strings.Contains(task, "thread: t-0001") || !strings.Contains(task, "[x] Write the code") {
		t.Errorf("task:\n%s", task)
	}

	// Nothing termilator-owned in the worktree: it is a clean checkout.
	if st, err := exec.Command("git", "-C", rec.Worktree, "status", "--porcelain", "--ignored").Output(); err != nil || len(st) != 0 {
		t.Errorf("worktree not clean (%v): %q", err, st)
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
	if !strings.Contains(res, "worktree was already gone") || !strings.Contains(res, "kept branch tm/demo/t-0001-fix-the-login") {
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
	if items := env.MustCLI("inbox", "list", "--project", "demo"); !strings.Contains(items, "thread-resolved: t-0001 resolved: kept worktree") {
		t.Fatalf("inbox:\n%s", items)
	}
}
