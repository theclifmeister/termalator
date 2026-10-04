package thread

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"
	"time"

	"github.com/theclifmeister/termalator/internal/agent"
	"github.com/theclifmeister/termalator/internal/mdfile"
	"github.com/theclifmeister/termalator/internal/project"
	"github.com/theclifmeister/termalator/internal/tasks"
)

// SelfReportTTL is how long a `tm status` percent counts (docs/SPEC.md §7.3).
const SelfReportTTL = 5 * time.Minute

// Status is threads/<id>/STATUS.md: TOML front matter plus the mirrored
// todo list. Only tm writes it: the server on todo changes, tm on step
// changes, tm status and tm done.
type Status struct {
	Percent       int          `toml:"percent" json:"percent"`
	PercentSource string       `toml:"percent_source" json:"percent_source"` // steps+todos, steps, todos, self, done or ""
	Current       string       `toml:"current" json:"current"`
	StepsDone     int          `toml:"steps_done" json:"steps_done"`
	StepsTotal    int          `toml:"steps_total" json:"steps_total"`
	TodosDone     int          `toml:"todos_done" json:"todos_done"`
	TodosTotal    int          `toml:"todos_total" json:"todos_total"`
	Activity      string       `toml:"activity" json:"activity"`                  // last self-report
	NeedsYou      string       `toml:"needs_you" json:"needs_you"`                // the question, when blocked on the human
	SelfPercent   int          `toml:"self_percent" json:"self_percent"`          // -1: unknown
	SelfAt        time.Time    `toml:"self_at,omitempty" json:"self_at,omitzero"` // when tm status was called
	Done          bool         `toml:"done,omitempty" json:"done,omitempty"`      // tm done was called
	Updated       time.Time    `toml:"updated" json:"updated"`                    //
	Todos         []agent.Todo `toml:"-" json:"todos"`
}

// ParseStatus reads STATUS.md's text.
func ParseStatus(data []byte) (*Status, error) {
	st := &Status{SelfPercent: -1}
	body, err := mdfile.Decode(data, st)
	if err != nil {
		return nil, err
	}
	for _, l := range strings.Split(string(body), "\n") {
		l = strings.TrimSpace(l)
		if len(l) < 6 || !strings.HasPrefix(l, "- [") || l[4] != ']' {
			continue
		}
		t := agent.Todo{Text: strings.TrimSpace(l[5:])}
		switch l[3] {
		case 'x':
			t.Status = agent.TodoCompleted
		case '~':
			t.Status = agent.TodoInProgress
			t.Text = strings.TrimSpace(strings.TrimSuffix(t.Text, "(in progress)"))
		case ' ':
			t.Status = agent.TodoPending
		default:
			continue
		}
		st.Todos = append(st.Todos, t)
	}
	return st, nil
}

// Render is STATUS.md's text.
func (st *Status) Render() ([]byte, error) {
	var b strings.Builder
	b.WriteString("## Todos\n")
	if len(st.Todos) == 0 {
		b.WriteString("(none)\n")
	}
	for _, t := range st.Todos {
		text := strings.Join(strings.Fields(t.Text), " ")
		switch t.Status {
		case agent.TodoCompleted:
			fmt.Fprintf(&b, "- [x] %s\n", text)
		case agent.TodoInProgress:
			fmt.Fprintf(&b, "- [~] %s  (in progress)\n", text)
		default:
			fmt.Fprintf(&b, "- [ ] %s\n", text)
		}
	}
	return mdfile.Join(st, []byte(b.String()))
}

// Derive recomputes the percent, counts and current item from the task's
// steps, the todos and the self-report (docs/SPEC.md §7.3).
func (st *Status) Derive(steps []tasks.Step, now time.Time) {
	S, s := len(steps), 0
	for _, x := range steps {
		if x.Done {
			s++
		}
	}
	T, c := len(st.Todos), 0
	current := ""
	for _, t := range st.Todos {
		switch t.Status {
		case agent.TodoCompleted:
			c++
		case agent.TodoInProgress:
			if current == "" {
				current = t.Text
			}
		}
	}
	if current == "" {
		for _, x := range steps {
			if !x.Done {
				current = x.Text
				break
			}
		}
	}
	st.StepsDone, st.StepsTotal, st.TodosDone, st.TodosTotal, st.Current = s, S, c, T, current
	self := -1
	if st.SelfPercent >= 0 && now.Sub(st.SelfAt) < SelfReportTTL {
		self = st.SelfPercent
	}
	pr := DeriveProgress(ProgressInput{StepsDone: s, StepsTotal: S, TodosDone: c, TodosTotal: T, Self: self, Done: st.Done})
	st.Percent, st.PercentSource = max(pr.Percent, 0), pr.Source
}

// ReadStatus reads a thread's STATUS.md; a missing file is an empty
// status.
func ReadStatus(p *project.Project, id string) (*Status, error) {
	data, err := os.ReadFile(Path(p, id, "STATUS.md"))
	if errors.Is(err, fs.ErrNotExist) {
		return &Status{SelfPercent: -1}, nil
	}
	if err != nil {
		return nil, err
	}
	return ParseStatus(data)
}

// UpdateStatus is a locked read-modify-write of STATUS.md: fn changes the
// inputs (todos, self-report, done), then the derived fields are
// recomputed from the linked task's steps. fn may be nil to refresh after
// a step change.
func UpdateStatus(p *project.Project, id string, fn func(st *Status) error) (*Status, error) {
	r, err := Load(p, id)
	if err != nil {
		return nil, err
	}
	var steps []tasks.Step
	if tid := r.TaskID(); tid > 0 {
		if t, err := p.Tasks().Get(tid); err == nil {
			steps = t.Steps
		}
	}
	var out *Status
	err = mdfile.Update(Path(p, id, "STATUS.md"), func(old []byte) ([]byte, error) {
		st := &Status{SelfPercent: -1}
		if old != nil {
			if parsed, err := ParseStatus(old); err == nil {
				st = parsed
			}
		}
		if fn != nil {
			if err := fn(st); err != nil {
				return nil, err
			}
		}
		before := *st
		now := time.Now().UTC().Truncate(time.Second)
		st.Derive(steps, now)
		if old == nil || !sameStatus(&before, st) || fn != nil {
			st.Updated = now
		}
		out = st
		return st.Render()
	})
	return out, err
}

// sameStatus reports whether deriving changed nothing worth a new
// timestamp.
func sameStatus(a, b *Status) bool {
	return a.Percent == b.Percent && a.PercentSource == b.PercentSource && a.Current == b.Current &&
		a.StepsDone == b.StepsDone && a.StepsTotal == b.StepsTotal && a.TodosDone == b.TodosDone && a.TodosTotal == b.TodosTotal
}

// SameTodos reports whether two todo lists read the same in STATUS.md.
func SameTodos(a, b []agent.Todo) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if strings.Join(strings.Fields(a[i].Text), " ") != strings.Join(strings.Fields(b[i].Text), " ") || norm(a[i].Status) != norm(b[i].Status) {
			return false
		}
	}
	return true
}

func norm(s agent.TodoStatus) agent.TodoStatus {
	if s != agent.TodoCompleted && s != agent.TodoInProgress {
		return agent.TodoPending
	}
	return s
}
