package proto

import "time"

// Open questions (docs/SPEC.md §3.3, Ask): an agent's mod that sees a
// question menu open (Claude's AskUserQuestion) calls session.ask with
// it. The server answers at once and keeps the question on the session
// (SessionInfo.Question) until the connection ends; when every question
// has an answer through session.answer it sends one ask.answered line
// and hangs up. The mod answers the menu with it; when the user answers
// in the pane first, the mod hangs up and the question goes.
const (
	// MethodSessionAsk opens a question on a session (SessionAskParams)
	// and answers an empty result; one AskEvent line follows.
	MethodSessionAsk = "session.ask"
	// MethodSessionAnswer answers one question of a session's open one
	// (SessionAnswerParams) and returns SessionAnswerResult.
	MethodSessionAnswer = "session.answer"
	// EventAskAnswered carries the answers, by question text.
	EventAskAnswered = "ask.answered"
	// EventAskClosed ends an ask without answers: another ask replaced it,
	// or the session ended.
	EventAskClosed = "ask.closed"
)

// ErrNoQuestion: the session has no open question (no mod, or the menu
// is gone).
const ErrNoQuestion = "no-question"

// Question is an open question menu: one or more questions, each
// answered on its own.
type Question struct {
	// Tool is the agent's tool that opened the menu (AskUserQuestion).
	Tool      string         `json:"tool,omitempty"`
	Questions []QuestionItem `json:"questions"`
	// Since is when the menu opened (set by the server).
	Since time.Time `json:"since,omitzero"`
}

// QuestionItem is one question of a menu. Answered says whether
// session.answer already gave it its answer (Answer).
type QuestionItem struct {
	Question    string           `json:"question"`
	Header      string           `json:"header,omitempty"`
	MultiSelect bool             `json:"multiSelect,omitempty"`
	Options     []QuestionOption `json:"options"`
	Answered    bool             `json:"answered,omitempty"`
	Answer      string           `json:"answer,omitempty"`
}

// QuestionOption is one option of a question.
type QuestionOption struct {
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
}

// SessionAskParams are the params of session.ask.
type SessionAskParams struct {
	ID       string   `json:"id"`
	Question Question `json:"question"`
}

// SessionAnswerParams are the params of session.answer: question Index
// (from 0) of the session's open question gets Answer, an option's
// label, labels joined with ", " for a multi-select, or free text.
type SessionAnswerParams struct {
	ID     string `json:"id"`
	Index  int    `json:"index"`
	Answer string `json:"answer"`
}

// SessionAnswerResult says how many questions still wait for an answer;
// at 0 the answers went to the mod.
type SessionAnswerResult struct {
	Remaining int `json:"remaining"`
}

// AskEvent is the line that ends a session.ask.
type AskEvent struct {
	Event   string            `json:"event"` // EventAskAnswered or EventAskClosed
	Answers map[string]string `json:"answers,omitempty"`
}
