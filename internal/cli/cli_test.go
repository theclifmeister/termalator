package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/theclifmeister/termilator/internal/caller"
	"github.com/theclifmeister/termilator/internal/home"
)

type harness struct {
	t    *testing.T
	root string
	env  map[string]string
	cwd  string
}

func newHarness(t *testing.T) *harness {
	root := t.TempDir()
	t.Setenv(home.Env, root)
	return &harness{t: t, root: root, env: map[string]string{}, cwd: t.TempDir()}
}

// run runs one tm command in-process as the given caller.
func (h *harness) run(c caller.Caller, stdin string, args ...string) (code int, stdout, stderr string) {
	h.t.Helper()
	var out, errb bytes.Buffer
	e := &Env{
		Stdin: strings.NewReader(stdin), Stdout: &out, Stderr: &errb,
		Getenv: func(k string) string { return h.env[k] }, Cwd: h.cwd, Caller: c,
	}
	code, ok := e.Run(args)
	if !ok {
		h.t.Fatalf("not handled: %v", args)
	}
	return code, out.String(), errb.String()
}

func (h *harness) ok(c caller.Caller, args ...string) string {
	h.t.Helper()
	code, out, errs := h.run(c, "", args...)
	if code != 0 {
		h.t.Fatalf("tm %v: exit %d\n%s%s", args, code, out, errs)
	}
	return out
}

func (h *harness) expect(wantCode int, wantErr string, c caller.Caller, args ...string) {
	h.t.Helper()
	code, out, errs := h.run(c, "", args...)
	if code != wantCode || !strings.Contains(errs, wantErr) {
		h.t.Fatalf("tm %v: exit %d, stderr %q (stdout %q); want %d and %q", args, code, errs, out, wantCode, wantErr)
	}
}

var (
	human = caller.Caller{Kind: caller.Human}
	coord = caller.Caller{Kind: caller.Coordinator, Project: "demo"}
	thr   = caller.Caller{Kind: caller.Thread, Project: "demo", Thread: "t-0001"}
)

func TestNotHandled(t *testing.T) {
	e := &Env{}
	if _, ok := e.Run([]string{"no-such-command"}); ok {
		t.Fatal("handled a foreign command")
	}
}

func TestProjectNewList(t *testing.T) {
	h := newHarness(t)
	out := h.ok(human, "project", "new", "Demo", "--goal", "Try tm", "--repo", h.cwd)
	if !strings.Contains(out, "created project demo at "+filepath.Join(h.root, "projects", "demo")) {
		t.Fatalf("out %q", out)
	}
	h.expect(1, "project-exists", human, "project", "new", "demo")
	h.expect(1, "human-only", coord, "project", "new", "other")
	h.expect(2, "usage", human, "project", "new")
	h.expect(2, "unknown flag", human, "project", "list", "--bogus")
	if out := h.ok(human, "project", "list"); !strings.Contains(out, "demo") || !strings.Contains(out, "Try tm") {
		t.Fatalf("list %q", out)
	}
	var list []map[string]any
	json.Unmarshal([]byte(h.ok(human, "project", "list", "--json")), &list)
	if len(list) != 1 || list[0]["slug"] != "demo" {
		t.Fatalf("json %v", list)
	}
}

