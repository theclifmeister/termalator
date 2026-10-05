package server

import (
	"testing"

	"github.com/theclifmeister/termilator/internal/agent"
	"github.com/theclifmeister/termilator/internal/session"
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
