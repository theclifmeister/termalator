package server

import (
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/theclifmeister/terminatr/internal/agent"
	"github.com/theclifmeister/terminatr/internal/caller"
	"github.com/theclifmeister/terminatr/internal/project"
	"github.com/theclifmeister/terminatr/internal/proto"
	"github.com/theclifmeister/terminatr/internal/tasks"
	"github.com/theclifmeister/terminatr/internal/thread"
)

var human = caller.Caller{Kind: caller.Human}

func newWatchProject(t *testing.T) *project.Project {
	t.Helper()
	p, err := project.New(project.Options{Name: "Demo"})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// TestWatchOf: a thread session's Watch carries its task, current item,
// question, PR link and its project's waiting work.
func TestWatchOf(t *testing.T) {
	testPaths(t)
	p := newWatchProject(t)
	if _, err := p.Tasks().Add(human, []tasks.NewTask{
		{Title: "Fix the login", Status: "started", Steps: []string{"Write", "Test"}},
		{Title: "Check it", Status: "review"},
		{Title: "Later", Status: "ready"},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Tasks().StepCheck(human, 1, 1, true); err != nil {
		t.Fatal(err)
	}
	rec, err := thread.Create(p, thread.Record{Title: "Fix the login", Task: "T1"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := thread.UpdateStatus(p, rec.ID, func(st *thread.Status) error {
		st.NeedsYou = "which port?"
		st.Todos = []agent.Todo{{Text: "Doing the test", Status: agent.TodoInProgress}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := p.AddItem("needs-you", rec.ID, "waiting", true); err != nil {
		t.Fatal(err)
	}
	info := proto.SessionInfo{ID: "s-3", Role: proto.RoleThread, Agent: "claude", Project: p.Slug, Thread: rec.ID,
		State: "working", Reason: "tool", Queued: 2}
	w := watchOf(info, filepath.Join(t.TempDir(), "none.json"))
	want := proto.WatchSession{ID: "s-3", Role: proto.RoleThread, Agent: "claude", Project: p.Slug, Thread: rec.ID,
		State: "working", Reason: "tool", NeedsYou: "which port?"}
	if w.Session != want {
		t.Errorf("session %+v, want %+v", w.Session, want)
	}
	if w.Task == nil || *w.Task != (proto.WatchTask{ID: "T1", Title: "Fix the login", Status: "started", StepsDone: 1, StepsTotal: 2,
		Current: w.Task.Current}) || w.Task.Current == "" {
		t.Errorf("task %+v", w.Task)
	}
	if w.NeedsYou != 1 || w.Inbox != 1 || w.Queued != 2 || w.PR != "" {
		t.Errorf("watch %+v", w)
	}

	// The ticker's PR, behind its base: said in the same words.
	st := filepath.Join(t.TempDir(), "ticker.json")
	os.WriteFile(st, []byte(`{"threads": {"`+p.Slug+`/`+rec.ID+`": {"pr": {"number": 12, "url": "https://github.com/o/r/pull/12",
		"state": "OPEN", "checks": "fail", "failed": 2, "base": "main", "merge_state": "BEHIND"}}}}`), 0o600)
	if w := watchOf(info, st); w.PR != "#12 open, 2 checks failed, behind main" || w.PRURL != "https://github.com/o/r/pull/12" {
		t.Errorf("pr %q %q", w.PR, w.PRURL)
	}

	// Not a thread: the project's counts, no task.
	info.Role, info.Thread = proto.RoleCoordinator, ""
	if w := watchOf(info, ""); w.Task != nil || w.NeedsYou != 1 || w.Inbox != 1 {
		t.Errorf("coordinator watch %+v", w)
	}
}

// TestWatchStream: session.watch answers the state, sends a line when it
// changes behind the server's back within a second (the re-read), and a
// last line with state "exited" when the session ends, at once (the
// server's own change wakes it).
func TestWatchStream(t *testing.T) {
	p := testPaths(t)
	var needs atomic.Int64
	watchFunc = func(info proto.SessionInfo, _ string) proto.Watch {
		w := watchOf(info, "")
		w.NeedsYou = int(needs.Load())
		return w
	}
	// Registered before the server's cleanup, so it runs after it.
	t.Cleanup(func() { watchFunc = watchOf })
	startServer(t, p)
	c, err := Dial(p, proto.KindControl)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	var started proto.SessionStartResult
	call(t, c, proto.MethodSessionStart, proto.SessionStartParams{Argv: []string{"/bin/sh"}, Cwd: "/"}, &started)
	id := started.Session.ID

	if _, _, err := WatchSession(p, "s-999"); err == nil {
		t.Fatal("watched an unknown session")
	}
	watch := func(poll string) <-chan proto.Watch {
		t.Helper()
		t.Setenv(envWatchPoll, poll)
		st, w, err := WatchSession(p, id)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { st.Close() })
		if w.Session.ID != id || w.Session.Role != proto.RoleShell || w.NeedsYou != int(needs.Load()) {
			t.Fatalf("first state %+v", w)
		}
		got := make(chan proto.Watch, 16)
		go func() {
			defer close(got)
			for {
				w, err := st.Next()
				if err != nil {
					return
				}
				got <- w
			}
		}()
		return got
	}
	next := func(got <-chan proto.Watch, what string, ok func(proto.Watch) bool) {
		t.Helper()
		deadline := time.After(time.Second)
		for {
			select {
			case w, open := <-got:
				if !open {
					t.Fatalf("stream ended waiting for %s", what)
				}
				if ok(w) {
					return
				}
			case <-deadline:
				t.Fatalf("no line with %s within 1s", what)
			}
		}
	}
	polled := watch("100ms")
	woken := watch("1h")
	needs.Store(1)
	next(polled, "needs_you 1", func(w proto.Watch) bool { return w.NeedsYou == 1 })

	call(t, c, proto.MethodSessionStop, proto.SessionIDParams{ID: id}, nil)
	exited := func(w proto.Watch) bool { return w.Session.State == string(agent.StateExited) }
	next(woken, "state exited", exited)
	next(polled, "state exited", exited)
	select {
	case _, open := <-woken:
		if open {
			t.Fatal("a line after exited")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the stream didn't end with the session")
	}
}