func TestTaskWorkflow(t *testing.T) {
	h := newHarness(t)
	h.ok(human, "project", "new", "demo")
	h.expect(2, "no project here", coord, "task", "list")
	h.env[caller.EnvProject] = "demo"

	out := h.ok(coord, "task", "add", "Fix login redirect", "--notes", "Users land on /home", "--step", "Reproduce", "--step", "Fix")
	if out != "created T1 Fix login redirect\n" {
		t.Fatalf("add %q", out)
	}
	if out := h.ok(coord, "task", "add", "Fix login redirect"); out != "existing T1 Fix login redirect\n" {
		t.Fatalf("re-add %q", out)
	}
	h.expect(1, "empty-title", coord, "task", "add", " ")

	// Bulk add, with one bad item: valid ones are saved, exit 1.
	plan := `[{"title":"Write docs","status":"ready"},{"title":"Sneak","status":"done"},{"title":"Fix login redirect"}]`
	code, out, _ := h.run(coord, plan, "task", "add", "--json")
	var res []map[string]string
	json.Unmarshal([]byte(out), &res)
	if code != 1 || len(res) != 3 || res[0]["result"] != "created" || res[1]["code"] != "human-only" || res[2]["result"] != "existing" {
		t.Fatalf("bulk: exit %d %v", code, res)
	}
	code, _, _ = h.run(coord, `{"nope":1}`, "task", "add", "--json")
	if code != 2 {
		t.Fatalf("bad plan exit %d", code)
	}

	h.ok(coord, "task", "status", "T1", "started")
	if out := h.ok(coord, "task", "status", "t1", "start"); out != "T1 unchanged (already started)\n" {
		t.Fatalf("idempotent status %q", out)
	}
	h.expect(1, "invalid-status", coord, "task", "status", "T1", "later")
	h.expect(2, "not a task id", coord, "task", "status", "X1", "ready")
	h.expect(1, "unknown-task", coord, "task", "status", "T99", "ready")
	h.ok(coord, "task", "status", "T2", "blocked", "--note", "waiting for API key")

	list := h.ok(coord, "task", "list")
	want := "NEEDS YOU\n  T2   blocked  Write docs\nIN MOTION\n  T1   started  Fix login redirect  0/2\n"
	if list != want {
		t.Fatalf("list:\n%s\nwant:\n%s", list, want)
	}
	var tj []map[string]any
	json.Unmarshal([]byte(h.ok(coord, "task", "list", "--needs-you", "--json")), &tj)
	if len(tj) != 1 || tj[0]["id"] != "T2" || !strings.Contains(tj[0]["notes"].(string), "waiting for API key") {
		t.Fatalf("needs-you json %v", tj)
	}
	if out := h.ok(coord, "task", "list", "--status", "open,ready"); out != "no tasks\n" {
		t.Fatalf("filtered %q", out)
	}

	h.ok(coord, "task", "edit", "T1", "--title", "Fix login redirect after OAuth", "--owner", "claude")
	h.expect(2, "nothing to edit", coord, "task", "edit", "T1")
	notes := filepath.Join(t.TempDir(), "n.md")
	os.WriteFile(notes, []byte("From a file.\n"), 0o644)
	h.ok(coord, "task", "edit", "T1", "--notes-file", notes)

	h.ok(coord, "task", "steps", "T1", "add", "Open a PR")
	h.ok(coord, "task", "steps", "T1", "check", "1")
	if out := h.ok(coord, "task", "steps", "T1", "check", "1"); !strings.Contains(out, "unchanged") {
		t.Fatalf("check twice %q", out)
	}
	h.ok(coord, "task", "steps", "T1", "rename", "2", "Fix the redirect")
	h.expect(1, "unknown-step", coord, "task", "steps", "T1", "check", "9")
	h.expect(2, "usage", coord, "task", "steps", "T1", "frob", "1")

	show := h.ok(coord, "task", "show", "T1")
	for _, w := range []string{"T1 Fix login redirect after OAuth", "status: started · owner: claude", "From a file.", "Steps (1/3):", "1. [x] Reproduce", "2. [ ] Fix the redirect"} {
		if !strings.Contains(show, w) {
			t.Errorf("show lacks %q:\n%s", w, show)
		}
	}

	h.ok(coord, "task", "archive", "T2")
	if out := h.ok(coord, "task", "list", "--archived"); !strings.Contains(out, "T2") {
		t.Fatalf("archived list %q", out)
	}
	h.ok(coord, "task", "unarchive", "T2")
	h.expect(1, "needs-approval", coord, "task", "delegate", "T1")

	raw, _ := os.ReadFile(filepath.Join(h.root, "projects", "demo", "TASKS.md"))
	if !strings.Contains(string(raw), "### T1 Fix login redirect after OAuth\nstatus: started · owner: claude") {
		t.Fatalf("TASKS.md:\n%s", raw)
	}
}

func TestCallerRules(t *testing.T) {
	h := newHarness(t)
	h.ok(human, "project", "new", "demo")
	h.env[caller.EnvProject] = "demo"
	h.ok(coord, "task", "add", "Mine", "--step", "Plan")
	h.ok(coord, "task", "add", "Theirs")
	// Threads link up in M6; set the link the way delegation will.
	raw := filepath.Join(h.root, "projects", "demo", "TASKS.md")
	data, _ := os.ReadFile(raw)
	os.WriteFile(raw, []byte(strings.Replace(string(data), "### T1 Mine\nstatus: open", "### T1 Mine\nstatus: open · thread: t-0001", 1)), 0o644)

	// A thread reads everything and adds/ticks steps on its own task only.
	h.ok(thr, "task", "list")
	h.ok(thr, "task", "show", "T2")
	h.ok(thr, "task", "steps", "T1", "add", "Build")
	h.ok(thr, "task", "steps", "T1", "check", "2")
	h.expect(1, "coordinator-only", thr, "task", "steps", "T2", "add", "x")
	h.expect(1, "coordinator-only", thr, "task", "steps", "T1", "remove", "1")
	h.expect(1, "coordinator-only", thr, "task", "status", "T1", "review")
	h.expect(1, "coordinator-only", thr, "task", "add", "New")
	h.expect(1, "coordinator-only", thr, "task", "edit", "T1", "--title", "x")
	h.expect(1, "coordinator-only", thr, "task", "archive", "T1")
	h.expect(1, "coordinator-only", thr, "task", "delegate", "T1")
	h.expect(1, "coordinator-only", thr, "inbox", "list")

	// Only the user accepts work: the coordinator sets done only with
	// --approved-by-user, a thread never.
	h.ok(coord, "task", "status", "T1", "review")
	h.expect(1, "human-only", coord, "task", "status", "T1", "done")
	h.expect(1, "coordinator-only", thr, "task", "status", "T1", "done", "--approved-by-user")
	h.expect(2, "", coord, "task", "status", "T1", "review", "--approved-by-user")
	h.ok(coord, "task", "status", "T1", "done", "--approved-by-user")
	if inbox := h.ok(coord, "inbox", "list"); inbox != "" {
		t.Fatalf("inbox %q", inbox)
	}

	// Agents can't write another project's tasks.
	h.ok(human, "project", "new", "other")
	h.expect(1, "other-project", coord, "task", "add", "x", "--project", "other")
	h.ok(coord, "task", "list", "--project", "other")
}

