package tasks

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/theclifmeister/termilator/internal/caller"
	"github.com/theclifmeister/termilator/internal/mdfile"
)

// Error is a refusal with a stable code (exit 1 in the CLI, §6.3), such
// as unknown-task, human-only or coordinator-only.
type Error struct {
	Code string
	Msg  string
}

func (e *Error) Error() string { return e.Code + ": " + e.Msg }

func refuse(code, msg string) error { return &Error{Code: code, Msg: msg} }

// Events lets the project layer journal changes without this package
// knowing the project folder's other files.
type Events interface {
	// Journal records one applied change (§5.1 JOURNAL.md).
	Journal(c caller.Caller, action, ref, detail string) error
}

// Store applies task commands to one project folder.
type Store struct {
	Dir    string           // the project folder
	Slug   string           // the project's slug, for the caller check
	Now    func() time.Time // nil means time.Now
	Events Events           // nil means no journal
}

// TasksPath is <project>/TASKS.md.
func (s *Store) TasksPath() string { return filepath.Join(s.Dir, "TASKS.md") }

// ArchivePath is <project>/tasks/ARCHIVE.md.
func (s *Store) ArchivePath() string { return filepath.Join(s.Dir, "tasks", "ARCHIVE.md") }

func (s *Store) today() string {
	now := time.Now
	if s.Now != nil {
		now = s.Now
	}
	return now().Format("2006-01-02")
}

