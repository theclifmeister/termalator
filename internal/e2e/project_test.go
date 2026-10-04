package e2e

// M4 scenarios with projects: tm project open, the project switcher, the
// server's caller checks (docs/SPEC.md §11.1) and the "clearing the
// coordinator loses nothing" invariant (§16.6).

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestSmokeProjectOpenAndSwitch: tm project open starts a project's
// coordinator once; from the dashboard, p switches project and ] and [
// cycle through the projects' coordinators.
func TestSmokeProjectOpenAndSwitch(t *testing.T) {
	env := New(t)
	env.FakeClaude()
	alpha, alphaDir := newProject(env, "Alpha")
	beta, betaDir := newProject(env, "Beta")
	env.Trust(alphaDir, betaDir)

	// Without a terminal it prints the session id; again, the same one.
	id := strings.TrimSpace(env.MustCLI("project", "open", alpha))
	if again := strings.TrimSpace(env.MustCLI("project", "open", alpha)); again != id {
		t.Fatalf("project open twice: %s, then %s", id, again)
	}
	coord := &Session{ID: id}
	info, _ := env.Info(coord)
	coord.PID = info.PID
	env.track(info.PID, "coordinator "+id)
	if info.Role != "coordinator" || info.Project != alpha || info.Cwd != alphaDir {
		t.Fatalf("coordinator session %+v", info)
	}
	env.WaitState(coord, "idle", agentWait)

	w := env.Window(100, 30)
	w.WaitFor("alpha        coordinator                    idle", wait)
	w.WaitFor("beta         coordinator                    —", wait)

	// p, down, enter: beta's coordinator starts and is attached.
	w.Type("p")
	w.WaitFor("enter open its coordinator", wait)
	w.Key(keyDown)
	w.Key(Enter)
	w.WaitUntil("attached to beta", agentWait, func(sc string) bool { return lastLine(sc, beta+" coordinator") })
	w.WaitFor("Fake Claude Code", agentWait)

	// Ctrl+\ then [: the previous project, alpha; then ]: beta again.
	w.Key(CtrlBackslash)
	w.WaitFor("PROJECTS", wait)
	w.Type("[")
	w.WaitUntil("attached to alpha", wait, func(sc string) bool { return lastLine(sc, id+" · "+alpha+" coordinator") })
	w.Key(CtrlBackslash)
	w.WaitFor("PROJECTS", wait)
	w.Type("]")
	w.WaitUntil("attached to beta", wait, func(sc string) bool { return lastLine(sc, beta+" coordinator") })
	w.Key(CtrlBackslash)
	w.WaitFor("PROJECTS", wait)
	w.Type("q")
	w.WaitExit(wait)

	n := 0
	for _, s := range env.Sessions() {
		env.track(s.PID, "session "+s.ID)
		if s.Role == "coordinator" {
			n++
		}
	}
	if n != 2 {
		t.Fatalf("%d coordinators, want 2 (one per project)", n)
	}

	// tm project open on a terminal attaches, with the status bar.
	w2 := env.Window(100, 30, "project", "open", beta)
	w2.WaitUntil("attached to beta", wait, func(sc string) bool { return lastLine(sc, beta+" coordinator") })
	w2.Key(CtrlBackslash)
	w2.WaitExit(wait)
}

// scripts writes fake-agent scripts into a fresh directory, which the
// server's sessions get as FAKEAGENT_SCRIPTS. Call before the server
// starts.
func (e *Env) scripts(files map[string]string) {
	e.T.Helper()
	dir := filepath.Join(e.T.TempDir(), "scripts")
	os.MkdirAll(dir, 0o755)
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name+".toml"), []byte(body), 0o644); err != nil {
			e.T.Fatal(err)
		}
	}
	e.Setenv("FAKEAGENT_SCRIPTS", dir)
}

