package claude

import (
	"errors"
	"strings"
	"testing"

	"github.com/theclifmeister/terminatr/internal/agent"
)

func doctorOf(t *testing.T, list string, runErr error) []agent.DoctorCheck {
	t.Helper()
	d, ok := load(t).(agent.Doctor)
	if !ok {
		t.Fatal("claude is not an agent.Doctor")
	}
	look := func(string) (string, error) { return "/bin/claude", nil }
	run := func(_, _ string, _ ...string) (string, error) { return list, runErr }
	return d.DoctorChecks(look, run)
}

func TestDoctorUnsafePlugin(t *testing.T) {
	cs := doctorOf(t, `[{"id":"worktrees@supermods","enabled":true},{"id":"other@x","enabled":true}]`, nil)
	if len(cs) != 1 || !cs[0].Warn || cs[0].Name != "worktrees@supermods" || !strings.Contains(cs[0].Detail, "claude plugin disable") {
		t.Fatalf("checks %+v", cs)
	}
	cs = doctorOf(t, `[{"id":"worktrees@supermods","enabled":false}]`, nil)
	if len(cs) != 1 || cs[0].Warn {
		t.Fatalf("disabled plugin: %+v", cs)
	}
}

func TestDoctorListFails(t *testing.T) {
	if cs := doctorOf(t, "", errors.New("boom")); len(cs) != 1 || !cs[0].Warn {
		t.Fatalf("run error: %+v", cs)
	}
	if cs := doctorOf(t, "not json", nil); len(cs) != 1 || !cs[0].Warn {
		t.Fatalf("bad json: %+v", cs)
	}
}

func TestModNoteAndRoleFiles(t *testing.T) {
	reg, err := agent.Load("")
	if err != nil {
		t.Fatal(err)
	}
	if got := agent.ModNote(reg); got != "Claude Code "+ModsMinVersion {
		t.Fatalf("ModNote %q", got)
	}
	if got := agent.RoleFiles(reg); len(got) != 1 || got[0] != "CLAUDE.md" {
		t.Fatalf("RoleFiles %v", got)
	}
	if m, ok := load(t).(agent.Modder); !ok || m.ModRequired() {
		t.Fatal("claude's mod must be optional")
	}
}
