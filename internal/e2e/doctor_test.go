package e2e

// M8 scenarios: tm doctor (docs/SPEC.md §15 M8).

import (
	"encoding/json"
	"os"
	"path/filepath"
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
	if r.Code != 0 || !strings.Contains(r.Stdout, "9.9.9 (newer than the last tested") {
		t.Fatalf("newer version: exit %d\n%s", r.Code, r.Stdout)
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

// TestDoctorServerContext: the code-host checks run in the server's
// context, as its sessions see them. Here the server's gh is logged in
// and the shell's is not (as over SSH on a Mac, without the keychain):
// doctor shows the server's result, and a note instead of a warning. With
// no server it checks in this shell and says so.
func TestDoctorServerContext(t *testing.T) {
	env, _, _ := tickerEnv(t)
	// tickerEnv's fake gh: `gh auth status` succeeds while $GH_STATE exists.
	// The server's gh is logged in; this shell's is not.
	good := filepath.Join(t.TempDir(), "auth.json")
	os.WriteFile(good, []byte("{}"), 0o644)
	env.Setenv("GH_STATE", good)
	env.RestartServer()
	env.Setenv("GH_STATE", filepath.Join(t.TempDir(), "none.json"))

	type result struct {
		Status string
		Checks []struct{ Group, Name, Status, Detail, Source string }
	}
	var res result
	r := env.CLI("doctor", "--json")
	if err := json.Unmarshal([]byte(r.Stdout), &res); err != nil {
		t.Fatalf("doctor --json: %v\n%s%s", err, r.Stdout, r.Stderr)
	}
	found := map[string]string{}
	for _, c := range res.Checks {
		found[c.Name] = c.Status + " " + c.Source + " " + c.Detail
	}
	if got := found["gh auth"]; got != "ok server logged in" {
		t.Errorf("gh auth: %q", got)
	}
	if got := found["local shell"]; !strings.HasPrefix(got, "ok local ") || !strings.Contains(got, "gh auth") || !strings.Contains(got, "the server's sessions pass") {
		t.Errorf("local shell note: %q", got)
	}
	// Only the gh line: a runner with az installed has an "az login  not
	// logged in" line of its own.
	out := env.MustCLI("doctor")
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "gh auth") && !strings.Contains(line, "local shell") && (!strings.Contains(line, "logged in [server]") || strings.Contains(line, "not logged in")) {
			t.Errorf("doctor text, gh auth line %q:\n%s", line, out)
		}
	}
	if !strings.Contains(out, "gh auth        logged in [server]") {
		t.Errorf("doctor text lacks the server's gh auth:\n%s", out)
	}

	// No server: this shell's checks, labelled local.
	env.MustCLI("server", "stop", "--yes")
	res = result{}
	r = env.CLI("doctor", "--json")
	if err := json.Unmarshal([]byte(r.Stdout), &res); err != nil {
		t.Fatalf("doctor --json: %v\n%s%s", err, r.Stdout, r.Stderr)
	}
	found = map[string]string{}
	for _, c := range res.Checks {
		found[c.Name] = c.Status + " " + c.Source
	}
	if got := found["gh auth"]; got != "warn local" {
		t.Errorf("no server, gh auth: %q", got)
	}
	if _, ok := found["local shell"]; ok {
		t.Error("a local shell note without a server")
	}
}
