package e2e

// Active and inactive projects (docs/SPEC.md §3.6, §5.1): only an active
// project's coordinator and threads run and come back after a server
// restart; an inactive project's stay dormant until it is activated.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/theclifmeister/terminatr/internal/emu"
)

// dormantIDs are the session ids sessions.json keeps dormant.
func dormantIDs(env *Env) []string {
	var st struct{ Dormant []struct{ ID string } }
	b, _ := os.ReadFile(filepath.Join(env.Home, "state", "sessions.json"))
	json.Unmarshal(b, &st)
	var out []string
	for _, r := range st.Dormant {
		out = append(out, r.ID)
	}
	return out
}

// projectSessions are the ids of slug's running sessions.
func projectSessions(env *Env, slug string) []string {
	var out []string
	for _, s := range env.Sessions() {
		if s.Project == slug {
			out = append(out, s.ID)
		}
	}
	return out
}

// TestSmokeProjectActive: deactivating a project stops its coordinator
// and thread (asking first: without a terminal it needs --yes), keeps
// them dormant across a server restart, refuses to open it or start a
// thread meanwhile, and activating it resumes both with their
// conversations, under their ids.
func TestSmokeProjectActive(t *testing.T) {
	env, projDir, _ := threadEnv(t)
	coord := env.StartAgent("claude", projDir, "--role", "coordinator", "--project", "demo")
	env.WaitState(coord, "idle", agentWait)
	env.Prompt(coord, "hello")
	env.WaitFake("prompt", agentWait, func(r FakeRecord) bool { return r.Str("text") == "hello" })
	coordSID := env.WaitState(coord, "idle", agentWait).AgentSID
	th := startThread(t, env, projDir)
	threadSID := env.WaitState(th, "idle", agentWait).AgentSID
	if !Poll(agentWait, func() bool {
		var st struct{ Sessions []struct{ Prompted bool } }
		b, _ := os.ReadFile(filepath.Join(env.Home, "state", "sessions.json"))
		json.Unmarshal(b, &st)
		n := 0
		for _, r := range st.Sessions {
			if r.Prompted {
				n++
			}
		}
		return n == 2
	}) {
		t.Fatal("sessions.json never recorded the prompts")
	}

	// Without a terminal, deactivating running agents needs --yes.
	if r := env.CLI("project", "deactivate", "demo"); r.Code == 0 || !strings.Contains(r.Stderr, "confirm with --yes") {
		t.Fatalf("deactivate without --yes: exit %d\n%s%s", r.Code, r.Stdout, r.Stderr)
	}
	if got := len(projectSessions(env, "demo")); got != 2 {
		t.Fatalf("%d sessions run after a refused deactivate, want 2", got)
	}
	out := env.MustCLI("project", "deactivate", "demo", "--yes")
	if want := "deactivated demo: stopped its coordinator and t-0001, to resume when you activate it"; !strings.Contains(out, want) {
		t.Fatalf("deactivate said %q, want %q", out, want)
	}
	if got := projectSessions(env, "demo"); len(got) != 0 {
		t.Fatalf("still running after deactivate: %v", got)
	}
	if got := dormantIDs(env); len(got) != 2 {
		t.Fatalf("dormant %v, want the coordinator and the thread", got)
	}
	if out := env.MustCLI("project", "list"); !strings.Contains(out, "demo (inactive)") {
		t.Errorf("project list:\n%s", out)
	}

	// While inactive nothing opens it: not tm project open, not a thread.
	if r := env.CLI("project", "open", "demo"); r.Code == 0 || !strings.Contains(r.Stdout+r.Stderr, "demo is inactive") {
		t.Errorf("project open: exit %d\n%s%s", r.Code, r.Stdout, r.Stderr)
	}
	if r := env.CLI("thread", "start", "Another", "--project", "demo"); r.Code == 0 || !strings.Contains(r.Stdout+r.Stderr, "demo is inactive") {
		t.Errorf("thread start: exit %d\n%s%s", r.Code, r.Stdout, r.Stderr)
	}

	// A restart leaves them dormant: nothing of demo's is started.
	starts := len(env.FakeRecords("start"))
	env.MustCLI("server", "restart", "--yes")
	env.MustCLI("session", "list")
	if got := projectSessions(env, "demo"); len(got) != 0 {
		t.Fatalf("running after a restart while inactive: %v", got)
	}
	if got := dormantIDs(env); len(got) != 2 {
		t.Fatalf("dormant after a restart %v, want 2", got)
	}
	if n := len(env.FakeRecords("start")); n != starts {
		t.Fatalf("%d agents started while demo is inactive", n-starts)
	}

	// Activating resumes both, under their ids, with their conversations.
	out = env.MustCLI("project", "activate", "demo")
	if want := "activated demo: resumed its coordinator and t-0001"; !strings.Contains(out, want) {
		t.Fatalf("activate said %q, want %q", out, want)
	}
	if got := env.WaitState(coord, "idle", agentWait).AgentSID; got != coordSID {
		t.Errorf("coordinator came back with %q, want %q", got, coordSID)
	}
	if got := env.WaitState(th, "idle", agentWait).AgentSID; got != threadSID {
		t.Errorf("t-0001 came back with %q, want %q", got, threadSID)
	}
	for _, sid := range []string{coordSID, threadSID} {
		env.WaitFake("start", agentWait, func(r FakeRecord) bool { return r.Str("resume") == sid })
	}
	if got := dormantIDs(env); len(got) != 0 {
		t.Errorf("dormant after activate: %v", got)
	}
	if out := env.MustCLI("project", "activate", "demo"); !strings.Contains(out, "demo is already active") {
		t.Errorf("activate again: %q", out)
	}
	journal, _ := os.ReadFile(filepath.Join(projDir, "JOURNAL.md"))
	for _, want := range []string{"project.deactivate demo", "project.activate demo"} {
		if !strings.Contains(string(journal), want) {
			t.Errorf("JOURNAL.md lacks %q", want)
		}
	}
}

