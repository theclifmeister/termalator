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
	// The sidebar's tree: alpha (the first, listed) open with its idle
	// coordinator, beta closed without one.
	w.WaitFor("▾○ alpha", wait)
	w.WaitFor("  ○ coordinator", wait)
	w.WaitFor("▸· beta", wait)

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

// TestSmokeSidebar: the project tree in the sidebar (docs/SPEC.md §4)
// on every screen, shared by the consoles of view main. ▸ ▾ open and
// close a project in both consoles; a project row shows its dashboard, a
// coordinator row attaches the coordinator, a thread row watches the
// thread, from the dashboard and from (split) panes; the sidebar stays
// left of split panes; prefix } widens it and a drag on its border moves
// it, both resizing the panes and kept in ui.json; a narrow window gets
// the slim strip. tm attach shows the sidebar too, and a click on it
// switches that console to the full UI on view main.
func TestSmokeSidebar(t *testing.T) {
	env, projDir, _ := threadEnv(t)
	demo := "demo"
	beta, betaDir := newProject(env, "Beta")
	env.Trust(betaDir)
	th := startThread(t, env, projDir)

	w := env.Window(120, 30)
	// beta, the first project, is current: open, with its coordinator
	// (none runs yet); demo is closed, with its one thread.
	w.WaitFor("▾· "+beta, wait)
	w.WaitFor("  · coordinator", wait)
	w.WaitFor("▸· "+demo+"              1", wait)
	w2 := env.Window(100, 26)
	w2.WaitFor("▸· "+demo, wait)

	// ▸ opens demo in both consoles of the view; ▾ in the other closes it.
	both := []*Window{w, w2}
	w.Click(0, sideRow(t, w.Screen(), demo))
	for _, x := range both {
		x.WaitUntil("demo open", wait, func(sc string) bool { return treeRow(sc, demo, "t-0001 Small fix") >= 0 })
	}
	w2.Click(0, sideRow(t, w2.Screen(), demo))
	for _, x := range both {
		x.WaitUntil("demo closed", wait, func(sc string) bool { return treeRow(sc, demo, "coordinator") < 0 })
	}

	// A project row shows that project's dashboard, in both consoles.
	w.Click(4, sideRow(t, w.Screen(), demo))
	for _, x := range both {
		x.WaitUntil("demo's dashboard", wait, func(sc string) bool {
			return strings.Contains(sc, " "+demo+" ──") && strings.Contains(sc, "t-0001 Small fix") && treeRow(sc, demo, "coordinator") >= 0
		})
	}
	// beta's row, then its coordinator's row, starts and attaches the
	// coordinator; the panes get the window less the sidebar's 24 columns.
	clickCoordinator(t, w, beta)
	w.WaitUntil("attached to beta", agentWait, func(sc string) bool { return lastLine(sc, beta+" coordinator") })
	w.WaitFor("Fake Claude Code", agentWait)
	b := coordinatorOf(t, env, beta)
	waitPaneSize(t, env, b, 96, 29)

	// A split: the sidebar stays, the panes share the 96 columns.
	w.Prefix("%")
	w.WaitUntil("two panes", wait, func(sc string) bool { return lastLine(sc, "pane 2/2") })
	waitPaneSize(t, env, b, 48, 29)
	for i, l := range strings.Split(w.Screen(), "\n")[:29] {
		if r := []rune(l); len(r) <= 72 || r[23] != '│' || r[72] != '│' {
			t.Fatalf("row %d lacks the sidebar's border or the divider:\n%s", i, w.Screen())
		}
	}

	// From the split: ▸ opens demo in place, and its thread's row watches
	// the thread, still in the attach view.
	w.Click(0, sideRow(t, w.Screen(), demo))
	w.WaitUntil("demo open", wait, func(sc string) bool { return treeRow(sc, demo, "t-0001") >= 0 && lastLine(sc, "pane 2/2") })
	w.Click(5, treeRow(w.Screen(), demo, "t-0001"))
	for _, x := range both {
		x.WaitUntil("watching t-0001", wait, func(sc string) bool { return lastLine(sc, "watch-only") && !lastLine(sc, "pane ") })
	}
	// No console sized the thread yet: the first to watch it fills it.
	waitPaneSize(t, env, th, 96, 29)
	// beta's row, from the pane, shows beta's dashboard on both consoles;
	// its coordinator's row attaches the coordinator again.
	w.Click(4, sideRow(t, w.Screen(), beta))
	for _, x := range both {
		x.WaitUntil("beta's dashboard", wait, func(sc string) bool { return strings.Contains(sc, " "+beta+" ──") })
	}
	clickCoordinator(t, w, beta)
	w.WaitUntil("attached to beta", wait, func(sc string) bool { return lastLine(sc, beta+" coordinator") })

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

	// A narrow window: the slim strip of projects, 7 columns, never
	// nothing; beta, the focused pane's, is marked.
	w.Resize(70, 30)
	waitPaneSize(t, env, b, 63, 29)
	w.WaitFor("▸○beta│", wait)
	w.WaitFor(" ·demo│", wait)
	w.Resize(120, 30)
	w.Detach()
	for _, x := range both {
		x.WaitFor("SESSIONS", wait)
	}

	// tm attach on the thread: watch-only, with the sidebar. A click on
	// demo's coordinator hands the console over to view main, which
	// starts and shows the coordinator, on every console of the view.
	w3 := env.Attach(120, 30, th.ID)
	w3.WaitUntil("tm attach watching", wait, func(sc string) bool { return lastLine(sc, "watch-only") })
	w3.WaitFor(" PROJECTS 2", wait)
	w3.Click(4, treeRow(w3.Screen(), demo, "coordinator"))
	for _, x := range []*Window{w3, w, w2} {
		x.WaitUntil("on demo's coordinator", agentWait, func(sc string) bool { return lastLine(sc, demo+" coordinator") })
	}
	coordinatorOf(t, env, demo)
	w3.Detach() // a full console now: back to the dashboard, everywhere
	for _, x := range []*Window{w3, w, w2} {
		x.WaitFor("SESSIONS", wait)
		x.Type("q")
		x.WaitExit(wait)
	}
}

