package e2e

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"
)

// argv is a fake agent start record's command line.
func argv(r FakeRecord) []string {
	var out []string
	l, _ := r["argv"].([]any)
	for _, a := range l {
		s, _ := a.(string)
		out = append(out, s)
	}
	return out
}

// hasRemote reports whether argv asks for remote control named name.
func hasRemote(argv []string, name string) bool {
	i := slices.Index(argv, "--remote-control")
	return i >= 0 && i+1 < len(argv) && argv[i+1] == name
}

// turnsDone waits until the fake agent finished n turns and s is idle.
func turnsDone(env *Env, s *Session, n int) {
	env.T.Helper()
	if !Poll(agentWait, func() bool {
		stops := 0
		for _, ev := range env.HookEvents() {
			if ev == "Stop" {
				stops++
			}
		}
		i, _ := env.Info(s)
		return stops >= n && i.State == "idle"
	}) {
		env.T.Fatalf("%d turns not done: %q", n, env.HookEvents())
	}
}

// remoteIs waits until s shows remote control on or off, as the agent's
// session file says, still in process pid.
func remoteIs(t *testing.T, env *Env, s *Session, on bool, pid int) {
	t.Helper()
	if !Poll(wait, func() bool { i, _ := env.Info(s); return i.RemoteControl == on }) {
		i, _ := env.Info(s)
		t.Fatalf("remote control not %v: %+v", on, i)
	}
	if i, _ := env.Info(s); i.PID != pid {
		t.Fatalf("the agent restarted: pid %d, was %d", i.PID, pid)
	}
}

// remoteMarked reports whether the sidebar on screen sc shows "⌁" on
// the coordinator's row only, at its right, beside the state glyph, and
// not after the project's name.
func remoteMarked(sc, slug string) bool {
	marked := false
	for _, l := range strings.Split(sc, "\n") {
		side := []rune(l)
		side = side[:min(len(side), sideDefault)]
		row := strings.TrimRight(string(side[:max(len(side)-1, 0)]), " ")
		if strings.Contains(row, slug+"⌁") || strings.Contains(row, slug+" ⌁") {
			return false
		}
		if f := strings.Fields(row); len(f) >= 3 && f[len(f)-3] == "coordinator" && f[len(f)-2] == "⌁" {
			marked = true
		}
	}
	return marked
}

