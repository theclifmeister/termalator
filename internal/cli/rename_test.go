package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/theclifmeister/terminatr/internal/project"
	"github.com/theclifmeister/terminatr/internal/thread"
	"github.com/theclifmeister/terminatr/internal/worktree"
)

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@t", "-c", "init.defaultBranch=main"}, args...)...)
	cmd.Dir = dir
	b, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, b)
	}
	return strings.TrimSpace(string(b))
}

// threadWithWorktree makes a repo and a thread of project slug with a
// live worktree of it, as tm thread start would.
func threadWithWorktree(t *testing.T, h *harness, slug string) (repo string, rec *thread.Record) {
	t.Helper()
	repo = filepath.Join(t.TempDir(), "repo")
	os.MkdirAll(repo, 0o755)
	git(t, repo, "init", "-q")
	os.WriteFile(filepath.Join(repo, "f"), []byte("x"), 0o644)
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-q", "-m", "c")
	h.ok(human, "project", "repo", "add", repo, "--project", slug)
	p, err := project.Open(slug)
	if err != nil {
		t.Fatal(err)
	}
	wt, _ := thread.WorktreeDir(slug, "t-0001", "Fix it")
	branch := thread.BranchName(slug, "t-0001", "Fix it")
	os.MkdirAll(filepath.Dir(wt), 0o755)
	git(t, repo, "worktree", "add", "-q", "-b", branch, wt)
	rec, err = thread.Create(p, thread.Record{Title: "Fix it", Agent: "claude", Repo: repo, Branch: branch, Worktree: wt,
		State: thread.Stopped, AgentSID: "sid-1", Prompted: true})
	if err != nil {
		t.Fatal(err)
	}
	return repo, rec
}