// sideRow is the screen row where the sidebar lists slug.
func sideRow(t *testing.T, screen, slug string) int {
	t.Helper()
	if i := projectRow(screen, slug); i >= 0 {
		return i
	}
	t.Fatalf("the sidebar doesn't list %s:\n%s", slug, screen)
	return -1
}

// projectRow is the screen row of slug's project row in the sidebar, -1
// for none.
func projectRow(screen, slug string) int {
	for i, l := range strings.Split(screen, "\n") {
		if r := []rune(l); len(r) > 3 && strings.HasPrefix(strings.TrimSpace(string(r[2:min(len(r), 23)])), slug) {
			return i
		}
	}
	return -1
}

// treeRow is the screen row of the row under slug's project in the
// sidebar's tree whose text starts with label ("coordinator", a thread
// id); -1 when slug is closed or has none.
func treeRow(screen, slug, label string) int {
	lines := strings.Split(screen, "\n")
	for i := projectRow(screen, slug) + 1; i > 0 && i < len(lines); i++ {
		r := []rune(lines[i])
		if len(r) < 4 || r[0] != ' ' || r[1] != ' ' {
			return -1
		}
		if strings.HasPrefix(string(r[4:min(len(r), 23)]), label) {
			return i
		}
	}
	return -1
}

// clickCoordinator clicks slug's row in the sidebar, which shows its
// dashboard, then its coordinator's row, which attaches the coordinator
// (started when none runs).
func clickCoordinator(t *testing.T, w *Window, slug string) {
	t.Helper()
	w.Click(4, sideRow(t, w.Screen(), slug))
	y := -1
	w.WaitUntil(slug+"'s coordinator row", wait, func(sc string) bool { y = treeRow(sc, slug, "coordinator"); return y >= 0 })
	w.Click(4, y)
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
