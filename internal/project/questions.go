package project

// Open questions (docs/SPEC.md §5.1, questions.json): what the
// coordinator asked the user and is waiting on, kept in a form the
// question dialog (Claude Code's AskUserQuestion) takes as it is, so a
// band in the coordinator's pane, or a key in the dashboard, can have
// it open them. CONTEXT.md's "## Needs you" stays the coordinator's
// human-readable copy; this file is the structured one, written only
// through `tm ask`.

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/theclifmeister/terminatr/internal/mdfile"
)

// The question dialog's limits: 2 to 4 options a question (it adds
// "Other" itself), a header chip of 12 characters, and at most
// MaxDialogQuestions questions at once.
const (
	MinQuestionOptions = 2
	MaxQuestionOptions = 4
	MaxQuestionHeader  = 12
	MaxDialogQuestions = 4
	maxQuestionText    = 1000
	maxOptionText      = 300
)

// questionsFile is the store, in the project folder.
const questionsFile = "questions.json"

// QuestionOption is one choice of a question.
type QuestionOption struct {
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
}

// Question is one open question to the user.
type Question struct {
	ID       string           `json:"id"`             // Q1, Q2, …
	Task     string           `json:"task,omitempty"` // T12, or none
	Header   string           `json:"header"`         // the dialog's chip
	Question string           `json:"question"`
	Options  []QuestionOption `json:"options"`
	// MultiSelect lets the user pick several options.
	MultiSelect bool `json:"multi_select,omitempty"`
	// Recommended is the label of the option the coordinator recommends;
	// the dialog lists it first, marked "(Recommended)".
	Recommended string    `json:"recommended,omitempty"`
	Asked       time.Time `json:"asked"`
}

// questionStore is questions.json.
type questionStore struct {
	NextID int        `json:"next_id"`
	Open   []Question `json:"open"`
}

var questionRefRE = regexp.MustCompile(`^[Qq]([1-9][0-9]*)$`)
var taskRefRE = regexp.MustCompile(`^T[1-9][0-9]*$`)

// Questions are the open questions, oldest first.
func (p *Project) Questions() ([]Question, error) {
	data, err := os.ReadFile(p.Path(questionsFile))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	st, err := decodeQuestions(data)
	if err != nil {
		return nil, err
	}
	return st.Open, nil
}

func decodeQuestions(data []byte) (questionStore, error) {
	var st questionStore
	if len(data) == 0 {
		return st, nil
	}
	if err := json.Unmarshal(data, &st); err != nil {
		return st, fmt.Errorf("%s: %w", questionsFile, err)
	}
	return st, nil
}

// updateQuestions is a locked read-modify-write of the store.
func (p *Project) updateQuestions(fn func(st *questionStore) error) error {
	return mdfile.Update(p.Path(questionsFile), func(old []byte) ([]byte, error) {
		st, err := decodeQuestions(old)
		if err != nil {
			return nil, err
		}
		if err := fn(&st); err != nil {
			return nil, err
		}
		if st.Open == nil {
			st.Open = []Question{}
		}
		b, err := json.MarshalIndent(st, "", "  ")
		return append(b, '\n'), err
	})
}