// remoteProject is a project whose setting starts its coordinator with
// remote control; it returns the running, idle coordinator after the
// kickoff turn.
func remoteProject(t *testing.T, env *Env) (slug string, coord *Session) {
	t.Helper()
	slug, dir := newProject(env, "Alpha")
	env.Trust(dir)
	os.MkdirAll(env.Home, 0o700)
	if err := os.WriteFile(filepath.Join(env.Home, "config.toml"), []byte("[projects."+slug+"]\ncoordinator_remote_control = true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	id := strings.TrimSpace(env.MustCLI("project", "open", slug))
	coord = &Session{ID: id}
	info, _ := env.Info(coord)
	coord.PID = info.PID
	env.track(info.PID, "coordinator "+id)
	if !info.RemoteControl {
		t.Fatalf("coordinator started without remote control: %+v", info)
	}
	start := env.WaitFake("start", agentWait, nil)
	if a := argv(start); !hasRemote(a, slug) || a[len(a)-2] != "--" {
		t.Fatalf("launch argv %q: want --remote-control %s, the kickoff last after --", a, slug)
	}
	turnsDone(env, coord, 1)
	return slug, coord
}

// TestSmokeCoordinatorRemoteControl: the project's setting starts the
// coordinator with remote control, named after the project (docs/SPEC.md
// §8.2). With the claude manifest both directions stay in the session:
// on pastes /remote-control <slug>; off pastes /remote-control and
// answers its menu. The status bar and the sidebar show it; prefix+r
// asks, then does the same.
func TestSmokeCoordinatorRemoteControl(t *testing.T) {
	env := New(t)
	env.FakeClaude()
	alpha, coord := remoteProject(t, env)
	info, _ := env.Info(coord)

	out := env.MustCLI("project", "remote", "off", alpha)
	if !strings.Contains(out, "remote control off") || strings.Contains(out, "config") {
		t.Fatalf("remote off said %q", out)
	}
	env.WaitFake("slash", agentWait, func(r FakeRecord) bool { return r.Str("text") == "/remote-control" })
	env.WaitFor(coord, "Remote Control disconnected.", agentWait)
	remoteIs(t, env, coord, false, info.PID)
	env.WaitState(coord, "idle", agentWait)

	if out := env.MustCLI("project", "remote", "on", alpha); !strings.Contains(out, "remote control on") {
		t.Fatalf("remote on said %q", out)
	}
	env.WaitFake("slash", agentWait, func(r FakeRecord) bool { return r.Str("text") == "/remote-control "+alpha })
	env.WaitFor(coord, "/remote-control is active", agentWait)
	remoteIs(t, env, coord, true, info.PID)
	if out := env.MustCLI("project", "remote", "on", alpha); !strings.Contains(out, "already on") {
		t.Fatalf("on again said %q", out)
	}
	env.WaitState(coord, "idle", agentWait)

	w := env.Window(120, 30)
	w.WaitUntil("sidebar marker", wait, func(sc string) bool { return remoteMarked(sc, alpha) }) // the dashboard's sidebar
	clickCoordinator(t, w, alpha)
	w.WaitUntil("status bar marker", agentWait, func(sc string) bool { return lastLine(sc, "remote control on") })
	w.WaitUntil("sidebar marker", wait, func(sc string) bool { return remoteMarked(sc, alpha) }) // the attached view's sidebar
	w.Prefix("r")
	w.WaitFor("turn remote control off for "+alpha+"?", wait)
	w.Type("y")
	w.WaitFor("Remote Control disconnected.", agentWait)
	w.WaitUntil("marker gone", wait, func(sc string) bool {
		return lastLine(sc, alpha+" coordinator") && !strings.Contains(sc, "remote control on") && !strings.Contains(sc, "⌁")
	})
	remoteIs(t, env, coord, false, info.PID)
	w.Detach()
	w.WaitFor("SESSIONS", wait)
	w.Quit()
	w.WaitExit(wait)

	// tm attach on the coordinator has the status bar too: its state
	// and remote control show there.
	a := env.Attach(120, 30, coord.ID)
	a.WaitUntil("tm attach status bar", agentWait, func(sc string) bool { return lastLine(sc, coord.ID+" · "+alpha+" coordinator") })
	a.Detach()
	a.WaitExit(wait)

	// A project without a running coordinator: nothing to change.
	beta, _ := newProject(env, "Beta")
	if r := env.CLI("project", "remote", "on", beta); r.Code != 1 || !strings.Contains(r.Stderr, "open the project first") {
		t.Fatalf("remote on without a coordinator: %+v", r)
	}
}

// TestSmokeRemoteControlRestart: an agent whose manifest has only the
// launch args: off and on resume the agent with or without them, under
// the same session id, and an attached pane follows the restart.
func TestSmokeRemoteControlRestart(t *testing.T) {
	env := New(t)
	env.FakeClaude(func(m string) string {
		return regexp.MustCompile(`(?m)^(enable|disable|disable_dialog) = .*\n`).ReplaceAllString(m, "")
	})
	alpha, coord := remoteProject(t, env)
	info, _ := env.Info(coord)

	out := env.MustCLI("project", "remote", "off", alpha)
	if !strings.Contains(out, "restarted") {
		t.Fatalf("remote off said %q", out)
	}
	resumed := env.WaitFake("start", agentWait, func(r FakeRecord) bool { return slices.Contains(argv(r), "--resume") })
	if a := argv(resumed); slices.Contains(a, "--remote-control") {
		t.Fatalf("resumed with the flag: %q", a)
	}
	if !Poll(agentWait, func() bool { i, ok := env.Info(coord); return ok && !i.RemoteControl && i.PID != info.PID }) {
		t.Fatalf("session %s not relaunched without remote control: %+v", coord.ID, env.Sessions())
	}
	info, _ = env.Info(coord)
	env.track(info.PID, "relaunched coordinator")
	env.WaitState(coord, "idle", agentWait)

	// On from the attached pane, which reattaches across the restart.
	w := env.Window(120, 30)
	w.WaitFor("SESSIONS", wait)
	clickCoordinator(t, w, alpha)
	w.WaitUntil("attached", agentWait, func(sc string) bool { return lastLine(sc, alpha+" coordinator") })
	w.Prefix("r")
	w.WaitFor("turn remote control on for "+alpha, wait)
	w.Type("y")
	w.WaitFor(coord.ID+" is back", agentWait)
	w.WaitUntil("markers", wait, func(sc string) bool {
		return lastLine(sc, "remote control on") && remoteMarked(sc, alpha) && strings.HasPrefix(sc, " PROJECTS")
	})
	if !Poll(agentWait, func() bool {
		n := 0
		for _, r := range env.FakeRecords("start") {
			if hasRemote(argv(r), alpha) && slices.Contains(argv(r), "--resume") {
				n++
			}
		}
		return n == 1
	}) {
		t.Fatalf("no resume with --remote-control %s", alpha)
	}
	for _, s := range env.Sessions() {
		env.track(s.PID, "session "+s.ID)
	}
	w.Type("hello")
	w.Key(Enter)
	env.WaitFake("prompt", agentWait, func(r FakeRecord) bool { return r.Str("text") == "hello" })
	w.Detach()
	w.WaitFor("SESSIONS", wait)
	w.Quit()
	w.WaitExit(wait)
}

// TestSmokeTickerKeepsRemoteOn: with the setting on, the ticker turns a
// running coordinator's remote control on when it reads off (here: it
// started before the setting was turned on), once it is idle, and
// journals it; the user's own off then holds (docs/SPEC.md §11.2).
func TestSmokeTickerKeepsRemoteOn(t *testing.T) {
	env := New(t)
	env.FakeClaude()
	env.Setenv("TERMILATOR_TICK_SWEEP", "300ms")
	env.Setenv("TERMILATOR_TICK_REMOTE", "1s")
	env.Setenv("TERMILATOR_TICK_REMOTE_GRACE", "1s")
	slug, dir := newProject(env, "Alpha")
	env.Trust(dir)
	id := strings.TrimSpace(env.MustCLI("project", "open", slug))
	coord := &Session{ID: id}
	info, _ := env.Info(coord)
	coord.PID = info.PID
	env.track(info.PID, "coordinator "+id)
	turnsDone(env, coord, 1)
	if info, _ := env.Info(coord); info.RemoteControl {
		t.Fatalf("started with remote control: %+v", info)
	}

	writeConfig(t, env, "[projects."+slug+"]\ncoordinator_remote_control = true\n")
	enable := func(r FakeRecord) bool { return r.Str("text") == "/remote-control "+slug }
	env.WaitFake("slash", agentWait, enable)
	remoteIs(t, env, coord, true, info.PID)
	if !Poll(wait, func() bool {
		j, _ := os.ReadFile(filepath.Join(dir, "JOURNAL.md"))
		return strings.Contains(string(j), " ticker remote.on "+slug+" "+id)
	}) {
		j, _ := os.ReadFile(filepath.Join(dir, "JOURNAL.md"))
		t.Fatalf("no journal line:\n%s", j)
	}
	env.WaitState(coord, "idle", agentWait)

	out := env.MustCLI("project", "remote", "off", slug)
	if !strings.Contains(out, "stays off until the coordinator is started anew") {
		t.Fatalf("remote off said %q", out)
	}
	env.WaitFor(coord, "Remote Control disconnected.", agentWait)
	remoteIs(t, env, coord, false, info.PID)
	env.WaitState(coord, "idle", agentWait)
	// Several of the ticker's tries later, it is still off.
	time.Sleep(4 * time.Second)
	if n := len(slices.DeleteFunc(env.FakeRecords("slash"), func(r FakeRecord) bool { return !enable(r) })); n != 1 {
		t.Fatalf("the ticker turned it on %d times; the user's off must hold", n)
	}
	if i, _ := env.Info(coord); i.RemoteControl || !i.RemoteHeld {
		t.Fatalf("after the user's off: %+v", i)
	}
}