func TestProjectRename(t *testing.T) {
	h := newHarness(t)
	claudeDir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", claudeDir)
	h.ok(human, "project", "new", "demo")
	h.ok(human, "project", "new", "other")
	repo, rec := threadWithWorktree(t, h, "demo")
	cfg := filepath.Join(h.root, "config.toml")
	os.WriteFile(cfg, []byte("# mine\n[projects.demo] # the demo\n# keep me\nauto_close = \"merged\"\n\n[projects.other]\nyolo = true\n"), 0o600)
	os.MkdirAll(filepath.Join(h.root, "state"), 0o700)
	os.WriteFile(filepath.Join(h.root, "state", "ticker.json"), []byte(`{"threads":{"demo/t-0001":{"agent":"idle","reports":2,"pr":{},"pr_polled":"0001-01-01T00:00:00Z"}},"projects":{"demo":{"nudged":["i-1"],"last_nudge":"0001-01-01T00:00:00Z","completed":{"T1":"abc"}}}}`), 0o600)
	// Claude's conversation for the worktree, to resume after the move.
	conv := filepath.Join(claudeDir, "projects", strings.Map(keyRune, rec.Worktree))
	os.MkdirAll(conv, 0o700)
	os.WriteFile(filepath.Join(conv, "sid-1.jsonl"), []byte("{}\n"), 0o600)
	os.WriteFile(filepath.Join(claudeDir, ".claude.json"), []byte(`{"projects":{"`+rec.Worktree+`":{"hasTrustDialogAccepted":true}}}`), 0o600)

	// Refusals, before anything moves.
	h.expect(1, "human-only", coord, "project", "rename", "demo", "demo2")
	h.expect(2, "usage", human, "project", "rename", "demo")
	h.expect(1, "unknown-project", human, "project", "rename", "nope", "demo2")
	h.expect(1, "invalid-project", human, "project", "rename", "demo", "!!")
	h.expect(1, "project-exists", human, "project", "rename", "demo", "other")
	h.expect(1, "unchanged", human, "project", "rename", "demo", "demo")
	os.MkdirAll(filepath.Join(h.root, "worktrees", "taken"), 0o755)
	h.expect(1, "project-exists", human, "project", "rename", "demo", "taken")

	out := h.ok(human, "project", "rename", "demo", "demo2")
	if !strings.Contains(out, "renamed demo to demo2") || !strings.Contains(out, "1 thread records updated") || strings.Contains(out, "note:") {
		t.Fatalf("rename: %q", out)
	}
	if _, err := os.Stat(filepath.Join(h.root, "projects", "demo")); !os.IsNotExist(err) {
		t.Fatalf("old folder: %v", err)
	}
	p, err := project.Open("demo2")
	if err != nil {
		t.Fatalf("open: %v %+v", err, p)
	}
	j, _ := os.ReadFile(p.Path("JOURNAL.md"))
	if !strings.Contains(string(j), "project.new demo") || !strings.Contains(string(j), "project.rename demo2 demo → demo2\n") {
		t.Fatalf("journal:\n%s", j)
	}

	// The worktree moved, its record follows, git knows it, and the
	// branch keeps its name.
	r, err := thread.Load(p, rec.ID)
	wt := filepath.Join(h.root, "worktrees", "demo2", filepath.Base(rec.Worktree))
	if err != nil || r.Worktree != wt || r.Branch != "tm/demo/t-0001-fix-it" {
		t.Fatalf("record: %v %+v", err, r)
	}
	real, _ := filepath.EvalSymlinks(wt)
	if list := git(t, repo, "worktree", "list", "--porcelain"); !strings.Contains(list, "worktree "+real+"\n") || strings.Contains(list, "prunable") {
		t.Fatalf("worktree list:\n%s", list)
	}
	if b := git(t, wt, "rev-parse", "--abbrev-ref", "HEAD"); b != r.Branch {
		t.Fatalf("worktree branch %q", b)
	}

	// The settings moved with their comments; the memos and the agent's
	// conversation too.
	if b, _ := os.ReadFile(cfg); string(b) != "# mine\n[projects.demo2] # the demo\n# keep me\nauto_close = \"merged\"\n\n[projects.other]\nyolo = true\n" {
		t.Fatalf("config.toml:\n%s", b)
	}
	if b, _ := os.ReadFile(filepath.Join(h.root, "state", "ticker.json")); !strings.Contains(string(b), `"demo2/t-0001"`) || !strings.Contains(string(b), `"demo2": {`) || strings.Contains(string(b), `"demo"`) {
		t.Fatalf("ticker.json:\n%s", b)
	}
	if _, err := os.Stat(filepath.Join(claudeDir, "projects", strings.Map(keyRune, wt), "sid-1.jsonl")); err != nil {
		t.Fatalf("conversation not moved: %v", err)
	}
	if b, _ := os.ReadFile(filepath.Join(claudeDir, ".claude.json")); !strings.Contains(string(b), `"`+wt+`"`) {
		t.Fatalf(".claude.json:\n%s", b)
	}

	// A new thread's branch takes the new slug; --name is gone.
	if b := thread.BranchName("demo2", "t-0002", "More"); b != "tm/demo2/t-0002-more" {
		t.Fatalf("branch %q", b)
	}
	h.expect(2, "unknown flag --name", human, "project", "rename", "demo2", "demo3", "--name", "Demo 3")
}

func keyRune(r rune) rune {
	if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' {
		return r
	}
	return '-'
}

// T56: a thread whose repo folder is gone resolves without git; one
// whose repo moved to another of the project's repos follows it there.
func TestResolveMissingOrMovedRepo(t *testing.T) {
	h := newHarness(t)
	h.ok(human, "project", "new", "demo")
	repo, rec := threadWithWorktree(t, h, "demo")

	moved := filepath.Join(filepath.Dir(repo), "moved")
	if err := os.Rename(repo, moved); err != nil {
		t.Fatal(err)
	}
	h.ok(human, "project", "repo", "add", moved, "--project", "demo")
	out := h.ok(coord, "thread", "resolve", rec.ID, "--project", "demo")
	if !strings.Contains(out, "removed worktree "+rec.Worktree) {
		t.Fatalf("resolve after a move: %q", out)
	}
	p, _ := project.Open("demo")
	if r, _ := thread.Load(p, rec.ID); r.Repo != p.Meta.Repos[1] {
		t.Fatalf("repo %q, want %q", r.Repo, p.Meta.Repos[1])
	}

	// Gone for good: nothing for git to do, and no git error.
	h2 := newHarness(t)
	h2.ok(human, "project", "new", "demo")
	repo, rec = threadWithWorktree(t, h2, "demo")
	os.RemoveAll(repo)
	out = h2.ok(coord, "thread", "resolve", rec.ID, "--project", "demo")
	if !strings.Contains(out, "repo "+repo+" is gone") || !strings.Contains(out, "kept worktree") || strings.Contains(out, "chdir") {
		t.Fatalf("resolve without a repo: %q", out)
	}
}

