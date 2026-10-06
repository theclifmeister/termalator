package e2e

// Replacing a server of another version (docs/SPEC.md §3.3, Stopping
// across protocols): restart is how a server is upgraded, so stop and
// restart must work whatever the running server speaks. The server's
// TERMINATR_TEST_HELLO hook plays the old server: "protocol=1" claims an
// older protocol, "deaf" answers no hello at all.

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/theclifmeister/terminatr/internal/proto"
)

func TestSmokeReplaceOldServer(t *testing.T) {
	for _, c := range []struct {
		name, hello string
		// replace swaps the old server for this tm's.
		replace func(env *Env)
	}{
		{"older protocol, restart", "protocol=1", func(env *Env) {
			// Agents run: without --yes the server's own refusal comes
			// back, not a version error.
			r := env.CLI("server", "restart")
			if r.Code != 1 || !strings.Contains(r.Stderr, "pass --yes") {
				t.Fatalf("restart without --yes: %+v", r)
			}
			r = env.CLI("server", "restart", "--yes")
			if r.Code != 0 || !strings.Contains(r.Stdout, "stopped (pid") || !strings.Contains(r.Stdout, "started (pid") {
				t.Fatalf("restart: %+v", r)
			}
		}},
		{"older protocol, doctor --fix", "protocol=1", func(env *Env) {
			r := env.CLI("doctor")
			if !strings.Contains(r.Stdout, "run 'tm server restart'") || !strings.Contains(r.Stdout, "can be fixed") {
				t.Fatalf("doctor: %+v", r)
			}
			r = env.CLI("doctor", "--fix", "--yes")
			if !strings.Contains(r.Stdout, "done  restart the server") {
				t.Fatalf("doctor --fix: %+v", r)
			}
		}},
		{"deaf, restart", "deaf", func(env *Env) {
			r := env.CLI("server", "restart", "--yes")
			if r.Code != 0 || !strings.Contains(r.Stderr, "sent it SIGTERM") || !strings.Contains(r.Stdout, "started (pid") {
				t.Fatalf("restart: %+v", r)
			}
		}},
		{"deaf, stop", "deaf", func(env *Env) {
			r := env.CLI("server", "stop")
			if r.Code != 1 || !strings.Contains(r.Stderr, "pass --yes") {
				t.Fatalf("stop without --yes: %+v", r)
			}
			r = env.CLI("server", "stop", "--yes")
			if r.Code != 0 || !strings.Contains(r.Stdout, "stopped (pid") {
				t.Fatalf("stop: %+v", r)
			}
			env.MustCLI("server", "start")
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			env := New(t)
			env.FakeClaude()
			dir := env.Workdir()
			env.Trust(dir)
			a := env.StartAgent("claude", dir)
			env.WaitState(a, "idle", agentWait)
			env.Prompt(a, "hello")
			env.WaitFake("prompt", agentWait, func(r FakeRecord) bool { return r.Str("text") == "hello" })
			sid := env.WaitState(a, "idle", agentWait).AgentSID
			if !Poll(agentWait, func() bool { return prompted(env, a.ID) }) {
				t.Fatal("sessions.json never recorded the prompt")
			}

			// Swap in the "old" server; it resumes the agent like any.
			env.MustCLI("server", "stop", "--yes")
			cmd := exec.Command(env.Bin, "server", "run", "--detached")
			cmd.Env = append(append([]string{}, env.Vars...), "TERMINATR_TEST_HELLO="+c.hello)
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("old server: %v\n%s", err, out)
			}
			if !Poll(wait, func() bool { return Alive(env.ServerPID()) }) {
				t.Fatal("old server never wrote its pid")
			}
			old := env.ServerPID()
			env.track(old, "old tm server")
			if !Poll(agentWait, func() bool { return resumes(env, sid) == 1 }) {
				t.Fatal("the old server did not resume the agent")
			}
			if r := env.CLI("session", "list"); r.Code == 0 {
				t.Fatalf("session list against the old server: %+v", r)
			} else if c.hello != "deaf" && !strings.Contains(r.Stderr, "run 'tm server restart'") {
				t.Fatalf("mismatch message doesn't say what works: %q", r.Stderr)
			}

			c.replace(env)

			if Alive(old) {
				t.Fatalf("old server (pid %d) still runs", old)
			}
			if got := env.WaitState(a, "idle", agentWait).AgentSID; got != sid {
				t.Errorf("agent came back with %q, want %q", got, sid)
			}
			if !Poll(agentWait, func() bool { return resumes(env, sid) == 2 }) {
				t.Fatalf("the new server did not resume the agent (%d resumes)", resumes(env, sid))
			}
			var st proto.ServerStatus
			json.Unmarshal([]byte(env.MustCLI("server", "status", "--json")), &st)
			if st.PreviousShutdown != "clean" {
				t.Errorf("old server's shutdown was %q, want clean", st.PreviousShutdown)
			}
		})
	}
}

// prompted reports whether sessions.json records session id as prompted,
// which is what makes it resumable.
func prompted(env *Env, id string) bool {
	var st struct {
		Sessions []struct {
			ID       string
			Prompted bool
		}
	}
	b, _ := os.ReadFile(filepath.Join(env.Home, "state", "sessions.json"))
	json.Unmarshal(b, &st)
	for _, r := range st.Sessions {
		if r.ID == id && r.Prompted {
			return true
		}
	}
	return false
}

// resumes counts the fake agent's starts that resumed sid.
func resumes(env *Env, sid string) int {
	n := 0
	for _, r := range env.FakeRecords("start") {
		if r.Str("resume") == sid {
			n++
		}
	}
	return n
}
