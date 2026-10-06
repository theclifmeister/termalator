package cli

// Integration tests: they build the real tm binary and run it as a
// subprocess against an isolated TERMINATR_HOME, checking output, --json
// and exit codes, and the caller checks as the session environment sets
// them. Nothing here needs the server. When project calls move to the
// server, these tests plug into the shared end-to-end harness (M1's
// internal/e2e) to start one.

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

var tmBin string // built once in TestMain; "" if the build failed

func TestMain(m *testing.M) {
	if dir := os.Getenv("TM_FAKE_LAUNCHCTL_DIR"); dir != "" && filepath.Base(os.Args[0]) == "launchctl" {
		os.Exit(fakeLaunchctl(dir, os.Args[1:]))
	}
	code := func() int {
		flag.Parse()
		if testing.Short() {
			return m.Run()
		}
		dir, err := os.MkdirTemp("", "tm-bin-")
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		defer os.RemoveAll(dir)
		bin := filepath.Join(dir, "tm")
		// The Makefile's environment (PKG_CONFIG_PATH, CGO_CFLAGS) is
		// inherited, so `make test` builds against the pinned libghostty.
		out, err := exec.Command("go", "build", "-o", bin, "../../cmd/tm").CombinedOutput()
		if err != nil {
			fmt.Fprintf(os.Stderr, "building tm failed; binary tests will fail (run make test):\n%s\n", out)
		} else {
			tmBin = bin
		}
		return m.Run()
	}()
	os.Exit(code)
}

// tmProc runs the binary with a clean environment: only PATH, HOME, the
// isolated TERMINATR_HOME and whatever the test adds.
type tmProc struct {
	t    *testing.T
	home string
	cwd  string
	env  []string
}

func newProc(t *testing.T) *tmProc {
	t.Helper()
	if testing.Short() {
		t.Skip("binary tests are skipped with -short")
	}
	if tmBin == "" {
		t.Fatal("tm binary was not built (see the TestMain output)")
	}
	p := &tmProc{t: t, home: t.TempDir(), cwd: t.TempDir()}
	// A command may have started a server: it must not outlive the test.
	t.Cleanup(func() { p.run("", "server", "stop", "--yes") })
	return p
}

// as returns a copy that runs as a hosted agent session.
func (p *tmProc) as(role, project, thread string) *tmProc {
	q := *p
	q.env = []string{"TERMINATR_SESSION=s-1", "TERMINATR_ROLE=" + role, "TERMINATR_PROJECT=" + project}
	if thread != "" {
		q.env = append(q.env, "TERMINATR_THREAD="+thread)
	}
	return &q
}

func (p *tmProc) run(stdin string, args ...string) (int, string, string) {
	p.t.Helper()
	cmd := exec.Command(tmBin, args...)
	cmd.Dir = p.cwd
	cmd.Env = append([]string{"PATH=" + os.Getenv("PATH"), "HOME=" + p.home, "TERMINATR_HOME=" + filepath.Join(p.home, "tm"),
		// A server started here stops itself when the test process is gone.
		"TERMINATR_TEST_OWNER=" + strconv.Itoa(os.Getpid()),
		// Never through the user's launchd (macOS).
		"TERMINATR_LAUNCHD=off"}, p.env...)
	cmd.Stdin = strings.NewReader(stdin)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	code := 0
	if x, ok := err.(*exec.ExitError); ok {
		code = x.ExitCode()
	} else if err != nil {
		p.t.Fatal(err)
	}
	return code, out.String(), errb.String()
}

func (p *tmProc) ok(args ...string) string {
	p.t.Helper()
	code, out, errs := p.run("", args...)
	if code != 0 {
		p.t.Fatalf("tm %s: exit %d\nstdout: %s\nstderr: %s", strings.Join(args, " "), code, out, errs)
	}
	return out
}

func (p *tmProc) want(code int, stderr string, args ...string) {
	p.t.Helper()
	c, out, errs := p.run("", args...)
	if c != code || !strings.Contains(errs, stderr) {
		p.t.Fatalf("tm %s: exit %d stderr %q stdout %q; want exit %d with %q", strings.Join(args, " "), c, errs, out, code, stderr)
	}
}