func TestContextAndCwdResolution(t *testing.T) {
	h := newHarness(t)
	h.ok(human, "project", "new", "demo", "--goal", "Ship v1")
	h.cwd = filepath.Join(h.root, "projects", "demo")
	h.ok(human, "task", "add", "First")
	out := h.ok(human, "context")
	for _, w := range []string{"## Project\nProject: demo (demo)", "Goal: Ship v1", "## Tasks\nOn deck (1)\n  T1   open     First", "## Threads\n(no threads)", "## Inbox\n(empty)", "human task.add T1 First"} {
		if !strings.Contains(out, w) {
			t.Errorf("context lacks %q:\n%s", w, out)
		}
	}
	if again := h.ok(human, "context"); again != out {
		t.Fatal("context not deterministic")
	}
	var js map[string]any
	if err := json.Unmarshal([]byte(h.ok(human, "context", "--json")), &js); err != nil || js["project"] != "demo" {
		t.Fatalf("json %v %v", js, err)
	}
	h.expect(1, "unknown-project", human, "context", "--project", "nope")
	h.expect(1, "invalid-project", human, "context", "--project", "../etc")
}

func TestBrokenTasksFileExits3(t *testing.T) {
	h := newHarness(t)
	h.ok(human, "project", "new", "demo")
	h.env[caller.EnvProject] = "demo"
	path := filepath.Join(h.root, "projects", "demo", "TASKS.md")
	os.WriteFile(path, []byte("# Tasks\n\n### T1 A\nstatus: someday\n"), 0o644)
	h.expect(3, "line 4", human, "task", "add", "B")
	h.expect(3, "line 4", human, "task", "list")
}

func TestSkillAndOpen(t *testing.T) {
	h := newHarness(t)
	out := h.ok(thr, "skill", "thread")
	if !strings.HasPrefix(out, "tm skill thread v") || !strings.Contains(out, "Stay in your worktree") {
		t.Fatalf("skill thread: %q", out[:min(len(out), 80)])
	}
	if out := h.ok(coord, "skill", "coordinator"); !strings.Contains(out, "tm context") {
		t.Fatal("skill coordinator")
	}
	h.expect(2, "no rules for role", human, "skill", "boss")
	h.expect(2, "usage", human, "skill")

	h.ok(human, "project", "new", "demo")
	// Opening needs the server (the e2e scenarios); refusals come first.
	h.expect(1, "unknown-project", human, "project", "open", "nope")
	h.expect(1, "human-only", coord, "project", "open", "demo")
}

func TestSafetySettingsInContext(t *testing.T) {
	h := newHarness(t)
	h.ok(human, "project", "new", "demo")
	if out := h.ok(human, "context", "--project", "demo"); !strings.Contains(out, "Safety: start_threads=propose · yolo=false · coordinator_approves=true") {
		t.Fatalf("defaults:\n%s", out)
	}
	cfg := filepath.Join(h.root, "config.toml")
	os.WriteFile(cfg, []byte("[projects.demo]\nstart_threads = \"auto\"\n"), 0o600)
	if out := h.ok(human, "context", "--project", "demo"); !strings.Contains(out, "start_threads=auto") {
		t.Fatalf("auto:\n%s", out)
	}
	var list []map[string]any
	json.Unmarshal([]byte(h.ok(human, "project", "list", "--json")), &list)
	if s, _ := list[0]["safety"].(map[string]any); s["start_threads"] != "auto" {
		t.Fatalf("list --json safety: %v", list)
	}
	os.WriteFile(cfg, []byte("[projects.demo]\nstart_threads = \"yes\"\n"), 0o600)
	h.expect(3, "start_threads must be", human, "context", "--project", "demo")
}

