package e2e

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestProjectRenameLive: tm project rename through a running server. A
// running thread refuses it; with the thread stopped the coordinator is
// stopped, the project, its worktree and its records move, git still
// knows the worktree, the coordinator comes back under the new slug, and
// the thread resumes in its moved worktree on its old branch.
func TestProjectRenameLive(t *testing.T) {
	env, projDir, _ := threadEnv(t)
	coord := env.StartAgent("claude", projDir, "--role", "coordinator", "--project", "demo")
	env.WaitState(coord, "idle", agentWait)
	env.MustCLI("thread", "start", "Small fix", "--project", "demo")
	tdir := filepath.Join(projDir, "threads", "t-0001")
	var rec struct {
		Worktree, Branch, Session, Repo string
		SID                             string `toml:"agent_session_id"`
		Prompted                        bool
	}
	readTOML(t, filepath.Join(tdir, "thread.toml"), &rec)
	s := &Session{ID: rec.Session}
	env.WaitState(s, "idle", agentWait)
	env.MustCLI("thread", "prompt", "t-0001", "hello", "--project", "demo")
	Poll(agentWait, func() bool { readTOML(t, filepath.Join(tdir, "thread.toml"), &rec); return rec.Prompted })

	if r := env.CLI("project", "rename", "demo", "demo2"); r.Code != 1 || !strings.Contains(r.Stderr, "sessions-running") || !strings.Contains(r.Stderr, "t-0001") {
		t.Fatalf("rename with a running thread: %+v", r)
	}
	if _, err := os.Stat(projDir); err != nil {
		t.Fatalf("refused rename moved the folder: %v", err)
	}
	env.MustCLI("thread", "stop", "t-0001", "--project", "demo")

	out := env.MustCLI("project", "rename", "demo", "demo2")
	if !strings.Contains(out, "renamed demo to demo2") || !strings.Contains(out, "coordinator started again") || strings.Contains(out, "note:") {
		t.Fatalf("rename: %q", out)
	}
	newDir := filepath.Join(env.Home, "projects", "demo2")
	if _, err := os.Stat(projDir); !os.IsNotExist(err) {
		t.Fatalf("old folder: %v", err)
	}
	for _, info := range env.Sessions() {
		if info.Project == "demo" {
			t.Errorf("a session of the old slug runs: %+v", info)
		}
	}
	// It starts without a trust screen: Claude's settings for the folder
	// came along.
	env.WaitState(coordinatorOf(t, env, "demo2"), "idle", agentWait)

	var moved struct{ Worktree, Branch string }
	readTOML(t, filepath.Join(newDir, "threads", "t-0001", "thread.toml"), &moved)
	wt := filepath.Join(env.Home, "worktrees", "demo2", filepath.Base(rec.Worktree))
	if moved.Worktree != wt || moved.Branch != rec.Branch || !strings.HasPrefix(rec.Branch, "tm/demo/") {
		t.Fatalf("record %+v (was %+v)", moved, rec)
	}
	if b, err := exec.Command("git", "-C", rec.Repo, "worktree", "list", "--porcelain").Output(); err != nil || strings.Contains(string(b), "prunable") || !strings.Contains(string(b), filepath.Base(wt)) {
		t.Fatalf("git worktree list (%v):\n%s", err, b)
	}
	if b, _ := os.ReadFile(filepath.Join(newDir, "JOURNAL.md")); !strings.Contains(string(b), "human project.rename demo2 demo → demo2\n") {
		t.Fatalf("journal:\n%s", b)
	}

	out = env.MustCLI("thread", "restart", "t-0001", "--project", "demo2")
	if !strings.Contains(out, "resumed "+rec.SID) {
		t.Fatalf("restart: %q", out)
	}
	env.WaitFake("start", agentWait, func(r FakeRecord) bool { return r.Str("resume") == rec.SID })
	readTOML(t, filepath.Join(newDir, "threads", "t-0001", "thread.toml"), &rec)
	if info, ok := env.Info(&Session{ID: rec.Session}); !ok || info.Cwd != wt {
		t.Fatalf("restarted thread: %+v %v", info, ok)
	}
}
