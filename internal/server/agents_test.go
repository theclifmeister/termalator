package server

import (
	"os"
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

// TestWriteLaunchFilesFails: a runtime dir that can't be written to is an
// error, not a session started without its hooks and settings.
func TestWriteLaunchFilesFails(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
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