func TestServerAndSessionDispatch(t *testing.T) {
	for _, cmd := range []string{"server", "session"} {
		var out, errs strings.Builder
		e := &Env{Stdout: &out, Stderr: &errs}
		code, ok := e.Run([]string{cmd, "bogus"})
		if !ok || code != ExitUsage || !strings.Contains(errs.String(), "usage: tm "+cmd) {
			t.Fatalf("tm %s bogus: handled=%v exit %d stderr %q", cmd, ok, code, errs.String())
		}
	}
}

func TestWarnSSH(t *testing.T) {
	ssh := func(k string) string {
		if k == "SSH_CONNECTION" {
			return "10.0.0.2 51000 10.0.0.1 22"
		}
		return ""
	}
	var b strings.Builder
	warnSSH(&b, "darwin", ssh)
	if !strings.HasPrefix(b.String(), "tm: warning: ") || !strings.Contains(b.String(), "tm server restart") {
		t.Fatalf("darwin over SSH: %q", b.String())
	}
	b.Reset()
	warnSSH(&b, "linux", ssh)
	warnSSH(&b, "darwin", func(string) string { return "" })
	if b.Len() != 0 {
		t.Fatalf("warned without cause: %q", b.String())
	}
}

func TestProjectLifecycle(t *testing.T) {
	h := newHarness(t)
	h.ok(human, "project", "new", "Demo")
	for _, args := range [][]string{{"pause", "demo"}, {"archive", "demo"}, {"delete", "demo", "--yes"}} {
		h.expect(1, "human-only", coord, append([]string{"project"}, args...)...)
	}

	// Pause: no thread starts, a mark in the list; idempotent.
	if out := h.ok(human, "project", "pause", "demo"); !strings.Contains(out, "paused demo") {
		t.Fatalf("pause: %q", out)
	}
	if out := h.ok(human, "project", "pause", "demo"); !strings.Contains(out, "already paused") {
		t.Fatalf("pause again: %q", out)
	}
	if out := h.ok(human, "project", "list"); !strings.Contains(out, "Demo (paused)") {
		t.Fatalf("list: %q", out)
	}
	h.expect(1, "project-paused", coord, "thread", "start", "Fix it", "--project", "demo")
	h.ok(human, "task", "add", "Fix it", "--project", "demo")
	h.expect(1, "project-paused", coord, "task", "delegate", "T1", "--project", "demo")
	if out := h.ok(human, "project", "resume", "demo"); !strings.Contains(out, "resumed demo") {
		t.Fatalf("resume: %q", out)
	}

	// Archive: hidden, marked in the list, back with unarchive.
	h.ok(human, "project", "archive", "demo")
	if out := h.ok(human, "project", "list"); !strings.Contains(out, "Demo (archived)") {
		t.Fatalf("list: %q", out)
	}
	var list []struct {
		Slug   string
		Safety struct{ Archived bool }
	}
	if err := json.Unmarshal([]byte(h.ok(human, "project", "list", "--json")), &list); err != nil || len(list) != 1 || !list[0].Safety.Archived {
		t.Fatalf("json: %v %+v", err, list)
	}
	h.ok(human, "project", "unarchive", "demo")
	if out := h.ok(human, "project", "list"); strings.Contains(out, "archived") {
		t.Fatalf("still archived: %q", out)
	}

	// Delete: --yes without a terminal; the folder moves to the trash and
	// its settings go, so a new project of the slug starts clean.
	h.ok(human, "project", "pause", "demo")
	h.expect(2, "--yes", human, "project", "delete", "demo")
	out := h.ok(human, "project", "delete", "demo", "--yes")
	if !strings.Contains(out, "moved to "+filepath.Join(h.root, ".trash", "demo-")) {
		t.Fatalf("delete: %q", out)
	}
	if _, err := os.Stat(filepath.Join(h.root, "projects", "demo")); !os.IsNotExist(err) {
		t.Fatalf("folder still there: %v", err)
	}
	trash, _ := filepath.Glob(filepath.Join(h.root, ".trash", "demo-*", "PROJECT.md"))
	if len(trash) != 1 {
		t.Fatalf("trash %v", trash)
	}
	h.expect(1, "unknown-project", human, "project", "archive", "demo")
	h.ok(human, "project", "new", "Demo")
	if out := h.ok(human, "project", "list"); strings.Contains(out, "paused") {
		t.Fatalf("new project inherited pause: %q", out)
	}
}