// TestSmokeSidebarInactive: an inactive project is one row in the
// sidebar, marked ⊘, with no coordinator or thread rows; space on it
// activates it and it expands; space on an active project with a running
// thread asks first, n keeps it, and y deactivates it, collapsing it.
func TestSmokeSidebarInactive(t *testing.T) {
	env, projDir, _ := threadEnv(t)
	startThread(t, env, projDir)
	beta, betaDir := newProject(env, "beta")
	env.Trust(betaDir)
	env.MustCLI("project", "deactivate", beta)

	// beta, the first project, is current: the sidebar's cursor starts
	// on its row.
	w := env.Window(120, 30)
	w.WaitUntil("beta collapsed", wait, func(sc string) bool {
		return inactiveRow(sc, beta) && treeRow(sc, beta, "coordinator") < 0 &&
			treeRow(sc, "demo", "t-0001 ") >= 0
	})
	w.Type("\t")
	w.WaitFor("sidebar: ↑ ↓ move", wait)
	w.Type(" ")
	w.WaitUntil("beta expanded", agentWait, func(sc string) bool {
		return treeRow(sc, beta, "coordinator") >= 0 && !inactiveRow(sc, beta)
	})
	if got := projectSessions(env, beta); len(got) != 0 {
		t.Fatalf("activating beta started %v; it had nothing to resume", got)
	}

	// Down past beta's coordinator to demo, which runs a thread.
	w.Type("j")
	w.Type("j")
	w.Type(" ")
	w.WaitFor("Deactivate demo? This stops t-0001", wait)
	w.Type("n")
	w.WaitFor("demo stays active", wait)
	if got := projectSessions(env, "demo"); len(got) != 1 {
		t.Fatalf("demo runs %v after n, want its thread", got)
	}
	w.Type(" ")
	w.WaitFor("Deactivate demo?", wait)
	w.Type("y")
	w.WaitUntil("demo collapsed", agentWait, func(sc string) bool {
		return inactiveRow(sc, "demo") && treeRow(sc, "demo", "t-0001 ") < 0
	})
	if !Poll(agentWait, func() bool { return len(projectSessions(env, "demo")) == 0 }) {
		t.Fatalf("demo still runs %v", projectSessions(env, "demo"))
	}
	if got := dormantIDs(env); len(got) != 1 {
		t.Errorf("dormant %v, want demo's thread", got)
	}
}

