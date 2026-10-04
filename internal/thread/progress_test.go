package thread

import (
	"testing"
	"time"

	"github.com/theclifmeister/termalator/internal/agent"
)

// TestDeriveProgress: one rule for every view (docs/SPEC.md §7.3).
func TestDeriveProgress(t *testing.T) {
	cases := []struct {
		name string
		in   ProgressInput
		want string
		src  string
	}{
		{"nothing", ProgressInput{Self: -1}, "", ""},
		{"steps only", ProgressInput{StepsDone: 1, StepsTotal: 3, Self: -1}, "30% 1/3", "steps"},
		{"todos only", ProgressInput{TodosDone: 1, TodosTotal: 3, Self: -1}, "30% 1/3", "todos"},
		// The t-0024 case: (2 + 1/3) / 4 = 58 % → 55 %, counted in steps.
		{"steps and todos", ProgressInput{StepsDone: 2, StepsTotal: 4, TodosDone: 1, TodosTotal: 3, Self: -1}, "55% 2/4", "steps+todos"},
		{"all steps done caps at 95", ProgressInput{StepsDone: 2, StepsTotal: 2, TodosDone: 1, TodosTotal: 1, Self: -1}, "95% 2/2", "steps+todos"},
		{"self fallback", ProgressInput{Self: 42}, "40%", "self"},
		{"steps beat self", ProgressInput{StepsTotal: 2, Self: 80}, "0% 0/2", "steps"},
		{"done", ProgressInput{StepsDone: 1, StepsTotal: 2, Self: -1, Done: true}, "100% 1/2", "done"},
	}
	for _, c := range cases {
		pr := DeriveProgress(c.in)
		if pr.String() != c.want || pr.Source != c.src {
			t.Errorf("%s: got %q %q, want %q %q", c.name, pr, pr.Source, c.want, c.src)
		}
	}
}

// TestStatusProgress: what STATUS.md stores reads back as what was
// derived, for each source.
func TestStatusProgress(t *testing.T) {
	now := time.Now()
	cases := []struct {
		name  string
		steps int // of which the first is done
		todos []agent.Todo
		self  int
		want  string
	}{
		{"steps only", 3, nil, -1, "30% 1/3"},
		{"todos only", 0, todos(C, I, P), -1, "30% 1/3"},
		{"both", 4, todos(C, I, P), -1, "30% 1/4"},
		{"self fallback", 0, nil, 42, "40%"},
		{"nothing", 0, nil, -1, ""},
	}
	for _, c := range cases {
		var bs []bool
		for i := range c.steps {
			bs = append(bs, i == 0)
		}
		st := &Status{Todos: c.todos, SelfPercent: c.self, SelfAt: now}
		st.Derive(steps(bs...), now)
		if got := st.Progress().String(); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
	if pr := (*Status)(nil).Progress(); pr.Percent != -1 || pr.String() != "" {
		t.Errorf("nil status: %+v", pr)
	}
}

// TestSessionProgress: a session that is no thread counts its todos and
// reaches 100 % with all of them done.
func TestSessionProgress(t *testing.T) {
	for _, c := range []struct {
		done, total int
		want        string
	}{{0, 0, ""}, {1, 3, "30% 1/3"}, {2, 5, "40% 2/5"}, {3, 3, "100% 3/3"}} {
		if got := SessionProgress(c.done, c.total).String(); got != c.want {
			t.Errorf("%d/%d: got %q, want %q", c.done, c.total, got, c.want)
		}
	}
}