// T131: resolve --discard removes a worktree with uncommitted files and
// its branch; a branch with commits that are nowhere else is refused.
func TestResolveDiscard(t *testing.T) {
	h := newHarness(t)
	h.ok(human, "project", "new", "demo")
	repo, rec := threadWithWorktree(t, h, "demo")
	os.WriteFile(filepath.Join(rec.Worktree, "report.md"), []byte("notes"), 0o644)

	// Plain resolve keeps a dirty worktree and its branch.
	h.expect(2, "unknown flag", coord, "thread", "ack", rec.ID, "--discard", "--project", "demo")
	h.expect(1, "coordinator-only", thr, "thread", "resolve", rec.ID, "--discard", "--project", "demo")

	// A commit on the branch is unsaved work: refused, nothing touched.
	git(t, rec.Worktree, "add", ".")
	git(t, rec.Worktree, "commit", "-q", "-m", "work")
	os.WriteFile(filepath.Join(rec.Worktree, "more.md"), []byte("x"), 0o644)
	h.expect(1, "unsaved work", coord, "thread", "resolve", rec.ID, "--discard", "--project", "demo")
	if _, err := os.Stat(rec.Worktree); err != nil {
		t.Fatalf("worktree touched by a refused discard: %v", err)
	}
	git(t, rec.Worktree, "reset", "-q", "--hard", "HEAD~1")
	os.WriteFile(filepath.Join(rec.Worktree, "report.md"), []byte("notes"), 0o644)

	out := h.ok(coord, "thread", "resolve", rec.ID, "--discard", "--project", "demo")
	if !strings.Contains(out, "removed worktree "+rec.Worktree) || !strings.Contains(out, "deleted branch "+rec.Branch+" (discarded)") {
		t.Fatalf("discard: %q", out)
	}
	if _, err := os.Stat(rec.Worktree); !os.IsNotExist(err) {
		t.Fatalf("worktree still there: %v", err)
	}
	if worktree.BranchExists(repo, rec.Branch) {
		t.Fatal("branch still there")
	}
}

// T136: resolve --discard on an already-resolved thread cleans up its
// leftover worktree and branch; without --discard it stays a no-op.
func TestResolveDiscardAlreadyResolved(t *testing.T) {
	h := newHarness(t)
	h.ok(human, "project", "new", "demo")
	repo, rec := threadWithWorktree(t, h, "demo")
	os.WriteFile(filepath.Join(rec.Worktree, "REPORT.md"), []byte("notes"), 0o644)

	// Plain resolve keeps the dirty worktree and the branch.
	h.ok(coord, "thread", "resolve", rec.ID, "--project", "demo")
	out := h.ok(coord, "thread", "resolve", rec.ID, "--project", "demo")
	if !strings.Contains(out, "already resolved") {
		t.Fatalf("resolve again: %q", out)
	}
	if _, err := os.Stat(rec.Worktree); err != nil {
		t.Fatalf("worktree gone: %v", err)
	}

	out = h.ok(coord, "thread", "resolve", rec.ID, "--discard", "--project", "demo")
	if !strings.Contains(out, "removed worktree "+rec.Worktree) || !strings.Contains(out, "deleted branch "+rec.Branch+" (discarded)") {
		t.Fatalf("discard resolved: %q", out)
	}
	if _, err := os.Stat(rec.Worktree); !os.IsNotExist(err) {
		t.Fatalf("worktree still there: %v", err)
	}
	if worktree.BranchExists(repo, rec.Branch) {
		t.Fatal("branch still there")
	}

	// Nothing left: back to a no-op.
	out = h.ok(coord, "thread", "resolve", rec.ID, "--discard", "--project", "demo")
	if !strings.Contains(out, "already resolved") {
		t.Fatalf("nothing left: %q", out)
	}
}