// TestSmokeServerCallerCheck: the server decides who calls tm task from
// the process tree (docs/SPEC.md §11.1). A coordinator that drops the
// session variables is still a coordinator and can't set done; a hosted
// shell session is the human and can.
func TestSmokeServerCallerCheck(t *testing.T) {
	env := New(t)
	env.FakeClaude()
	env.scripts(map[string]string{"sneak": `
[[step]]
do = "run"
cmd = 'env -u TERMALATOR_SESSION -u TERMALATOR_ROLE -u TERMALATOR_PROJECT "$TERMALATOR_BIN" task status T1 done --project demo; echo "exit=$?"'
`})
	slug, dir := newProject(env, "Demo")
	env.Trust(dir)
	env.MustCLI("task", "add", "Finish it", "--project", slug)
	s := env.StartAgent("claude", dir, "--role", "coordinator", "--project", slug)
	env.WaitState(s, "idle", agentWait)
	env.Prompt(s, "run sneak")
	env.WaitFor(s, "exit=1", agentWait)
	if out := env.MustCLI("task", "show", "T1", "--project", slug, "--json"); strings.Contains(out, `"status": "done"`) {
		t.Fatalf("a coordinator without its variables set done:\n%s", out)
	}
	if out := env.MustCLI("inbox", "list", "--project", slug); !strings.Contains(out, "confirm-done") {
		t.Fatalf("no confirmation raised:\n%s", out)
	}

	// The human, from a shell session inside termalator.
	sh := env.Start("shell")
	env.WaitFor(sh, "$", wait)
	env.Keys(sh, `"$TERMALATOR_BIN" task status T1 done --project demo; echo "rc=$?"`+"\r")
	env.WaitFor(sh, "rc=0", wait)
	if out := env.MustCLI("task", "show", "T1", "--project", slug, "--json"); !strings.Contains(out, `"status": "done"`) {
		t.Fatalf("the human's done didn't stick:\n%s", out)
	}
}

// TestSmokeClearLosesNothing is the invariant of docs/SPEC.md §16.6: a
// coordinator works (tasks, a done request, inbox handling, journal
// lines); after /clear its SessionStart re-injection is the role rules
// plus tm context, byte for byte as captured before, and no inbox item
// was lost or handled twice.
func TestSmokeClearLosesNothing(t *testing.T) {
	env := New(t)
	env.FakeClaude()
	tm := `"$TERMALATOR_BIN" `
	env.scripts(map[string]string{"work": `
[[step]]
do = "run"
cmd = '` + tm + `task add "Alpha" --step one --step two && ` + tm + `task add "Beta" --notes "second" && ` + tm + `task add "Gamma"'

[[step]]
do = "run"
cmd = '` + tm + `task status T1 started && ` + tm + `task steps T1 check 1 && ` + tm + `task status T2 review'

[[step]]
do = "run"
cmd = '` + tm + `task status T2 done; ` + tm + `task status T3 done; ` + tm + `inbox list'

[[step]]
do = "run"
cmd = '` + tm + `inbox done "$(` + tm + `inbox list | grep T3 | cut -d" " -f1)" && echo WORK-DONE'
`})
	slug, dir := newProject(env, "Demo")
	env.Trust(dir)
	s := env.StartAgent("claude", dir, "--role", "coordinator", "--project", slug)
	env.WaitState(s, "idle", agentWait)
	env.Prompt(s, "run work")
	env.WaitFor(s, "WORK-DONE", agentWait)
	env.WaitState(s, "idle", agentWait)

	inboxBefore := env.MustCLI("inbox", "list", "--project", slug)
	doneBefore := listDir(t, filepath.Join(dir, "inbox", "done"))
	if !strings.Contains(inboxBefore, "T2") || strings.Contains(inboxBefore, "T3") || len(doneBefore) != 1 {
		t.Fatalf("after the work: inbox\n%s\ndone %v", inboxBefore, doneBefore)
	}
	want := env.MustCLI("skill", "coordinator") + "\n\n" + env.MustCLI("context", "--project", slug)

	n := len(env.FakeRecords("context"))
	env.Keys(s, "/clear\r")
	r := env.WaitFake("context", agentWait, func(r FakeRecord) bool { return r.Str("source") == "clear" })
	if got := r.Str("text"); got != want {
		t.Fatalf("context after /clear differs from rules + tm context before it\n--- got\n%s\n--- want\n%s", got, want)
	}
	if len(env.FakeRecords("context")) != n+1 {
		t.Errorf("clear injected the context %d times", len(env.FakeRecords("context"))-n)
	}
	if after := env.MustCLI("inbox", "list", "--project", slug); after != inboxBefore {
		t.Fatalf("inbox changed across /clear:\n%s\nwas\n%s", after, inboxBefore)
	}
	if after := listDir(t, filepath.Join(dir, "inbox", "done")); strings.Join(after, ",") != strings.Join(doneBefore, ",") {
		t.Fatalf("handled items changed across /clear: %v, was %v", after, doneBefore)
	}
}

func listDir(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}
