package e2e

// tm watch (docs/SPEC.md §10): a mod's push feed of a session's state.

import (
	"bufio"
	"encoding/json"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/theclifmeister/terminatr/internal/proto"
)

// TestWatchFeed: tm watch --json prints the coordinator's state, then a
// line within a second of a project command run through the server
// (cli.run). The re-read is off (an hour) and the command runs outside
// the agent, which stays idle, so only the wake-up on the command can
// send the line.
func TestWatchFeed(t *testing.T) {
	env, projDir, _ := threadEnv(t)
	env.Setenv("TERMINATR_WATCH_POLL", "1h")
	coord := env.StartAgent("claude", projDir, "--role", "coordinator", "--project", "demo")
	env.WaitState(coord, "idle", agentWait)
	// The state settles: its source moves from the hooks to the status
	// file a moment after idle, a change that would wake the watch too.
	Poll(5*time.Second, func() bool {
		info, _ := env.Info(coord)
		return info.StateSources == "status_file"
	})

	cmd := exec.Command(env.Bin, "watch", "--session", coord.ID, "--json")
	cmd.Env = env.Vars
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	env.track(cmd.Process.Pid, "tm watch")
	t.Cleanup(func() {
		cmd.Process.Kill()
		cmd.Wait()
	})
	lines := make(chan proto.Watch, 64)
	go func() {
		defer close(lines)
		sc := bufio.NewScanner(stdout)
		for sc.Scan() {
			var w proto.Watch
			if err := json.Unmarshal(sc.Bytes(), &w); err != nil {
				t.Errorf("not a Watch line: %q", sc.Text())
				return
			}
			lines <- w
		}
	}()
	wait := func(what string, within time.Duration, ok func(proto.Watch) bool) proto.Watch {
		t.Helper()
		deadline := time.After(within)
		for {
			select {
			case w, open := <-lines:
				if !open {
					t.Fatalf("tm watch ended waiting for %s", what)
				}
				if ok(w) {
					return w
				}
			case <-deadline:
				t.Fatalf("no line with %s within %v", what, within)
			}
		}
	}
	first := wait("the first state", agentWait, func(proto.Watch) bool { return true })
	if first.Session.ID != coord.ID || first.Session.Role != proto.RoleCoordinator || first.Session.Project != "demo" ||
		first.NeedsYou != 0 || first.Task != nil {
		t.Fatalf("first line %+v", first)
	}

	// TERMINATR_SESSION makes tm forward the command to the server.
	add := exec.Command(env.Bin, "task", "add", "Check it", "--status", "review", "--project", "demo")
	add.Env = append(env.Vars, "TERMINATR_SESSION="+coord.ID)
	if b, err := add.CombinedOutput(); err != nil {
		t.Fatalf("task add: %v\n%s", err, b)
	}
	wait("needs_you 1", time.Second, func(w proto.Watch) bool { return w.NeedsYou == 1 })

	// The plain form is one line per state, for people.
	plain := exec.Command(env.Bin, "watch", "--session", coord.ID)
	plain.Env = env.Vars
	po, _ := plain.StdoutPipe()
	if err := plain.Start(); err != nil {
		t.Fatal(err)
	}
	env.track(plain.Process.Pid, "tm watch (plain)")
	line, _ := bufio.NewReader(po).ReadString('\n')
	plain.Process.Kill()
	plain.Wait()
	if !strings.HasPrefix(line, coord.ID+" coordinator ") || !strings.Contains(line, "1 needs you") {
		t.Errorf("plain line %q", line)
	}

	// Without a session, from outside one, it says so.
	if r := env.CLI("watch", "--json"); r.Code != 2 || !strings.Contains(r.Stderr, "no session") {
		t.Errorf("watch without a session: %+v", r)
	}
}