func readBoard(path string) (*Board, error) {
	data, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	b, err := Parse(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return b, nil
}

// Load reads TASKS.md.
func (s *Store) Load() (*Board, error) { return readBoard(s.TasksPath()) }

// LoadArchive reads tasks/ARCHIVE.md; its tasks have Archived set.
func (s *Store) LoadArchive() (*Board, error) {
	b, err := readBoard(s.ArchivePath())
	if err != nil {
		return nil, err
	}
	for _, t := range b.Tasks {
		t.Archived = true
	}
	return b, nil
}

// Get finds a task on the board or in the archive.
func (s *Store) Get(id int) (*Task, error) {
	b, err := s.Load()
	if err != nil {
		return nil, err
	}
	if t := b.Find(id); t != nil {
		return t, nil
	}
	a, err := s.LoadArchive()
	if err != nil {
		return nil, err
	}
	if t := a.Find(id); t != nil {
		return t, nil
	}
	return nil, unknown(id)
}

func unknown(id int) error { return refuse("unknown-task", fmt.Sprintf("T%d does not exist", id)) }

// op is the kind of change, for the caller rules of §6.4.
type op int

const (
	opWrite   op = iota // coordinator or human
	opOwnStep           // also the thread that runs the task
)

// allow applies the caller rules. t may be nil for adds.
func (s *Store) allow(c caller.Caller, t *Task, o op) error {
	if !c.IsAgent() {
		return nil
	}
	if c.Project != "" && s.Slug != "" && c.Project != s.Slug {
		return refuse("other-project", fmt.Sprintf("agents of project %s can't change project %s", c.Project, s.Slug))
	}
	if c.Kind == caller.Coordinator {
		return nil
	}
	if o == opOwnStep && t != nil && t.Thread != "" && t.Thread == c.Thread {
		return nil
	}
	if o == opOwnStep {
		return refuse("coordinator-only", "a thread may only add and tick steps on its own task")
	}
	return refuse("coordinator-only", "threads report through tm; the coordinator changes tasks")
}

// mutate is a locked read-modify-write of TASKS.md.
func (s *Store) mutate(fn func(b *Board) error) error {
	return mdfile.Update(s.TasksPath(), func(old []byte) ([]byte, error) {
		b, err := Parse(old)
		if err != nil {
			return nil, fmt.Errorf("%s: %w; fix the file by hand, tm won't rewrite it", s.TasksPath(), err)
		}
		if err := fn(b); err != nil {
			return nil, err
		}
		return b.Render(), nil
	})
}

// edit loads one live task under the lock, checks the caller and applies
// fn, which reports whether it changed anything.
func (s *Store) edit(c caller.Caller, id int, o op, fn func(t *Task) (bool, error)) (Result, error) {
	var res Result
	err := s.mutate(func(b *Board) error {
		t := b.Find(id)
		if t == nil {
			if a, err := s.LoadArchive(); err == nil && a.Find(id) != nil {
				return refuse("archived", fmt.Sprintf("T%d is archived; run tm task unarchive T%d first", id, id))
			}
			return unknown(id)
		}
		if err := s.allow(c, t, o); err != nil {
			return err
		}
		changed, err := fn(t)
		if err != nil {
			return err
		}
		if changed {
			t.Updated = s.today()
		}
		res = Result{Task: t, Changed: changed}
		return nil
	})
	return res, err
}

// Result is the outcome of a single-task command. Changed is false when
// the command was already true (exit 0, idempotent).
type Result struct {
	Task    *Task
	Changed bool
}

func (s *Store) journal(c caller.Caller, action string, t *Task, detail string) error {
	if s.Events == nil {
		return nil
	}
	return s.Events.Journal(c, action, t.Ref(), detail)
}

// NewTask is one task to add; zero fields take defaults.
type NewTask struct {
	Title  string   `json:"title"`
	Notes  string   `json:"notes,omitempty"`
	Steps  []string `json:"steps,omitempty"`
	Status string   `json:"status,omitempty"`
	Owner  string   `json:"owner,omitempty"`
}

// AddResult reports one NewTask: created, existing or failed.
type AddResult struct {
	Title  string `json:"title"`
	ID     string `json:"id,omitempty"`
	Result string `json:"result"`
	Code   string `json:"code,omitempty"`
	Error  string `json:"error,omitempty"`
}

// Add creates tasks. A title that exactly matches a task that isn't done
// is reported as existing instead, so a retried add is harmless. Valid
// items are saved even when others fail.
func (s *Store) Add(c caller.Caller, items []NewTask) ([]AddResult, error) {
	if err := s.allow(c, nil, opWrite); err != nil {
		return nil, err
	}
	archive, err := s.LoadArchive()
	if err != nil {
		return nil, err
	}
	var results []AddResult
	var created []*Task
	err = s.mutate(func(b *Board) error {
		results, created = nil, nil
		for _, a := range archive.Tasks {
			if a.ID >= b.NextID {
				b.NextID = a.ID + 1
			}
		}
		for _, it := range items {
			r, t := s.addOne(c, b, it)
			results = append(results, r)
			if t != nil {
				created = append(created, t)
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	for _, t := range created {
		if err := s.journal(c, "task.add", t, t.Title); err != nil {
			return results, err
		}
	}
	return results, nil
}

func (s *Store) addOne(c caller.Caller, b *Board, it NewTask) (AddResult, *Task) {
	fail := func(err error) (AddResult, *Task) {
		r := AddResult{Title: it.Title, Result: "failed", Error: err.Error()}
		var e *Error
		if errors.As(err, &e) {
			r.Code, r.Error = e.Code, e.Msg
		}
		return r, nil
	}
	title, err := checkTitle(it.Title)
	if err != nil {
		return fail(err)
	}
	for _, t := range b.Tasks {
		if t.Title == title && t.Status != Done {
			return AddResult{Title: title, ID: t.Ref(), Result: "existing"}, nil
		}
	}
	status := Open
	if it.Status != "" {
		st, ok := ParseStatus(it.Status)
		if !ok {
			return fail(refuse("invalid-status", fmt.Sprintf("unknown status %q", it.Status)))
		}
		if st == Done && c.IsAgent() {
			return fail(refuse("human-only", "only the human sets done"))
		}
		status = st
	}
	notes, err := checkNotes(it.Notes)
	if err != nil {
		return fail(err)
	}
	owner, err := checkLabel("owner", it.Owner)
	if err != nil {
		return fail(err)
	}
	t := &Task{ID: b.NextID, Title: title, Status: status, Notes: notes, Owner: owner, Created: s.today(), Updated: s.today()}
	for _, st := range it.Steps {
		text, err := checkStep(st)
		if err != nil {
			return fail(err)
		}
		t.Steps = append(t.Steps, Step{Text: text})
	}
	t.renumber()
	b.NextID++
	b.Tasks = append(b.Tasks, t)
	return AddResult{Title: title, ID: t.Ref(), Result: "created"}, t
}

// SetStatus moves a task. Only the human sets done (§6.4): an agent's
// call is refused with human-only, and the coordinator relays the user's
// acceptance with SetDoneApproved. note, if given, is appended to the
// notes and journaled.
func (s *Store) SetStatus(c caller.Caller, id int, st Status, note string) (Result, error) {
	if st == Done && c.IsAgent() {
		return s.refuseDone(c, id)
	}
	return s.setStatus(c, id, st, note, "")
}

// SetDoneApproved marks a task done on the coordinator's call once the
// user accepted the work and told it so (tm task status T12 done
// --approved-by-user); the journal says the user approved it.
func (s *Store) SetDoneApproved(c caller.Caller, id int, note string) (Result, error) {
	if c.IsAgent() && c.Kind != caller.Coordinator {
		return Result{}, refuse("coordinator-only", "threads report through tm done; the coordinator moves the task")
	}
	return s.setStatus(c, id, Done, note, " (approved by the user)")
}

// CompleteBySetting marks a task in review done on the ticker's call,
// as the project's complete_tasks setting says (the user's standing
// acceptance, §6.4): why says what shipped it, e.g. "released in v0.5.0
// (PR #65)". A task no longer in review is left alone (Changed false).
// It is journaled as "ticker task.done T12 <why>" and noted on the task.
func (s *Store) CompleteBySetting(c caller.Caller, id int, why string) (Result, error) {
	if c.IsAgent() {
		return Result{}, refuse("human-only", "only the user accepts work; the ticker does it on their setting")
	}
	res, err := s.edit(c, id, opWrite, func(t *Task) (bool, error) {
		if t.Status != Review {
			return false, nil
		}
		t.Status = Done
		line := fmt.Sprintf("%s (%s): %s, by the project's setting", Done, s.today(), why)
		if n, err := checkNotes(joinNotes(t.Notes, line)); err == nil {
			t.Notes = n
		}
		return true, nil
	})
	if err != nil || !res.Changed {
		return res, err
	}
	return res, s.journal(c, "task.done", res.Task, why)
}

func (s *Store) setStatus(c caller.Caller, id int, st Status, note, approved string) (Result, error) {
	res, err := s.edit(c, id, opWrite, func(t *Task) (bool, error) {
		changed := t.Status != st
		t.Status = st
		if note != "" {
			line := fmt.Sprintf("%s (%s): %s", st, s.today(), note)
			n, err := checkNotes(joinNotes(t.Notes, line))
			if err != nil {
				return false, err
			}
			t.Notes, changed = n, true
		}
		return changed, nil
	})
	if err != nil || !res.Changed {
		return res, err
	}
	return res, s.journal(c, "task.status", res.Task, joinDetail(string(st), note)+approved)
}

// refuseDone answers an agent's plain request to set done: refused, or
// already true.
func (s *Store) refuseDone(c caller.Caller, id int) (Result, error) {
	if c.Kind != caller.Coordinator {
		return Result{}, refuse("coordinator-only", "threads report through tm done; the coordinator moves the task")
	}
	t, err := s.Get(id)
	if err != nil {
		return Result{}, err
	}
	if err := s.allow(c, t, opWrite); err != nil {
		return Result{}, err
	}
	if t.Archived {
		return Result{}, refuse("archived", fmt.Sprintf("%s is archived", t.Ref()))
	}
	if t.Status == Done {
		return Result{Task: t}, nil
	}
	return Result{}, refuse("human-only", fmt.Sprintf("only the user accepts work: once they tell you %s is done, run tm task status %s done --approved-by-user", t.Ref(), t.Ref()))
}

// Edit changes title, notes or owner; nil fields are left alone.
func (s *Store) Edit(c caller.Caller, id int, title, notes, owner *string) (Result, error) {
	var t2, n2, o2 string
	var err error
	if title != nil {
		if t2, err = checkTitle(*title); err != nil {
			return Result{}, err
		}
	}
	if notes != nil {
		if *notes != "" {
			if n2, err = checkNotes(*notes); err != nil {
				return Result{}, err
			}
		}
	}
	if owner != nil {
		if o2, err = checkLabel("owner", *owner); err != nil {
			return Result{}, err
		}
	}
	res, err := s.edit(c, id, opWrite, func(t *Task) (bool, error) {
		changed := false
		if title != nil && t.Title != t2 {
			t.Title, changed = t2, true
		}
		if notes != nil && t.Notes != n2 {
			t.Notes, changed = n2, true
		}
		if owner != nil && t.Owner != o2 {
			t.Owner, changed = o2, true
		}
		return changed, nil
	})
	if err != nil || !res.Changed {
		return res, err
	}
	return res, s.journal(c, "task.edit", res.Task, "")
}

// SetThread links a task to the thread running it (delegation, §6.5).
func (s *Store) SetThread(c caller.Caller, id int, thread string) (Result, error) {
	th, err := checkLabel("thread", thread)
	if err != nil {
		return Result{}, err
	}
	res, err := s.edit(c, id, opWrite, func(t *Task) (bool, error) {
		changed := t.Thread != th
		t.Thread = th
		return changed, nil
	})
	if err != nil || !res.Changed {
		return res, err
	}
	return res, s.journal(c, "task.thread", res.Task, th)
}

// StepAdd appends a step. Not idempotent by design: two calls add two
// steps, as a plan may repeat a step's wording.
func (s *Store) StepAdd(c caller.Caller, id int, text string) (Result, error) {
	text, err := checkStep(text)
	if err != nil {
		return Result{}, err
	}
	res, err := s.edit(c, id, opOwnStep, func(t *Task) (bool, error) {
		t.Steps = append(t.Steps, Step{Text: text})
		t.renumber()
		return true, nil
	})
	if err != nil {
		return res, err
	}
	return res, s.journal(c, "task.steps.add", res.Task, fmt.Sprintf("%d %s", len(res.Task.Steps), text))
}

func stepAt(t *Task, n int) (*Step, error) {
	if n < 1 || n > len(t.Steps) {
		return nil, refuse("unknown-step", fmt.Sprintf("%s has no step %d (it has %d)", t.Ref(), n, len(t.Steps)))
	}
	return &t.Steps[n-1], nil
}

// StepCheck checks or unchecks step n; idempotent.
func (s *Store) StepCheck(c caller.Caller, id, n int, done bool) (Result, error) {
	res, err := s.edit(c, id, opOwnStep, func(t *Task) (bool, error) {
		st, err := stepAt(t, n)
		if err != nil {
			return false, err
		}
		changed := st.Done != done
		st.Done = done
		return changed, nil
	})
	if err != nil || !res.Changed {
		return res, err
	}
	action := "task.steps.check"
	if !done {
		action = "task.steps.uncheck"
	}
	return res, s.journal(c, action, res.Task, fmt.Sprint(n))
}

// StepRename changes step n's text; idempotent.
func (s *Store) StepRename(c caller.Caller, id, n int, text string) (Result, error) {
	text, err := checkStep(text)
	if err != nil {
		return Result{}, err
	}
	res, err := s.edit(c, id, opWrite, func(t *Task) (bool, error) {
		st, err := stepAt(t, n)
		if err != nil {
			return false, err
		}
		changed := st.Text != text
		st.Text = text
		return changed, nil
	})
	if err != nil || !res.Changed {
		return res, err
	}
	return res, s.journal(c, "task.steps.rename", res.Task, fmt.Sprintf("%d %s", n, text))
}

// StepRemove deletes step n; the later steps move up.
func (s *Store) StepRemove(c caller.Caller, id, n int) (Result, error) {
	res, err := s.edit(c, id, opWrite, func(t *Task) (bool, error) {
		if _, err := stepAt(t, n); err != nil {
			return false, err
		}
		t.Steps = append(t.Steps[:n-1], t.Steps[n:]...)
		t.renumber()
		return true, nil
	})
	if err != nil {
		return res, err
	}
	return res, s.journal(c, "task.steps.remove", res.Task, fmt.Sprint(n))
}

// Archive moves a task to tasks/ARCHIVE.md; idempotent.
func (s *Store) Archive(c caller.Caller, id int) (Result, error) {
	return s.move(c, id, true)
}

// Unarchive moves a task back to TASKS.md; idempotent.
func (s *Store) Unarchive(c caller.Caller, id int) (Result, error) {
	return s.move(c, id, false)
}

// move moves a task between TASKS.md and ARCHIVE.md, holding both locks
// (always TASKS.md first). The archive is written before TASKS.md, so a
// crash in between leaves the task in both files rather than in neither;
// the next move repairs that.
func (s *Store) move(c caller.Caller, id int, archive bool) (Result, error) {
	if err := os.MkdirAll(filepath.Dir(s.ArchivePath()), 0o755); err != nil {
		return Result{}, err
	}
	var res Result
	err := s.mutate(func(b *Board) error {
		return mdfile.Update(s.ArchivePath(), func(old []byte) ([]byte, error) {
			a, err := Parse(old)
			if err != nil {
				return nil, fmt.Errorf("%s: %w; fix the file by hand, tm won't rewrite it", s.ArchivePath(), err)
			}
			from, to := b, a
			if !archive {
				from, to = a, b
			}
			t := from.Find(id)
			if t == nil {
				if t = to.Find(id); t == nil {
					return nil, unknown(id)
				}
				if err := s.allow(c, t, opWrite); err != nil {
					return nil, err
				}
				res = Result{Task: t}
				return old, nil // already where it should be
			}
			if err := s.allow(c, t, opWrite); err != nil {
				return nil, err
			}
			from.Remove(id)
			to.Remove(id) // a leftover copy from an interrupted move
			t.Updated = s.today()
			to.Tasks = append(to.Tasks, t)
			t.Archived = archive
			res = Result{Task: t, Changed: true}
			return a.RenderArchive(), nil
		})
	})
	if err != nil || !res.Changed {
		return res, err
	}
	action := "task.archive"
	if !archive {
		action = "task.unarchive"
	}
	return res, s.journal(c, action, res.Task, "")
}

func joinNotes(notes, line string) string {
	if notes == "" {
		return line
	}
	return notes + "\n\n" + line
}

func joinDetail(a, b string) string {
	if b == "" {
		return a
	}
	return a + ": " + b
}
