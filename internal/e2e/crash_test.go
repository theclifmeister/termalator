package e2e

// M8 scenarios: crash and restart resume end to end (docs/SPEC.md §3.6,
// §15 M8). The fake agent plays the coordinator and the threads.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/theclifmeister/termilator/internal/proto"
)

// TestSmokeCrashResume is M8's "Try it": kill -9 the server with a
// coordinator and threads running; the next tm brings them back. Prompted
// sessions resume their latest agent session id, an unprompted thread
// starts fresh, a shell is lost, and the project gets a server-restart
// item (M7's ticker) while the server log names every session.
func TestSmokeCrashResume(t *testing.T) {
	env, projDir, _ := threadEnv(t)
	coord := env.StartAgent("claude", projDir, "--role", "coordinator", "--project", "demo")
	env.WaitState(coord, "idle", agentWait)
	env.Prompt(coord, "hello")
	env.WaitFake("prompt", agentWait, func(r FakeRecord) bool { return r.Str("text") == "hello" })
	coordSID := env.WaitState(coord, "idle", agentWait).AgentSID

	thread := func(id, title string) (*Session, string) {
		t.Helper()
		env.MustCLI("thread", "start", title, "--project", "demo")
		var rec struct{ Session string }
		readTOML(t, filepath.Join(projDir, "threads", id, "thread.toml"), &rec)
		s := &Session{ID: rec.Session}
		return s, env.WaitState(s, "idle", agentWait).AgentSID
	}
	worked, workedSID := thread("t-0001", "Small fix")
	env.MustCLI("thread", "prompt", "t-0001", "work on it", "--project", "demo")
	env.WaitFake("prompt", agentWait, func(r FakeRecord) bool { return r.Str("text") == "work on it" })
	env.WaitState(worked, "idle", agentWait)
	// A thread always gets its kickoff, so it is prompted from the start;
	// a plain agent session with no prompt is the unprompted case.
	dir := env.Workdir()
	env.Trust(dir)
	fresh := env.StartAgent("claude", dir)
	freshSID := env.WaitState(fresh, "idle", agentWait).AgentSID
	shell := env.Start("shell")

	// Wait until the server has recorded both prompts for resume.
	if !Poll(agentWait, func() bool {
		var st struct {
			Sessions []struct {
				ID       string
				Prompted bool
			}
		}
		b, _ := os.ReadFile(filepath.Join(env.Home, "state", "sessions.json"))
		json.Unmarshal(b, &st)
		n := 0
		for _, r := range st.Sessions {
			if r.Prompted && (r.ID == coord.ID || r.ID == worked.ID) {
				n++
			}
		}
		return n == 2
	}) {
		t.Fatal("sessions.json never recorded the prompts")
	}
	pids := map[string]int{}
	for _, s := range env.Sessions() {
		pids[s.ID] = s.PID
	}

	env.KillServer()
	for id, pid := range pids {
		if !Poll(wait, func() bool { return !Alive(pid) }) {
			t.Fatalf("session %s (pid %d) outlived its server", id, pid)
		}
	}

	env.MustCLI("session", "list") // auto-starts and resumes
	if got := env.WaitState(coord, "idle", agentWait).AgentSID; got != coordSID {
		t.Errorf("coordinator came back with %q, want %q", got, coordSID)
	}
	if got := env.WaitState(worked, "idle", agentWait).AgentSID; got != workedSID {
		t.Errorf("t-0001 came back with %q, want %q", got, workedSID)
	}
	if got := env.WaitState(fresh, "idle", agentWait).AgentSID; got == "" || got == freshSID {
		t.Errorf("unprompted %s came back with %q (was %q); want a fresh id", fresh.ID, got, freshSID)
	}
	for _, sid := range []string{coordSID, workedSID} {
		env.WaitFake("start", agentWait, func(r FakeRecord) bool { return r.Str("resume") == sid })
	}
	for _, r := range env.FakeRecords("start") {
		if r.Str("resume") == freshSID {
			t.Errorf("the unprompted session was resumed: %+v", r)
		}
	}
	for _, r := range env.FakeRecords("picker") {
		t.Fatalf("resume with an empty id: %+v", r)
	}

	var st proto.ServerStatus
	json.Unmarshal([]byte(env.MustCLI("server", "status", "--json")), &st)
	if st.PreviousShutdown != "crash" || !slices.Equal(st.Lost, []string{shell.ID}) || len(st.Resumed) != 3 {
		t.Fatalf("status after crash: %+v", st)
	}
	want := "server-restart: the server restarted after a crash: 2 session(s) resumed, 0 not restored"
	if items := env.MustCLI("inbox", "list", "--project", "demo"); !strings.Contains(items, want) {
		t.Fatalf("inbox lacks %q:\n%s", want, items)
	}
	logWant := "server restarted after crash; resumed coordinator, t-0001; started fresh claude " + fresh.ID + "; lost shell " + shell.ID
	if b, _ := os.ReadFile(filepath.Join(env.Home, "logs", "server.log")); !strings.Contains(string(b), logWant) {
		t.Errorf("server.log lacks %q", logWant)
	}

	// A clean restart after that resumes again and says so without "crash".
	env.MustCLI("server", "restart", "--yes")
	env.WaitState(coord, "idle", agentWait)
	if items := env.MustCLI("inbox", "list", "--project", "demo"); !strings.Contains(items, "server-restart: the server restarted after a clean stop: 2 session(s) resumed") {
		t.Fatalf("inbox after a clean restart:\n%s", items)
	}
}