func TestBinaryProjectAndTasks(t *testing.T) {
	tm := newProc(t)
	if _, err := os.Stat(filepath.Join(tm.home, ".terminatr")); err == nil {
		t.Fatal("touched the real-home default")
	}
	tm.want(2, "usage: tm project", "project")
	tm.ok("project", "new", "Demo", "--goal", "Try tm")
	if _, err := os.Stat(filepath.Join(tm.home, ".terminatr")); err == nil {
		t.Fatal("ignored TERMINATR_HOME")
	}
	var proj []map[string]any
	if err := json.Unmarshal([]byte(tm.ok("project", "list", "--json")), &proj); err != nil || len(proj) != 1 || proj[0]["goal"] != "Try tm" {
		t.Fatalf("project list --json: %v %v", proj, err)
	}

	tm.ok("task", "add", "Fix login", "--project", "demo", "--step", "Reproduce", "--step", "Fix")
	code, out, _ := tm.run(`[{"title":"Docs","status":"ready"},{"title":""}]`, "task", "add", "--json", "--project", "demo")
	var res []map[string]string
	json.Unmarshal([]byte(out), &res)
	if code != 1 || len(res) != 2 || res[0]["id"] != "T2" || res[1]["code"] != "empty-title" {
		t.Fatalf("bulk add: exit %d %s", code, out)
	}
	tm.want(2, "unknown flag", "task", "list", "--project", "demo", "--frobnicate")
	tm.want(1, "unknown-task", "task", "show", "T9", "--project", "demo")
	tm.want(2, "no project here", "task", "list")

	// The cwd picks the project, as for the coordinator.
	tm.cwd = filepath.Join(tm.home, "tm", "projects", "demo")
	tm.ok("task", "status", "T1", "started")
	if out := tm.ok("task", "status", "T1", "started"); !strings.Contains(out, "unchanged") {
		t.Fatalf("idempotent status: %q", out)
	}
	var tk struct {
		Changed bool `json:"changed"`
		Task    struct {
			ID    string `json:"id"`
			Steps []struct {
				N    int    `json:"n"`
				Text string `json:"text"`
				Done bool   `json:"done"`
			} `json:"steps"`
		} `json:"task"`
	}
	json.Unmarshal([]byte(tm.ok("task", "steps", "T1", "check", "1", "--json")), &tk)
	if !tk.Changed || tk.Task.ID != "T1" || len(tk.Task.Steps) != 2 || !tk.Task.Steps[0].Done {
		t.Fatalf("steps check --json: %+v", tk)
	}
	if out := tm.ok("task", "list"); out != "IN MOTION\n  T1   started  Fix login  1/2\nON DECK\n  T2   ready    Docs\n" {
		t.Fatalf("list:\n%s", out)
	}
}

func TestBinaryCallerChecks(t *testing.T) {
	tm := newProc(t)
	tm.ok("project", "new", "demo")
	coord := tm.as("coordinator", "demo", "")
	thread := tm.as("thread", "demo", "t-0001")

	coord.ok("task", "add", "Mine", "--step", "Plan")
	coord.ok("task", "add", "Theirs")
	tasksMD := filepath.Join(tm.home, "tm", "projects", "demo", "TASKS.md")
	data, _ := os.ReadFile(tasksMD)
	os.WriteFile(tasksMD, bytes.Replace(data, []byte("status: open"), []byte("status: open · thread: t-0001"), 1), 0o644)

	thread.ok("task", "list")
	thread.ok("task", "steps", "T1", "add", "Build")
	thread.ok("task", "steps", "T1", "check", "1")
	thread.want(1, "coordinator-only", "task", "steps", "T2", "check", "1")
	thread.want(1, "coordinator-only", "task", "status", "T1", "review")
	thread.want(1, "coordinator-only", "task", "add", "x")

	coord.want(1, "human-only", "project", "new", "x")
	coord.want(1, "human-only", "task", "status", "T1", "done")
	thread.want(1, "coordinator-only", "task", "status", "T1", "done", "--approved-by-user")
	coord.ok("task", "status", "T1", "done", "--approved-by-user")
	// A shell session inside terminatr counts as the human.
	shell := tm.as("shell", "demo", "")
	shell.ok("task", "status", "T2", "done")
	journal, _ := os.ReadFile(filepath.Join(tm.home, "tm", "projects", "demo", "JOURNAL.md"))
	for _, w := range []string{"t-0001 task.steps.add T1 2 Build", "coordinator task.status T1 done (approved by the user)", "human task.status T2 done"} {
		if !strings.Contains(string(journal), w) {
			t.Errorf("journal lacks %q:\n%s", w, journal)
		}
	}
}

// TestBinaryContextDeterministic is the base of the "clearing the
// coordinator loses nothing" invariant (§16.6): after scripted actions,
// `tm context` is a pure function of the files, so a coordinator that runs
// it after /clear sees exactly what it saw before.
func TestBinaryContextDeterministic(t *testing.T) {
	tm := newProc(t)
	tm.ok("project", "new", "demo", "--goal", "Ship v1")
	coord := tm.as("coordinator", "demo", "")
	coord.ok("task", "add", "Fix login", "--notes", "Users land on /home", "--step", "Reproduce")
	coord.ok("task", "add", "Docs")
	coord.ok("task", "status", "T2", "blocked", "--note", "needs review")
	coord.want(1, "human-only", "task", "status", "T1", "done")
	dir := filepath.Join(tm.home, "tm", "projects", "demo")
	os.WriteFile(filepath.Join(dir, "CONTEXT.md"), []byte("# Context\n\nPlan: login first.\n"), 0o644)

	first := coord.ok("context")
	firstJSON := coord.ok("context", "--json")
	for _, w := range []string{"Goal: Ship v1", "Plan: login first.", "Needs you (1)\n  T2   blocked  Docs", "coordinator task.add T1 Fix login"} {
		if !strings.Contains(first, w) {
			t.Errorf("context lacks %q:\n%s", w, first)
		}
	}

	// Reading changes nothing, and file times and directory order don't
	// matter: touch every file and run again.
	filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err == nil {
			now := info.ModTime().Add(3600e9)
			os.Chtimes(p, now, now)
		}
		return nil
	})
	if again := coord.ok("context"); again != first {
		t.Fatalf("context changed between runs:\n%s\n---\n%s", first, again)
	}
	if again := coord.ok("context", "--json"); again != firstJSON {
		t.Fatal("context --json changed between runs")
	}
	// The human and the coordinator see the same context.
	if human := tm.ok("context", "--project", "demo"); human != first {
		t.Fatal("context depends on the caller")
	}
}