// Check tidies q (trims, fills the header) and says why the dialog
// couldn't show it.
func (q *Question) Check() error {
	q.Task = strings.TrimSpace(q.Task)
	q.Header = strings.TrimSpace(q.Header)
	q.Question = strings.TrimSpace(q.Question)
	q.Recommended = strings.TrimSpace(q.Recommended)
	if q.Task != "" && !taskRefRE.MatchString(q.Task) {
		return refuse("invalid-question", "task %q is not a task id like T12", q.Task)
	}
	if q.Question == "" {
		return refuse("invalid-question", "the question is empty")
	}
	if utf8.RuneCountInString(q.Question) > maxQuestionText {
		return refuse("invalid-question", "the question is over %d characters", maxQuestionText)
	}
	if q.Header == "" {
		q.Header = q.Task
	}
	if q.Header == "" {
		q.Header = "Question"
	}
	if utf8.RuneCountInString(q.Header) > MaxQuestionHeader {
		return refuse("invalid-question", "header %q is over %d characters", q.Header, MaxQuestionHeader)
	}
	if n := len(q.Options); n < MinQuestionOptions || n > MaxQuestionOptions {
		return refuse("invalid-question", "a question takes %d to %d options (the dialog adds Other itself), not %d", MinQuestionOptions, MaxQuestionOptions, n)
	}
	seen := map[string]bool{}
	for i := range q.Options {
		o := &q.Options[i]
		o.Label, o.Description = strings.TrimSpace(o.Label), strings.TrimSpace(o.Description)
		if o.Label == "" {
			return refuse("invalid-question", "option %d has no label", i+1)
		}
		if utf8.RuneCountInString(o.Label)+utf8.RuneCountInString(o.Description) > maxOptionText {
			return refuse("invalid-question", "option %q is over %d characters", o.Label, maxOptionText)
		}
		if seen[o.Label] {
			return refuse("invalid-question", "two options are labelled %q", o.Label)
		}
		seen[o.Label] = true
	}
	if q.Recommended != "" && !seen[q.Recommended] {
		return refuse("invalid-question", "the recommended option %q is not one of the options", q.Recommended)
	}
	return nil
}

// AddQuestion checks q and stores it under the next id, which it
// returns. Asking the same question again (same task and text) gives the
// open one back, updated.
func (p *Project) AddQuestion(q Question) (Question, error) {
	if err := q.Check(); err != nil {
		return Question{}, err
	}
	err := p.updateQuestions(func(st *questionStore) error {
		for i, o := range st.Open {
			if o.Task == q.Task && o.Question == q.Question {
				q.ID, q.Asked = o.ID, o.Asked
				st.Open[i] = q
				return nil
			}
		}
		st.NextID = max(st.NextID, 1)
		q.ID = fmt.Sprintf("Q%d", st.NextID)
		st.NextID++
		q.Asked = now().UTC().Truncate(time.Second)
		st.Open = append(st.Open, q)
		return nil
	})
	if err != nil {
		return Question{}, err
	}
	return q, nil
}

// DoneQuestion closes the open question ref (Q3 or q3): answered, or no
// longer asked. Closing one that isn't open refuses with
// unknown-question.
func (p *Project) DoneQuestion(ref string) (Question, error) {
	m := questionRefRE.FindStringSubmatch(strings.TrimSpace(ref))
	if m == nil {
		return Question{}, refuse("invalid-question", "%q is not a question id like Q3", ref)
	}
	id := "Q" + m[1]
	var done Question
	err := p.updateQuestions(func(st *questionStore) error {
		for i, o := range st.Open {
			if o.ID == id {
				done = o
				st.Open = append(st.Open[:i], st.Open[i+1:]...)
				return nil
			}
		}
		return refuse("unknown-question", "no open question %s", id)
	})
	return done, err
}

// QuestionsWaiting is "2 questions waiting", or "" for none.
func QuestionsWaiting(n int) string {
	switch {
	case n <= 0:
		return ""
	case n == 1:
		return "1 question waiting"
	}
	return fmt.Sprintf("%d questions waiting", n)
}

// QuestionLine is q on one line, as tm ask list and tm context print it:
// "Q3 T12 [Repo] Which repo? Options: a (recommended); b — why".
func QuestionLine(q Question) string {
	var b strings.Builder
	b.WriteString(q.ID)
	if q.Task != "" {
		b.WriteString(" " + q.Task)
	}
	fmt.Fprintf(&b, " [%s] %s", q.Header, strings.Join(strings.Fields(q.Question), " "))
	if q.MultiSelect {
		b.WriteString(" (multi-select)")
	}
	b.WriteString(" Options:")
	for i, o := range q.Options {
		if i > 0 {
			b.WriteString(";")
		}
		b.WriteString(" " + o.Label)
		if o.Label == q.Recommended {
			b.WriteString(" (recommended)")
		}
		if o.Description != "" {
			b.WriteString(" — " + strings.Join(strings.Fields(o.Description), " "))
		}
	}
	return b.String()
}
