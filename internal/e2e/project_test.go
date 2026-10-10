package e2e

// M4 scenarios with projects: tm project open, the
// server's caller checks (docs/SPEC.md §11.1) and the "clearing the
// coordinator loses nothing" invariant (§15.6).

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/theclifmeister/terminatr/internal/mdfile"
	"github.com/theclifmeister/terminatr/internal/project"
)

// TestSmokeProjectOpenAndSwitch: tm project open starts a project's
// coordinator once; from the dashboard, p switches project and ] and [
// cycle through the projects' coordinators.
func TestSmokeProjectOpenAndSwitch(t *testing.T) {
	env := New(t)
	env.FakeClaude()
	alpha, alphaDir := newProject(env, "alpha")
	beta, betaDir := newProject(env, "beta")
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
	// The sidebar's tree: alpha (the first, listed) with its idle
	// coordinator, beta with none running.
	w.WaitUntil("the tree", wait, func(sc string) bool {
		a, b := projectRow(sc, alpha), projectRow(sc, beta)
		lines := strings.Split(sc, "\n")
		coord := func(l, glyph string) bool {
			l = sideCut(l, 100)
			return strings.HasPrefix(l, " └─ coordinator ") && strings.HasSuffix(l, glyph+" │")
		}
		return a >= 0 && b == a+2 && coord(lines[a+1], "○") && coord(lines[b+1], "·")
	})

	// ]: beta's coordinator starts and is attached.
	w.Type("]")
	w.WaitUntil("attached to beta", agentWait, func(sc string) bool { return lastLine(sc, beta+" coordinator") })
	w.WaitFor("Fake Claude Code", agentWait)

	// Prefix then [: the previous project, alpha, straight from the
	// session; then prefix ]: beta again.
	w.Prefix("[")
	w.WaitUntil("attached to alpha", wait, func(sc string) bool { return lastLine(sc, alpha+" coordinator") && lastLine(sc, " · "+id+" ") })
	w.Prefix("]")
	w.WaitUntil("attached to beta", wait, func(sc string) bool { return lastLine(sc, beta+" coordinator") })
	w.Quit()
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
// on every screen, shared by the consoles of view main. Every project is
// always expanded, its rows on tree connectors; a project row shows its dashboard, a
// coordinator row attaches the coordinator, a thread row attaches the
// thread, from the dashboard and from a session; the sidebar stays left
// of the pane; prefix } widens it and a drag on its border moves
// it, both resizing the panes and kept in ui.json; a narrow window gets
// the slim strip. tm attach shows the sidebar too, and a click on it
// switches that console to the full UI on view main.
func TestSmokeSidebar(t *testing.T) {
	env, projDir, _ := threadEnv(t)
	demo := "demo"
	beta, betaDir := newProject(env, "beta")
	env.Trust(betaDir)
	th := startThread(t, env, projDir)

	w := env.Window(120, 30)
	// beta, the first project, is current, with its coordinator (none
	// runs yet); demo is expanded too, with its coordinator and, nested
	// under that, its one thread.
	w.WaitFor(" ■ "+beta, wait)
	w.WaitUntil("demo's thread", wait, func(sc string) bool {
		d := projectRow(sc, demo)
		return d >= 0 && strings.HasSuffix(sideCut(strings.Split(sc, "\n")[d], 120), " 1     │") &&
			treeRow(sc, demo, "coordinator") >= 0 && strings.Contains(sc, "    └─ t-0001 ")
	})
	w2 := env.Window(100, 26)
	w2.WaitUntil("demo's thread", wait, func(sc string) bool { return treeRow(sc, demo, "t-0001 ") >= 0 })
	both := []*Window{w, w2}

	// A project row shows that project's dashboard, in both consoles.
	w.Click(4, sideRow(t, w.Screen(), demo))
	for _, x := range both {
		x.WaitUntil("demo's dashboard", wait, func(sc string) bool {
			return strings.Contains(sc, " "+demo+" · active ─") && strings.Contains(sc, "t-0001 Small fix") && treeRow(sc, demo, "coordinator") >= 0
		})
	}
	// beta's row, then its coordinator's row, starts and attaches the
	// coordinator; the panes get the window less the sidebar and the
	// coordinator's info panel.
	clickCoordinator(t, w, beta)
	w.WaitUntil("attached to beta", agentWait, func(sc string) bool { return lastLine(sc, beta+" coordinator") })
	w.WaitFor("Fake Claude Code", agentWait)
	b := coordinatorOf(t, env, beta)
	waitPaneSize(t, env, b, threadCols(120), 28)
	border := SideCols(120) - 1
	for i, l := range strings.Split(w.Screen(), "\n")[:28] {
		if r := []rune(l); len(r) <= border || r[border] != '│' {
			t.Fatalf("row %d lacks the sidebar's border:\n%s", i, w.Screen())
		}
	}

	// From the session: demo's thread's row attaches the thread, still in
	// the attach view.
	w.WaitUntil("demo's thread", wait, func(sc string) bool { return treeRow(sc, demo, "t-0001 ") >= 0 && lastLine(sc, beta+" coordinator") })
	w.Click(5, treeRow(w.Screen(), demo, "t-0001 "))
	for _, x := range both {
		x.WaitUntil("on t-0001", wait, func(sc string) bool { return lastLine(sc, demo+" t-0001") })
	}
	// No console sized the thread yet: the first to show it fills it.
	waitPaneSize(t, env, th, threadCols(120), 28)
	// beta's row, from the pane, shows beta's dashboard on both consoles;
	// its coordinator's row attaches the coordinator again.
	w.Click(4, sideRow(t, w.Screen(), beta))
	for _, x := range both {
		x.WaitUntil("beta's dashboard", wait, func(sc string) bool { return strings.Contains(sc, " "+beta+" · active ─") })
	}
	clickCoordinator(t, w, beta)
	w.WaitUntil("attached to beta", wait, func(sc string) bool { return lastLine(sc, beta+" coordinator") })

	// Prefix } widens the sidebar: a layout change, so the pane follows,
	// and ui.json keeps the width.
	w.Prefix("}")
	waitPaneSize(t, env, b, besidePanel(120, sideDefault+2), 28)
	if !Poll(wait, func() bool {
		return strings.Contains(readFile(env.Home, "ui.json"), fmt.Sprintf(`"width": %d`, sideDefault+2))
	}) {
		t.Fatalf("ui.json: %s", readFile(env.Home, "ui.json"))
	}
	// Dragging its border 4 columns right makes it 4 wider.
	w.Drag(sideDefault+1, sideDefault+5, 5)
	waitPaneSize(t, env, b, besidePanel(120, sideDefault+6), 28)
	if !Poll(wait, func() bool {
		return strings.Contains(readFile(env.Home, "ui.json"), fmt.Sprintf(`"width": %d`, sideDefault+6))
	}) {
		t.Fatalf("ui.json after the drag: %s", readFile(env.Home, "ui.json"))
	}

	// A narrow window: the slim strip of projects, 10 columns, never
	// nothing; beta, the focused pane's, in the accent colour.
	w.Resize(70, 30)
	waitPaneSize(t, env, b, besidePanel(70, SideCols(70)), 28)
	w.WaitFor(" ○ beta  │", wait)
	w.WaitFor(" · demo  │", wait)
	w.Resize(120, 30)
	w.Detach()
	for _, x := range both {
		x.WaitFor("SESSIONS", wait)
	}

	// tm attach on the thread, with the status bar and the sidebar. A click on
	// demo's coordinator hands the console over to view main, which
	// starts and shows the coordinator, on every console of the view.
	w3 := env.Attach(120, 30, th.ID)
	w3.WaitUntil("tm attach on t-0001", wait, func(sc string) bool { return lastLine(sc, demo+" t-0001") })
	w3.WaitUntil("the sidebar", wait, func(sc string) bool { return treeRow(sc, demo, "coordinator") >= 0 })
	w3.Click(4, treeRow(w3.Screen(), demo, "coordinator"))
	for _, x := range []*Window{w3, w, w2} {
		x.WaitUntil("on demo's coordinator", agentWait, func(sc string) bool { return lastLine(sc, demo+" coordinator") })
	}
	coordinatorOf(t, env, demo)
	w3.Detach() // a full console now: back to the dashboard, everywhere
	for _, x := range []*Window{w3, w, w2} {
		x.WaitFor("SESSIONS", wait)
		x.Quit()
		x.WaitExit(wait)
	}
}

// sideCut is the sidebar's part of a screen line, its border included,
// in a window cols wide.
func sideCut(l string, cols int) string {
	r := []rune(l)
	return string(r[:min(len(r), SideCols(cols))])
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
// inactiveRow reports whether the project's row in the sidebar carries
// the inactive mark, which sits at the row's far right.
func inactiveRow(screen, slug string) bool {
	i := projectRow(screen, slug)
	return i >= 0 && strings.Contains(sideCut(strings.Split(screen, "\n")[i], 120), "⊘")
}

func projectRow(screen, slug string) int {
	for i, l := range strings.Split(screen, "\n") {
		r := []rune(l)
		if len(r) < 4 || r[1] != '■' {
			continue
		}
		if f := strings.Fields(string(r[3:min(len(r), sideDefault-1)])); len(f) > 0 && strings.TrimRight(f[0], "⌁∥⊘") == slug {
			return i
		}
	}
	return -1
}

// treeRow is the screen row of the row under slug's project in the
// sidebar's tree whose text starts with label ("coordinator", or a
// thread's id on a row nested under it); -1 when slug has none.
func treeRow(screen, slug, label string) int {
	lines := strings.Split(screen, "\n")
	for i := projectRow(screen, slug) + 1; i > 0 && i < len(lines); i++ {
		r := []rune(lines[i])
		if len(r) < 4 || r[0] != ' ' {
			return -1
		}
		row := []rune(strings.TrimLeft(string(r[:min(len(r), sideDefault-1)]), " "))
		if len(row) < 3 || row[0] != '├' && row[0] != '└' {
			return -1
		}
		if strings.HasPrefix(string(row[3:]), label) {
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
cmd = 'env -u TERMINATR_SESSION -u TERMINATR_ROLE -u TERMINATR_PROJECT "$TERMINATR_BIN" task status T1 done --project demo 2>&1; echo "exit=$?"'
`})
	slug, dir := newProject(env, "demo")
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

	// The human, from a shell session inside terminatr.
	sh := env.Start("shell")
	env.WaitFor(sh, "$", wait)
	env.Keys(sh, `"$TERMINATR_BIN" task status T1 done --project demo; echo "rc=$?"`+"\r")
	env.WaitFor(sh, "rc=0", wait)
	if out := env.MustCLI("task", "show", "T1", "--project", slug, "--json"); !strings.Contains(out, `"status": "done"`) {
		t.Fatalf("the human's done didn't stick:\n%s", out)
	}
}

// TestSmokeClearLosesNothing is the invariant of docs/SPEC.md §15.6: a
// coordinator works (tasks, a done the user approved, inbox handling,
// journal lines); after /clear its SessionStart re-injection is the role rules
// plus tm context, byte for byte as captured before, and no inbox item
// was lost or handled twice.
func TestSmokeClearLosesNothing(t *testing.T) {
	env := New(t)
	env.FakeClaude()
	tm := `"$TERMINATR_BIN" `
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
	slug, dir := newProject(env, "demo")
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
	// The rules, then all of tm context and a last line on when to run
	// it again (T66, T109).
	rules := env.MustCLI("skill", "coordinator")
	full := env.MustCLI("context", "--project", slug)
	want := rules + "\n\n" + full +
		"\nThat is `tm context` as of this conversation's start; run it (or tm inbox list, tm thread list) again when you need the current state.\n"

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
