package server

import (
	"os"
	"strings"
	"testing"

	"github.com/theclifmeister/terminatr/internal/agent"
	"github.com/theclifmeister/terminatr/internal/project"
	"github.com/theclifmeister/terminatr/internal/session"
)

// TestWorked: a kickoff the agent hasn't started on doesn't mark the
// session prompted, or a restart would resume a conversation Claude never
// saved.
func TestWorked(t *testing.T) {
	for _, c := range []struct {
		st   agent.Merged
		want bool
	}{
		{agent.Merged{State: agent.StateWorking}, true},
		{agent.Merged{State: agent.StateWorking, Reason: "background"}, true},
		{agent.Merged{State: agent.StateBlocked, Reason: "permission"}, true},
		{agent.Merged{State: agent.StateWorking, Reason: agent.ReasonKickoff}, false},
		{agent.Merged{State: agent.StateIdle}, false},
		{agent.Merged{State: agent.StateUnknown}, false},
	} {
		if got := worked(session.AgentState{Merged: c.st}); got != c.want {
			t.Errorf("worked(%s/%s) = %v", c.st.State, c.st.Reason, got)
		}
	}
}

// TestOwnWorktree: only a worktree tm made (worktrees/<slug>/<dir>) is
// trusted ahead of launch, never the folder above it or elsewhere.
func TestOwnWorktree(t *testing.T) {
	home := t.TempDir()
	s := &Server{opts: Options{Paths: Paths{Home: home}}}
	for dir, want := range map[string]bool{
		home + "/worktrees/demo/t-0001-fix":       true,
		home + "/worktrees/demo/t-0001-fix/sub":   false,
		home + "/worktrees/demo":                  false,
		home + "/worktrees":                       false,
		home + "/projects/demo":                   false,
		home + "/worktrees/demo/../../projects/x": false,
		"/tmp/elsewhere":                          false,
	} {
		if got := s.ownWorktree(dir); got != want {
			t.Errorf("ownWorktree(%s) = %v", dir, got)
		}
	}
}

// TestWriteLaunchFilesFails: a runtime dir that can't be written to is an
// error, not a session started without its hooks and settings.
func TestWriteLaunchFilesFails(t *testing.T) {
	if os.Geteuid() == 0 || !chmodApplies {
		t.Skip("directory permissions don't apply")
	}
	rt := t.TempDir()
	if err := writeLaunchFiles(rt, map[string][]byte{"settings.json": []byte("{}"), "hooks/a": []byte("x")}); err != nil {
		t.Fatalf("writable dir: %v", err)
	}
	for _, name := range []string{"brief.md", "sub/hook.sh"} {
		ro := t.TempDir()
		if err := os.Chmod(ro, 0o500); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { os.Chmod(ro, 0o700) })
		if err := writeLaunchFiles(ro, map[string][]byte{name: []byte("x")}); err == nil {
			t.Errorf("%s in an unwritable runtime dir: no error", name)
		}
	}
}

// TestCoordinatorEssentials: after a clear the coordinator gets all of
// `tm context` (T109), within the budget, with when to run it again.
func TestCoordinatorEssentials(t *testing.T) {
	secs := []project.Section{
		{Title: "Project", Lines: []string{"Project: demo (demo)"}},
		{Title: "Context (CONTEXT.md)", Lines: []string{"## Where things stand"}},
		{Title: "Memory index (MEMORY.md)", Lines: []string{"- a memory"}},
		{Title: "Tasks", Lines: []string{"T1 a task"}},
		{Title: "Journal (last 10)", Lines: []string{"a journal line"}},
	}
	got := coordinatorEssentials(secs)
	for _, want := range []string{"## Project\n", "Where things stand", "a memory", "## Tasks\n", "a journal line",
		"That is `tm context` as of this conversation's start; run it"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q", want)
		}
	}
	long := make([]string, 2000)
	for i := range long {
		long[i] = "T1 a task with a long enough title to fill the budget"
	}
	secs[3].Lines = long
	got = coordinatorEssentials(secs)
	if len(got) > coordinatorBudget {
		t.Fatalf("%d bytes, over the budget", len(got))
	}
	if !strings.Contains(got, "[… cut at the size budget]") || !strings.HasSuffix(got, "the current state.\n") {
		t.Fatalf("no cut mark or no last line:\n%s", got[len(got)-300:])
	}
}
