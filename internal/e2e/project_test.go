package e2e

// M4 scenarios with projects: tm project open, the project switcher, the
// server's caller checks (docs/SPEC.md §11.1) and the "clearing the
// coordinator loses nothing" invariant (§16.6).

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/theclifmeister/termalator/internal/mdfile"
	"github.com/theclifmeister/termalator/internal/project"
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
	w.WaitFor("alpha        coordinator                    ○ idle", wait)
	w.WaitFor("beta         coordinator                    —", wait)

	// p, down, enter: beta's coordinator starts and is attached.
	w.Type("p")
	w.WaitFor("enter open its coordinator", wait)
	w.Key(keyDown)
	w.Key(Enter)
	w.WaitUntil("attached to beta", agentWait, func(sc string) bool { return lastLine(sc, beta+" coordinator") })
	w.WaitFor("Fake Claude Code", agentWait)

	// Prefix then [: the previous project, alpha, straight from the
	// session; then prefix ]: beta again.
	w.Prefix("[")
	w.WaitUntil("attached to alpha", wait, func(sc string) bool { return lastLine(sc, id+" · "+alpha+" coordinator") })
	w.Prefix("]")
	w.WaitUntil("attached to beta", wait, func(sc string) bool { return lastLine(sc, beta+" coordinator") })
	// Prefix p: the switcher, over the dashboard.
	w.Prefix("p")
	w.WaitFor("enter open its coordinator", wait)
	w.Key(keyEsc)
	w.WaitFor("SESSIONS", wait)
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
	w2.Detach()
	w2.WaitExit(wait)
}

// TestSmokeSidebar: the projects sidebar (docs/SPEC.md §4) on every
// screen. A click on a project opens its coordinator, from the dashboard
// and while attached, also from a split layout; the sidebar stays left
// of split panes; prefix } widens it and a drag on its border moves it,
// both resizing the panes and kept in ui.json; a narrow window gets the
// slim strip.
func TestSmokeSidebar(t *testing.T) {
	env := New(t)
	env.FakeClaude()
	alpha, alphaDir := newProject(env, "Alpha")
	beta, betaDir := newProject(env, "Beta")
	env.Trust(alphaDir, betaDir)

	w := env.Window(120, 30)
	w.WaitFor("beta         coordinator", wait)
	// From the dashboard: a click on alpha opens its coordinator; the
	// panes get the window less the sidebar's 24 columns.
	w.Click(3, sideRow(t, w.Screen(), alpha))
	w.WaitUntil("attached to alpha", agentWait, func(sc string) bool { return lastLine(sc, alpha+" coordinator") })
	w.WaitFor("Fake Claude Code", agentWait)
	w.WaitFor("▸○ "+alpha, wait) // the current project is marked
	a := coordinatorOf(t, env, alpha)
	waitPaneSize(t, env, a, 96, 29)

	// A split: the sidebar stays, the panes share the 96 columns, and
	// alpha stays the current project.
	w.Prefix("%")
	w.WaitUntil("two panes", wait, func(sc string) bool { return lastLine(sc, "pane 2/2") })
	waitPaneSize(t, env, a, 48, 29)
	for i, l := range strings.Split(w.Screen(), "\n")[:29] {
		if r := []rune(l); len(r) <= 72 || r[23] != '│' || r[72] != '│' {
			t.Fatalf("row %d lacks the sidebar's border or the divider:\n%s", i, w.Screen())
		}
	}
	if !strings.Contains(w.Screen(), "▸○ "+alpha) {
		t.Fatalf("alpha not marked current in a split:\n%s", w.Screen())
	}

	// From the split: a click on beta opens beta's coordinator.
	w.Click(3, sideRow(t, w.Screen(), beta))
	w.WaitUntil("attached to beta", agentWait, func(sc string) bool {
		return lastLine(sc, beta+" coordinator") && !lastLine(sc, "pane ")
	})
	w.WaitFor("Fake Claude Code", agentWait)
	b := coordinatorOf(t, env, beta)
	waitPaneSize(t, env, b, 96, 29)

	// Prefix } widens the sidebar: a layout change, so the pane follows,
	// and ui.json keeps the width.
	w.Prefix("}")
	waitPaneSize(t, env, b, 94, 29)
	if !Poll(wait, func() bool { return strings.Contains(readFile(env.Home, "ui.json"), `"width": 26`) }) {
		t.Fatalf("ui.json: %s", readFile(env.Home, "ui.json"))
	}
	// Dragging its border to column 29 makes it 30 wide.
	w.Drag(25, 29, 5)
	waitPaneSize(t, env, b, 90, 29)
	if !Poll(wait, func() bool { return strings.Contains(readFile(env.Home, "ui.json"), `"width": 30`) }) {
		t.Fatalf("ui.json after the drag: %s", readFile(env.Home, "ui.json"))
	}

	// A narrow window: the slim strip, 7 columns, never nothing.
	w.Resize(70, 30)
	waitPaneSize(t, env, b, 63, 29)
	w.WaitFor("▸○beta│", wait)
	w.WaitFor(" ○alph│", wait)

	w.Detach()
	w.WaitFor("SESSIONS", wait)
	w.Type("q")
	w.WaitExit(wait)
}