// TestSmokeSidebarExpandInactive: an inactive project expands like a
// folder (→) without activating it, its coordinator and thread shown
// dormant; enter on the dormant coordinator activates the project, which
// resumes the coordinator and the thread, and attaches the coordinator
// (docs/SPEC.md §4, §5.1). The collapsed and the expanded trees are
// golden screens.
func TestSmokeSidebarExpandInactive(t *testing.T) {
	env, projDir, _ := threadEnv(t)
	beta, betaDir := newProject(env, "beta")
	env.Trust(betaDir)
	coord := env.StartAgent("claude", projDir, "--role", "coordinator", "--project", "demo")
	env.WaitState(coord, "idle", agentWait)
	env.Prompt(coord, "hello")
	env.WaitFake("prompt", agentWait, func(r FakeRecord) bool { return r.Str("text") == "hello" })
	coordSID := env.WaitState(coord, "idle", agentWait).AgentSID
	th := startThread(t, env, projDir)
	threadSID := env.WaitState(th, "idle", agentWait).AgentSID
	if !Poll(agentWait, func() bool {
		var st struct{ Sessions []struct{ Prompted bool } }
		b, _ := os.ReadFile(filepath.Join(env.Home, "state", "sessions.json"))
		json.Unmarshal(b, &st)
		n := 0
		for _, r := range st.Sessions {
			if r.Prompted {
				n++
			}
		}
		return n == 2
	}) {
		t.Fatal("sessions.json never recorded the prompts")
	}
	env.MustCLI("project", "deactivate", "demo", "--yes")

	w := env.Window(100, 12)
	side := SideCols(100)
	sidebar := func() string {
		lines := strings.Split(w.Screen(), "\n")
		for i, l := range lines {
			r := []rune(l)
			lines[i] = string(r[:min(side, len(r))])
		}
		return strings.Join(lines, "\n")
	}
	w.WaitUntil("demo collapsed", wait, func(sc string) bool {
		return inactiveRow(sc, "demo") && treeRow(sc, beta, "coordinator") >= 0
	})
	WaitGolden(t, DefaultTimeout, sidebar, "sidebar-inactive.txt")

	// beta is current: ↓ ↓ past its coordinator to demo, → expands it.
	w.Key(keyTab)
	w.WaitFor("sidebar: ↑ ↓ move", wait)
	for _, k := range []emu.Key{keyDown, keyDown, keyRight} {
		w.Key(k)
	}
	w.WaitUntil("demo expanded", wait, func(sc string) bool {
		return treeRow(sc, "demo", "coordinator") >= 0 && treeRow(sc, "demo", "t-0001 ") >= 0
	})
	WaitGolden(t, DefaultTimeout, sidebar, "sidebar-inactive-expanded.txt")
	if got := projectSessions(env, "demo"); len(got) != 0 {
		t.Fatalf("expanding demo started %v", got)
	}
	if out := env.MustCLI("project", "list"); !strings.Contains(out, "demo (inactive)") {
		t.Fatalf("expanding activated demo:\n%s", out)
	}

	// enter on the dormant coordinator (the cursor is on it) activates
	// demo: both resume, and the coordinator attaches.
	w.Key(keyEnter)
	w.WaitUntil("on demo's coordinator", agentWait, func(sc string) bool { return lastLine(sc, "demo coordinator") })
	if got := env.WaitState(coord, "idle", agentWait).AgentSID; got != coordSID {
		t.Errorf("coordinator came back with %q, want %q", got, coordSID)
	}
	if got := env.WaitState(th, "idle", agentWait).AgentSID; got != threadSID {
		t.Errorf("t-0001 came back with %q, want %q", got, threadSID)
	}
	if out := env.MustCLI("project", "list"); strings.Contains(out, "(inactive)") {
		t.Errorf("demo still inactive:\n%s", out)
	}
	w.WaitUntil("demo active", wait, func(sc string) bool { return !inactiveRow(sc, "demo") })
}
