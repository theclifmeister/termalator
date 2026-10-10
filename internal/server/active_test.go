package server

import (
	"io"
	"log"
	"path/filepath"
	"slices"
	"testing"

	"github.com/theclifmeister/terminatr/internal/config"
	"github.com/theclifmeister/terminatr/internal/project"
	"github.com/theclifmeister/terminatr/internal/thread"
)

// TestLoadPreviousDormant: a server start resumes only active projects'
// coordinators and threads; an inactive project's stay dormant, and a
// dormant session of a project activated meanwhile is resumed unless its
// thread was stopped or its project is gone (docs/SPEC.md §3.6, §5.1).
func TestLoadPreviousDormant(t *testing.T) {
	t.Setenv("TERMINATR_HOME", t.TempDir())
	on, err := project.New(project.Options{Slug: "on"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := project.New(project.Options{Slug: "off"}); err != nil {
		t.Fatal(err)
	}
	if err := config.SetProject("off", "active", false); err != nil {
		t.Fatal(err)
	}
	running, err := thread.Create(on, thread.Record{State: thread.Running, Session: "s-5"})
	if err != nil {
		t.Fatal(err)
	}
	stopped, err := thread.Create(on, thread.Record{State: thread.Stopped, Session: "s-6"})
	if err != nil {
		t.Fatal(err)
	}
	rec := func(id, role, slug, th string) SessionRecord {
		return SessionRecord{ID: id, Role: role, Project: slug, Thread: th, Agent: "claude", AgentSessionID: "a-" + id}
	}
	path := filepath.Join(t.TempDir(), "sessions.json")
	if err := saveState(path, &State{Shutdown: "clean", NextID: 9,
		Sessions: []SessionRecord{
			rec("s-1", "coordinator", "on", ""),
			rec("s-2", "coordinator", "off", ""),
			rec("s-3", "thread", "off", "t-0001"),
			rec("s-4", "shell", "", ""),
		},
		Dormant: []SessionRecord{
			rec("s-5", "thread", "on", running.ID),
			rec("s-6", "thread", "on", stopped.ID),
			rec("s-7", "coordinator", "gone", ""),
			rec("s-8", "coordinator", "off", ""),
		},
	}); err != nil {
		t.Fatal(err)
	}
	s := &Server{opts: Options{Paths: Paths{Sessions: path}}, log: log.New(io.Discard, "", 0),
		prevProject: map[string]string{}, dormant: map[string]SessionRecord{}, records: map[string]SessionRecord{}}
	resume, _ := s.loadPrevious()
	var ids []string
	for _, r := range resume {
		ids = append(ids, r.ID)
	}
	slices.Sort(ids)
	if want := []string{"s-1", "s-4", "s-5"}; !slices.Equal(ids, want) {
		t.Errorf("resumed %v, want %v", ids, want)
	}
	var dormant []string
	for id := range s.dormant {
		dormant = append(dormant, id)
	}
	slices.Sort(dormant)
	if want := []string{"s-2", "s-3", "s-8"}; !slices.Equal(dormant, want) {
		t.Errorf("dormant %v, want %v", dormant, want)
	}
	// Every save keeps the dormant records.
	if err := s.saveLocked(""); err != nil {
		t.Fatal(err)
	}
	st, err := loadState(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Dormant) != 3 || len(st.Sessions) != 0 {
		t.Errorf("saved %d dormant, %d sessions; want 3, 0", len(st.Dormant), len(st.Sessions))
	}
}

func TestMovePath(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"/h/projects/a", "/h/projects/b"},
		{"/h/projects/a/threads/t-0001/brief.md", "/h/projects/b/threads/t-0001/brief.md"},
		{"/h/worktrees/a/t-0001-x", "/h/worktrees/b/t-0001-x"},
		{"/h/projects/ab/x", "/h/projects/ab/x"},
		{"/elsewhere/x", "/elsewhere/x"},
	} {
		f := filepath.FromSlash
		if got := movePath(f(c.in), f("/h/projects/a"), f("/h/projects/b"), f("/h/worktrees/a"), f("/h/worktrees/b")); got != f(c.want) {
			t.Errorf("movePath(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	if got := sortLabels([]string{"t-0003", "coordinator", "t-0001"}); !slices.Equal(got, []string{"coordinator", "t-0001", "t-0003"}) {
		t.Errorf("sortLabels %v", got)
	}
}