// sideRow is the screen row where the sidebar lists slug.
func sideRow(t *testing.T, screen, slug string) int {
	t.Helper()
	for i, l := range strings.Split(screen, "\n") {
		if r := []rune(l); len(r) > 3 && strings.HasPrefix(strings.TrimSpace(string(r[2:min(len(r), 23)])), slug) {
			return i
		}
	}
	t.Fatalf("the sidebar doesn't list %s:\n%s", slug, screen)
	return -1
}

// coordinatorOf is the project's coordinator session.
func coordinatorOf(t *testing.T, env *Env, slug string) *Session {
	t.Helper()
	var s *Session
	Poll(wait, func() bool {
		for _, info := range env.Sessions() {
			if info.Role == "coordinator" && info.Project == slug {
				s = &Session{ID: info.ID, PID: info.PID}
			}
		}
		return s != nil
	})
	if s == nil {
		t.Fatalf("no coordinator for %s", slug)
	}
	env.track(s.PID, "coordinator "+s.ID)
	return s
}

func readFile(dir, name string) string {
	b, _ := os.ReadFile(filepath.Join(dir, name))
	return string(b)
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
// session variables is still a coordinator and can't set done without
// the user's approval; a hosted shell session is the human and can.
func TestSmokeServerCallerCheck(t *testing.T) {
	env := New(t)
	env.FakeClaude()
	env.scripts(map[string]string{"sneak": `
[[step]]
do = "run"
cmd = 'env -u TERMALATOR_SESSION -u TERMALATOR_ROLE -u TERMALATOR_PROJECT "$TERMALATOR_BIN" task status T1 done --project demo 2>&1; echo "exit=$?"'
`})
	slug, dir := newProject(env, "Demo")
	env.Trust(dir)
	env.MustCLI("task", "add", "Finish it", "--project", slug)
	s := env.StartAgent("claude", dir, "--role", "coordinator", "--project", slug)
	env.WaitState(s, "idle", agentWait)
	env.Prompt(s, "run sneak")
	env.WaitFor(s, "exit=1", agentWait)
	env.WaitFor(s, "human-only", agentWait)
	if out := env.MustCLI("task", "show", "T1", "--project", slug, "--json"); strings.Contains(out, `"status": "done"`) {
		t.Fatalf("a coordinator without its variables set done:\n%s", out)
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
// coordinator works (tasks, a done the user approved, inbox handling,
// journal lines); after /clear its SessionStart re-injection is the role rules
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
cmd = '` + tm + `task status T2 done --approved-by-user; ` + tm + `task status T3 done; ` + tm + `inbox list'

[[step]]
do = "run"
cmd = '` + tm + `inbox done "$(` + tm + `inbox list | grep t-0003 | cut -d" " -f1)" && echo WORK-DONE'
`})
	slug, dir := newProject(env, "Demo")
	env.Trust(dir)
	// Two items from threads, as the ticker writes them.
	for _, it := range []project.Item{
		{ID: "20261004T120000Z-report-t-0002", Kind: "report", Subject: "t-0002", Summary: "t-0002 handed in report 1"},
		{ID: "20261004T120001Z-report-t-0003", Kind: "report", Subject: "t-0003", Summary: "t-0003 handed in report 1"},
	} {
		os.MkdirAll(filepath.Join(dir, "inbox"), 0o755)
		if err := mdfile.Write(filepath.Join(dir, "inbox", it.ID+".md"), it, nil); err != nil {
			t.Fatal(err)
		}
	}
	s := env.StartAgent("claude", dir, "--role", "coordinator", "--project", slug)
	env.WaitState(s, "idle", agentWait)
	env.Prompt(s, "run work")
	env.WaitFor(s, "WORK-DONE", agentWait)
	// The server nudges the idle coordinator about the items once; let
	// that turn end before /clear.
	env.WaitFake("prompt", agentWait, func(r FakeRecord) bool { return strings.HasPrefix(r.Str("text"), "[tm] ") })
	env.WaitState(s, "idle", agentWait)

	inboxBefore := env.MustCLI("inbox", "list", "--project", slug)
	doneBefore := listDir(t, filepath.Join(dir, "inbox", "done"))
	if !strings.Contains(inboxBefore, "t-0002") || strings.Contains(inboxBefore, "t-0003") || len(doneBefore) != 1 {
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
