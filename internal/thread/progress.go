package thread

import "fmt"

// Progress is a thread's or session's progress as every view shows it
// (docs/SPEC.md §7.3): the derived percent and the done/total of the
// list that sets the scale.
type Progress struct {
	Percent int    // -1: none
	Source  string // steps+todos, steps, todos, self, done or ""
	Done    int
	Total   int // 0: no counts to show
}

// ProgressInput is what progress is derived from.
type ProgressInput struct {
	StepsDone, StepsTotal int
	TodosDone, TodosTotal int
	Self                  int  // the fresh self-reported percent, -1 for none
	Done                  bool // tm done was called
}

// DeriveProgress is the single rule of docs/SPEC.md §7.3. Steps set the
// scale and todos fill in the current step; done/total counts steps
// when there are any, else todos.
func DeriveProgress(in ProgressInput) Progress {
	S, s, T, c := in.StepsTotal, in.StepsDone, in.TodosTotal, in.TodosDone
	pr := Progress{Percent: -1}
	var pct float64
	switch {
	case S > 0 && T > 0:
		// An agent that keeps one list for the whole job can push this
		// past the checked steps by at most one step's worth.
		pct = (float64(s) + float64(c)/float64(T)) / float64(S) * 100
		if s == S {
			pct = 100
		}
		pr.Source, pr.Done, pr.Total = "steps+todos", s, S
	case S > 0:
		pct = float64(s) / float64(S) * 100
		pr.Source, pr.Done, pr.Total = "steps", s, S
	case T > 0:
		pct = float64(c) / float64(T) * 100
		pr.Source, pr.Done, pr.Total = "todos", c, T
	case in.Self >= 0:
		pct = float64(in.Self)
		pr.Source = "self"
	}
	if pr.Source != "" {
		pr.Percent = min(max(int(pct)/5*5, 0), 95)
	}
	if in.Done {
		pr.Percent, pr.Source = 100, "done"
	}
	return pr
}

// SessionProgress is the progress of a session that is not a thread: its
// todos alone. With no tm done, it reaches 100 % once every todo is done.
func SessionProgress(todosDone, todosTotal int) Progress {
	return DeriveProgress(ProgressInput{TodosDone: todosDone, TodosTotal: todosTotal, Self: -1,
		Done: todosTotal > 0 && todosDone == todosTotal})
}

// Progress is the stored progress of STATUS.md, as derived by Derive.
func (st *Status) Progress() Progress {
	if st == nil || st.PercentSource == "" {
		return Progress{Percent: -1}
	}
	pr := Progress{Percent: st.Percent, Source: st.PercentSource}
	switch {
	case st.StepsTotal > 0:
		pr.Done, pr.Total = st.StepsDone, st.StepsTotal
	case st.TodosTotal > 0:
		pr.Done, pr.Total = st.TodosDone, st.TodosTotal
	}
	return pr
}

// String is "55% 2/4", "40%" without counts, or "" without progress.
func (pr Progress) String() string {
	if pr.Percent < 0 {
		return ""
	}
	if pr.Total > 0 {
		return fmt.Sprintf("%d%% %d/%d", pr.Percent, pr.Done, pr.Total)
	}
	return fmt.Sprintf("%d%%", pr.Percent)
}
