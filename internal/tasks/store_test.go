package tasks

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/theclifmeister/termilator/internal/caller"
)

var (
	human  = caller.Caller{Kind: caller.Human}
	coord  = caller.Caller{Kind: caller.Coordinator, Project: "demo"}
	thread = caller.Caller{Kind: caller.Thread, Project: "demo", Thread: "t-0001"}
)

type fakeEvents struct {
	journal []string
}

func (f *fakeEvents) Journal(c caller.Caller, action, ref, detail string) error {
	f.journal = append(f.journal, strings.TrimSpace(c.String()+" "+action+" "+ref+" "+detail))
	return nil
}

func newStore(t *testing.T) (*Store, *fakeEvents) {
	t.Helper()
	ev := &fakeEvents{}
	s := &Store{
		Dir:    t.TempDir(),
		Slug:   "demo",
		Now:    func() time.Time { return time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC) },
		Events: ev,
	}
	return s, ev
}

func code(err error) string {
	var e *Error
	if errors.As(err, &e) {
		return e.Code
	}
	if err != nil {
		return "other: " + err.Error()
	}
	return ""
}

func mustAdd(t *testing.T, s *Store, c caller.Caller, items ...NewTask) []AddResult {
	t.Helper()
	r, err := s.Add(c, items)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestAddIdempotentAndPartial(t *testing.T) {
	s, ev := newStore(t)
	r := mustAdd(t, s, coord,
		NewTask{Title: "Fix login", Notes: "Users land on /home", Steps: []string{"Reproduce", "Fix"}},
		NewTask{Title: "  "},
		NewTask{Title: "Ship", Status: "ready"},
		NewTask{Title: "Sneaky", Status: "done"},
		NewTask{Title: "Odd", Status: "maybe"},
	)
	got := []string{}
	for _, x := range r {
		got = append(got, x.Result+":"+x.ID+x.Code)
	}
	want := "created:T1 failed:empty-title created:T2 failed:human-only failed:invalid-status"
	if strings.Join(got, " ") != want {
		t.Fatalf("got %v", got)
	}
	r = mustAdd(t, s, coord, NewTask{Title: "Fix login"}, NewTask{Title: "New"})
	if r[0].Result != "existing" || r[0].ID != "T1" || r[1].ID != "T3" {
		t.Fatalf("second add %+v", r)
	}
	if len(ev.journal) != 3 {
		t.Fatalf("journal %v", ev.journal)
	}
	b, _ := s.Load()
	if b.NextID != 4 || b.Find(1).Created != "2026-10-04" || len(b.Find(1).Steps) != 2 {
		t.Fatalf("board %+v", b)
	}
}

func TestThreadMayNotAdd(t *testing.T) {
	s, _ := newStore(t)
	if _, err := s.Add(thread, []NewTask{{Title: "x"}}); code(err) != "coordinator-only" {
		t.Fatalf("err %v", err)
	}
	other := caller.Caller{Kind: caller.Coordinator, Project: "other"}
	if _, err := s.Add(other, []NewTask{{Title: "x"}}); code(err) != "other-project" {
		t.Fatalf("err %v", err)
	}
}

func TestStatusAndDoneRule(t *testing.T) {
	s, ev := newStore(t)
	mustAdd(t, s, coord, NewTask{Title: "A"})

	res, err := s.SetStatus(coord, 1, Started, "")
	if err != nil || !res.Changed {
		t.Fatalf("%v %+v", err, res)
	}
	res, err = s.SetStatus(coord, 1, Started, "")
	if err != nil || res.Changed {
		t.Fatalf("not idempotent: %v %+v", err, res)
	}
	if _, err := s.SetStatus(thread, 1, Review, ""); code(err) != "coordinator-only" {
		t.Fatalf("thread status: %v", err)
	}
	if _, err := s.SetStatus(thread, 1, Done, ""); code(err) != "coordinator-only" {
		t.Fatalf("thread done: %v", err)
	}
	_, err = s.SetStatus(coord, 1, Done, "")
	if code(err) != "human-only" || !strings.Contains(err.Error(), "--approved-by-user") {
		t.Fatalf("coordinator done: %v", err)
	}
	if tk, _ := s.Get(1); tk.Status != Started {
		t.Fatalf("status changed to %s", tk.Status)
	}
	res, err = s.SetStatus(human, 1, Done, "")
	if err != nil || !res.Changed {
		t.Fatalf("human done: %v %+v", err, res)
	}
	// An agent asking again once it's done is already true.
	if _, err := s.SetStatus(coord, 1, Done, ""); err != nil {
		t.Fatalf("done again: %v", err)
	}

	// The coordinator relays the user's acceptance; a thread can't.
	mustAdd(t, s, coord, NewTask{Title: "B"})
	if _, err := s.SetDoneApproved(thread, 2, ""); code(err) != "coordinator-only" {
		t.Fatalf("thread approved done: %v", err)
	}
	res, err = s.SetDoneApproved(coord, 2, "")
	if err != nil || !res.Changed || res.Task.Status != Done {
		t.Fatalf("approved done: %v %+v", err, res)
	}
	if j := ev.journal[len(ev.journal)-1]; j != "coordinator task.status T2 done (approved by the user)" {
		t.Fatalf("journal %q", j)
	}
	if _, err := s.SetStatus(coord, 9, Ready, ""); code(err) != "unknown-task" {
		t.Fatalf("unknown: %v", err)
	}
}

func TestStatusNote(t *testing.T) {
	s, _ := newStore(t)
	mustAdd(t, s, coord, NewTask{Title: "A", Notes: "Context."})
	if _, err := s.SetStatus(coord, 1, Blocked, "waiting for API key"); err != nil {
		t.Fatal(err)
	}
	tk, _ := s.Get(1)
	if tk.Notes != "Context.\n\nblocked (2026-10-04): waiting for API key" {
		t.Fatalf("notes %q", tk.Notes)
	}
}

func TestStepsRules(t *testing.T) {
	s, ev := newStore(t)
	mustAdd(t, s, coord, NewTask{Title: "Mine"}, NewTask{Title: "Other"})
	if _, err := s.SetThread(coord, 1, "t-0001"); err != nil {
		t.Fatal(err)
	}
	for _, txt := range []string{"Plan", "Build", "Test"} {
		if _, err := s.StepAdd(thread, 1, txt); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.StepAdd(thread, 2, "nope"); code(err) != "coordinator-only" {
		t.Fatalf("other task: %v", err)
	}
	res, err := s.StepCheck(thread, 1, 2, true)
	if err != nil || !res.Changed {
		t.Fatal(err)
	}
	if res, _ = s.StepCheck(thread, 1, 2, true); res.Changed {
		t.Fatal("check not idempotent")
	}
	if _, err := s.StepCheck(thread, 1, 7, true); code(err) != "unknown-step" {
		t.Fatalf("step 7: %v", err)
	}
	if _, err := s.StepRename(thread, 1, 1, "x"); code(err) != "coordinator-only" {
		t.Fatalf("thread rename: %v", err)
	}
	if _, err := s.StepRemove(coord, 1, 1); err != nil {
		t.Fatal(err)
	}
	tk, _ := s.Get(1)
	if len(tk.Steps) != 2 || tk.Steps[0].Text != "Build" || !tk.Steps[0].Done || tk.Steps[1].N != 2 {
		t.Fatalf("steps %+v", tk.Steps)
	}
	if tk.Status != Open {
		t.Fatal("steps changed status")
	}
	if !strings.Contains(strings.Join(ev.journal, "\n"), "t-0001 task.steps.check T1 2") {
		t.Fatalf("journal %v", ev.journal)
	}
}

func TestEdit(t *testing.T) {
	s, _ := newStore(t)
	mustAdd(t, s, coord, NewTask{Title: "A", Notes: "n"})
	title, notes, empty := "B", "new notes", ""
	if res, err := s.Edit(coord, 1, &title, &notes, nil); err != nil || !res.Changed {
		t.Fatal(err)
	}
	if res, _ := s.Edit(coord, 1, &title, &notes, nil); res.Changed {
		t.Fatal("edit not idempotent")
	}
	if _, err := s.Edit(coord, 1, nil, &empty, nil); err != nil {
		t.Fatal(err)
	}
	bad := "## Done"
	if _, err := s.Edit(coord, 1, nil, &bad, nil); code(err) != "invalid-notes" {
		t.Fatalf("bad notes: %v", err)
	}
	if _, err := s.Edit(thread, 1, &title, nil, nil); code(err) != "coordinator-only" {
		t.Fatalf("thread edit: %v", err)
	}
	tk, _ := s.Get(1)
	if tk.Title != "B" || tk.Notes != "" {
		t.Fatalf("%+v", tk)
	}
}

func TestArchive(t *testing.T) {
	s, _ := newStore(t)
	mustAdd(t, s, coord, NewTask{Title: "A"}, NewTask{Title: "B"})
	if res, err := s.Archive(coord, 2); err != nil || !res.Changed {
		t.Fatal(err)
	}
	if res, err := s.Archive(coord, 2); err != nil || res.Changed {
		t.Fatalf("archive not idempotent: %v", err)
	}
	if _, err := s.SetStatus(coord, 2, Ready, ""); code(err) != "archived" {
		t.Fatalf("status on archived: %v", err)
	}
	tk, err := s.Get(2)
	if err != nil || !tk.Archived {
		t.Fatalf("get archived: %v %+v", err, tk)
	}
	// Ids are never reused, even if TASKS.md loses its front matter.
	os.WriteFile(s.TasksPath(), []byte("# Tasks\n\n### T1 A\nstatus: open\n"), 0o644)
	r := mustAdd(t, s, coord, NewTask{Title: "C"})
	if r[0].ID != "T3" {
		t.Fatalf("reused id: %+v", r)
	}
	if res, err := s.Unarchive(coord, 2); err != nil || !res.Changed {
		t.Fatal(err)
	}
	if _, err := s.Unarchive(coord, 2); err != nil {
		t.Fatal(err)
	}
	b, _ := s.Load()
	a, _ := s.LoadArchive()
	if b.Find(2) == nil || len(a.Tasks) != 0 {
		t.Fatalf("after unarchive: board %d tasks, archive %d", len(b.Tasks), len(a.Tasks))
	}
	if _, err := s.Archive(thread, 1); code(err) != "coordinator-only" {
		t.Fatalf("thread archive: %v", err)
	}
}

func TestArchiveRepairsInterruptedMove(t *testing.T) {
	s, _ := newStore(t)
	mustAdd(t, s, coord, NewTask{Title: "A"})
	os.MkdirAll(filepath.Dir(s.ArchivePath()), 0o755)
	os.WriteFile(s.ArchivePath(), []byte("# Archive\n\n### T1 A\nstatus: open\n"), 0o644)
	if _, err := s.Archive(coord, 1); err != nil {
		t.Fatal(err)
	}
	b, _ := s.Load()
	a, _ := s.LoadArchive()
	if len(b.Tasks) != 0 || len(a.Tasks) != 1 {
		t.Fatalf("board %d, archive %d", len(b.Tasks), len(a.Tasks))
	}
}

func TestRefusesBrokenFile(t *testing.T) {
	s, _ := newStore(t)
	broken := "# Tasks\n\n### T1 A\nstatus: whenever\n"
	os.WriteFile(s.TasksPath(), []byte(broken), 0o644)
	_, err := s.Add(coord, []NewTask{{Title: "B"}})
	var pe *ParseError
	if !errors.As(err, &pe) || pe.Line != 4 {
		t.Fatalf("err %v", err)
	}
	raw, _ := os.ReadFile(s.TasksPath())
	if string(raw) != broken {
		t.Fatal("rewrote a broken file")
	}
}
