package e2e

// The parallel threads cap and auto-close (docs/SPEC.md §9, §11.2).

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeConfig(t *testing.T, env *Env, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(env.Home, "config.toml"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func threadState(t *testing.T, projDir, id string) string {
	t.Helper()
	var rec struct{ State string }
	readTOML(t, filepath.Join(projDir, "threads", id, "thread.toml"), &rec)
	return rec.State
}

// TestThreadCapAndOverride: with a cap of 1, an idle thread doesn't
// count, a blocked one does; at the cap tm thread start refuses with
// exit 1, and --over-cap starts it anyway, journaled as the user's.
func TestThreadCapAndOverride(t *testing.T) {
	env, projDir, _ := tickerEnv(t)
	writeConfig(t, env, "[projects.demo]\nparallel_threads = 1\n")
	th := startThread(t, env, projDir) // t-0001, idle after its kickoff
	env.MustCLI("thread", "start", "Second", "--project", "demo")
	var rec struct{ Session string }
	readTOML(t, filepath.Join(projDir, "threads", "t-0002", "thread.toml"), &rec)
	env.WaitState(&Session{ID: rec.Session}, "idle", agentWait)

	env.MustCLI("thread", "prompt", "t-0001", "run thread-block", "--project", "demo")
	env.WaitState(th, "blocked", agentWait)
	r := env.CLI("thread", "start", "Third", "--project", "demo")
	if r.Code != 1 || !strings.Contains(r.Stderr, "1 of 1 parallel threads are working") || !strings.Contains(r.Stderr, "--over-cap") {
		t.Fatalf("at the cap: exit %d\n%s%s", r.Code, r.Stdout, r.Stderr)
	}
	if _, err := os.Stat(filepath.Join(projDir, "threads", "t-0003")); !os.IsNotExist(err) {
		t.Fatalf("a refused start left a thread: %v", err)
	}
	env.MustCLI("thread", "start", "Third", "--over-cap", "--project", "demo")
	if j, _ := os.ReadFile(filepath.Join(projDir, "JOURNAL.md")); !strings.Contains(string(j), "thread.start t-0003 Third (over the parallel threads cap, approved by the user)") {
		t.Fatalf("journal:\n%s", j)
	}
}

// TestSmokeTickerAutoCloseKeepsUnsavedWork: the PR merged and the agent
// is idle, but the worktree has uncommitted changes: the thread stays
// open and the coordinator gets one item; once the work is gone, it
// closes.
func TestSmokeTickerAutoCloseKeepsUnsavedWork(t *testing.T) {
	env, projDir, _ := tickerEnv(t)
	state := fakeGH(t, env)
	startThread(t, env, projDir)
	var rec struct{ Worktree string }
	readTOML(t, filepath.Join(projDir, "threads", "t-0001", "thread.toml"), &rec)
	scratch := filepath.Join(rec.Worktree, "scratch.txt")
	if err := os.WriteFile(scratch, []byte("not committed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	setPR(t, state, `{"number":7,"url":"https://github.com/o/r/pull/7","state":"MERGED","statusCheckRollup":[]}`)
	waitInbox(t, env, "close-held: t-0001 finished but was not auto-closed: uncommitted changes in its worktree")
	time.Sleep(time.Second) // a few sweeps
	if st := threadState(t, projDir, "t-0001"); st != "running" {
		t.Fatalf("thread %s with uncommitted changes", st)
	}
	if items := env.MustCLI("inbox", "list", "--project", "demo"); strings.Count(items, "close-held:") != 1 {
		t.Fatalf("inbox:\n%s", items)
	}
	os.Remove(scratch)
	waitInbox(t, env, "thread-resolved: t-0001 resolved: removed worktree")
}

// TestTickerAutoCloseDays: auto-close N days after the thread finished
// (tm done), with a day shortened to a second.
func TestTickerAutoCloseDays(t *testing.T) {
	env, projDir, out := tickerEnv(t)
	fakeGH(t, env)
	env.Setenv("TERMALATOR_TICK_DAY", "1s")
	writeConfig(t, env, "[projects.demo]\nauto_close = \"days\"\nauto_close_days = 2\n")
	os.WriteFile(filepath.Join(env.scriptsDir(), "thread-finish.toml"), []byte(`
[[step]]
do = "run"
cmd = 'printf "## Report\nDone.\n\n## Next\nMerge the PR\n" | "$TERMALATOR_BIN" report && "$TERMALATOR_BIN" done > "$OUT/done" 2>&1; echo "exit $?" >> "$OUT/done"'
`), 0o644)
	th := startThread(t, env, projDir)
	time.Sleep(3 * time.Second) // idle, not done: nothing closes
	if st := threadState(t, projDir, "t-0001"); st != "running" {
		t.Fatalf("an unfinished thread is %s", st)
	}
	env.MustCLI("thread", "prompt", "t-0001", "run thread-finish", "--project", "demo")
	if got := readOut(t, out, "done"); !strings.Contains(got, "t-0001 done") {
		t.Fatalf("done: %q", got)
	}
	done := time.Now()
	env.WaitState(th, "idle", agentWait)
	waitInbox(t, env, "thread-resolved: t-0001 resolved")
	if d := time.Since(done); d < 2*time.Second-200*time.Millisecond {
		t.Fatalf("closed %v after tm done, before 2 days of a second", d)
	}
}
