package e2e

// M8 scenarios: tm doctor (docs/SPEC.md §15 M8).

import (
	"encoding/json"
	"os"
	"strconv"
	"strings"
	"testing"
)

// TestDoctor: with a live server and the fake claude, doctor passes and
// names the server and the agent's tested version; an untested version
// is a warning, not a failure.
func TestDoctor(t *testing.T) {
	env := New(t)
	env.FakeClaude()
	env.Start("shell")
	pid := env.ServerPID()

	r := env.CLI("doctor")
	if r.Code != 0 {
		t.Fatalf("doctor: exit %d\n%s%s", r.Code, r.Stdout, r.Stderr)
	}
	for _, want := range []string{"libghostty-vt  linked", "pid " + strconv.Itoa(pid), "2.1.289 (tested)", "0 failures"} {
		if !strings.Contains(r.Stdout, want) {
			t.Errorf("doctor lacks %q:\n%s", want, r.Stdout)
		}
	}
	var res struct {
		Status string
		Checks []struct{ Group, Name, Status, Detail string }
	}
	if err := json.Unmarshal([]byte(env.MustCLI("doctor", "--json")), &res); err != nil || res.Status == "fail" || len(res.Checks) == 0 {
		t.Fatalf("doctor --json: %v %+v", err, res)
	}

	env.Setenv("FAKEAGENT_VERSION", "9.9.9")
	r = env.CLI("doctor")
	if r.Code != 0 || !strings.Contains(r.Stdout, "9.9.9 is not in tested_versions") {
		t.Fatalf("untested version: exit %d\n%s", r.Code, r.Stdout)
	}
}

// TestDoctorStaleSocket: after a crash the socket and pid file are left
// over. Doctor reports them and never starts a server; --fix without a
// terminal refuses unless --yes, and --fix --yes removes them.
func TestDoctorStaleSocket(t *testing.T) {
	env := New(t)
	env.Start("shell")
	env.KillServer()
	if _, err := os.Stat(env.Socket); err != nil {
		t.Fatalf("no socket left after the crash: %v", err)
	}
	r := env.CLI("doctor")
	if r.Code != 0 || !strings.Contains(r.Stdout, "stale socket") || !strings.Contains(r.Stdout, "tm doctor --fix") {
		t.Fatalf("doctor after a crash: exit %d\n%s", r.Code, r.Stdout)
	}
	if Alive(env.ServerPID()) {
		t.Fatal("doctor started a server")
	}
	r = env.CLI("doctor", "--fix")
	if r.Code != 1 || !strings.Contains(r.Stderr, "pass --yes") {
		t.Fatalf("--fix without a terminal: exit %d\n%s%s", r.Code, r.Stdout, r.Stderr)
	}
	if _, err := os.Stat(env.Socket); err != nil {
		t.Fatal("--fix without confirmation removed the socket")
	}
	r = env.CLI("doctor", "--fix", "--yes")
	if r.Code != 0 || !strings.Contains(r.Stdout, "done  remove stale socket") {
		t.Fatalf("--fix --yes: exit %d\n%s%s", r.Code, r.Stdout, r.Stderr)
	}
	if _, err := os.Stat(env.Socket); !os.IsNotExist(err) {
		t.Fatal("stale socket still there")
	}
	if out := env.MustCLI("doctor"); strings.Contains(out, "stale") {
		t.Fatalf("still stale:\n%s", out)
	}
}
