package session

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"
)

// driftSession is a bare session around claude's manifest that records
// its log.
func driftSession(t *testing.T, home string, pid int) (*Session, *agentRT, func() []string) {
	t.Helper()
	rt, err := newAgentRT(AgentConfig{Agent: claudeLike(t), Home: home}, pid, false)
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var log []string
	logf := func(f string, args ...any) {
		mu.Lock()
		defer mu.Unlock()
		log = append(log, fmt.Sprintf(f, args...))
	}
	s := &Session{cfg: Config{ID: "s-test", Logf: logf}, ag: rt}
	return s, rt, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), log...)
	}
}

// TestDrift: what an agent release could change is noted once per kind,
// with the agent's version and the last tested one, in the log and in
// tm agent explain; what the manifest expects isn't.
func TestDrift(t *testing.T) {
	s, rt, log := driftSession(t, t.TempDir(), os.Getpid())
	s.SetAgentVersion("9.0.0")

	s.checkHook(rt, "SessionStart", map[string]any{"session_id": "a"})
	s.checkHook(rt, "Stop", map[string]any{"session_id": "a"})
	if got := log(); len(got) != 0 {
		t.Fatalf("expected events noted: %q", got)
	}
	s.checkHook(rt, "TurnEnded", map[string]any{"session_id": "a"})
	s.checkHook(rt, "TurnEnded", map[string]any{"session_id": "a"})
	s.checkHook(rt, "Stop", map[string]any{"sessionId": "a"})
	got := log()
	if len(got) != 2 || !strings.Contains(got[0], `agent drift: claude 9.0.0, last tested `) ||
		!strings.Contains(got[0], `hook event "TurnEnded" is none tm registered`) || !strings.Contains(got[1], `"Stop" has no session_id`) {
		t.Fatalf("log %q", got)
	}

	// The prompt channel: the agent's command refused, not a refusal
	// of the adapter's own.
	s.checkChannel(rt, errors.New("codex: no thread id yet"))
	if len(log()) != 2 {
		t.Fatalf("an adapter's refusal noted: %q", log())
	}
	err := exec.Command("/bin/sh", "-c", "exit 2").Run()
	s.checkChannel(rt, fmt.Errorf("codex queue: %w: error: unrecognized subcommand 'queue'", err))
	if got := log(); len(got) != 3 || !strings.Contains(got[2], "unrecognized subcommand") {
		t.Fatalf("log %q", got)
	}

	// Exited soon after its launch: the screen's last lines say why.
	s.checkExit(rt, "exit status 2", []string{"", "error: unexpected argument '--approve-for-me' found", "", "Usage: codex [OPTIONS]", ""})
	if got := log(); len(got) != 4 || !strings.Contains(got[3], `--approve-for-me' found / Usage: codex [OPTIONS]`) {
		t.Fatalf("log %q", got)
	}

	e, _ := s.Explain()
	if e.Extra["version"] != "9.0.0" || e.Extra["last_tested"] == "" || strings.Count(e.Extra["drift"], " | ") != 3 {
		t.Fatalf("explain %v", e.Extra)
	}
}

// TestDriftHookSilence: working on screen for hookSilence with no hook
// event at all says the hooks don't run; one event is enough.
func TestDriftHookSilence(t *testing.T) {
	s, rt, log := driftSession(t, t.TempDir(), os.Getpid())
	now := time.Now()
	s.checkHookSilence(rt, now.Add(time.Hour))
	rt.workingSince = now
	s.checkHookSilence(rt, now.Add(hookSilence-time.Second))
	if len(log()) != 0 {
		t.Fatalf("too early: %q", log())
	}
	s.checkHookSilence(rt, now.Add(hookSilence))
	if got := log(); len(got) != 1 || !strings.Contains(got[0], "no hook event came") || !strings.Contains(got[0], "(version unknown)") {
		t.Fatalf("log %q", got)
	}

	s2, rt2, log2 := driftSession(t, t.TempDir(), os.Getpid())
	rt2.workingSince = now
	rt2.seq.Add(1)
	s2.checkHookSilence(rt2, now.Add(time.Hour))
	if len(log2()) != 0 {
		t.Fatalf("noted with a hook event: %q", log2())
	}
}

// TestDriftStatusFile: a status value the manifest doesn't know is
// drift; the version the file names is the session's.
func TestDriftStatusFile(t *testing.T) {
	home := t.TempDir()
	pid := os.Getpid()
	writeStatus(t, home, pid, `{"status":"pondering","version":"3.0.0"}`)
	_, rt, _ := driftSession(t, home, pid)
	err := rt.pollStatus(time.Now())
	if err == nil || !strings.Contains(err.Error(), `unknown status "pondering"`) || rt.version != "3.0.0" {
		t.Fatalf("drift %v, version %q", err, rt.version)
	}
	writeStatus(t, home, pid, `{"status":"idle","version":"3.0.0"}`)
	if err := rt.pollStatus(time.Now().Add(time.Hour)); err != nil || rt.fields == nil {
		t.Fatalf("a newer version's known status: %v, fields %v", err, rt.fields)
	}
}
