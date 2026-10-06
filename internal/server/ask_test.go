package server

import (
	"errors"
	"maps"
	"testing"
	"time"

	"github.com/theclifmeister/terminatr/internal/proto"
)

var menu = proto.Question{Tool: "AskUserQuestion", Questions: []proto.QuestionItem{
	{Question: "Which colour?", Header: "Colour", Options: []proto.QuestionOption{{Label: "Red"}, {Label: "Blue"}}},
	{Question: "Which fruits?", MultiSelect: true, Options: []proto.QuestionOption{{Label: "Apple"}, {Label: "Pear"}}},
}}

// TestAsk: a mod's open question shows on the session until it ends;
// answers fill it in question by question, and the last one goes back
// to the mod. Hanging up, a new ask, or the session's end takes it down.
func TestAsk(t *testing.T) {
	p := testPaths(t)
	startServer(t, p)
	c, err := Dial(p, proto.KindControl)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	var started proto.SessionStartResult
	call(t, c, proto.MethodSessionStart, proto.SessionStartParams{Argv: []string{"/bin/sh"}, Cwd: "/"}, &started)
	id := started.Session.ID

	question := func() *proto.Question {
		t.Helper()
		var res proto.SessionListResult
		call(t, c, proto.MethodSessionList, nil, &res)
		for _, s := range res.Sessions {
			if s.ID == id {
				return s.Question
			}
		}
		t.Fatalf("no session %s", id)
		return nil
	}
	waitQuestion := func(open bool) *proto.Question {
		t.Helper()
		for range 200 {
			if q := question(); (q != nil) == open {
				return q
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatalf("question open = %v never came", !open)
		return nil
	}
	answer := func(i int, a string) (proto.SessionAnswerResult, error) {
		var res proto.SessionAnswerResult
		err := c.Call(proto.MethodSessionAnswer, proto.SessionAnswerParams{ID: id, Index: i, Answer: a}, &res)
		return res, err
	}
	code := func(err error) string {
		var perr *proto.Error
		if errors.As(err, &perr) {
			return perr.Code
		}
		return ""
	}

	if _, err := answer(0, "Red"); code(err) != proto.ErrNoQuestion {
		t.Fatalf("answer without a question: %v", err)
	}
	if _, err := AskSession(p, id, proto.Question{}); code(err) != proto.ErrBadParams {
		t.Fatalf("ask with no questions: %v", err)
	}
	if _, err := AskSession(p, "s-999", menu); code(err) != proto.ErrUnknownSession {
		t.Fatalf("ask on an unknown session: %v", err)
	}

	type got struct {
		ans map[string]string
		err error
	}
	ask := func() <-chan got {
		ch := make(chan got, 1)
		go func() {
			a, err := AskSession(p, id, menu)
			ch <- got{a, err}
		}()
		return ch
	}

	// Answered question by question; the last answer goes to the mod.
	done := ask()
	q := waitQuestion(true)
	if len(q.Questions) != 2 || q.Questions[1].Options[1].Label != "Pear" || q.Since.IsZero() {
		t.Fatalf("open question %+v", q)
	}
	if _, err := answer(2, "Red"); code(err) != proto.ErrBadParams {
		t.Fatalf("answer to question 3 of 2: %v", err)
	}
	if res, err := answer(0, "Blue"); err != nil || res.Remaining != 1 {
		t.Fatalf("answer 1: %+v %v", res, err)
	}
	if q := question(); q == nil || !q.Questions[0].Answered || q.Questions[0].Answer != "Blue" || q.Questions[1].Answered {
		t.Fatalf("after answer 1: %+v", q)
	}
	if res, err := answer(1, "Apple, kiwi"); err != nil || res.Remaining != 0 {
		t.Fatalf("answer 2: %+v %v", res, err)
	}
	select {
	case g := <-done:
		want := map[string]string{"Which colour?": "Blue", "Which fruits?": "Apple, kiwi"}
		if g.err != nil || !maps.Equal(g.ans, want) {
			t.Fatalf("the mod got %v %v", g.ans, g.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the mod got no answers")
	}
	waitQuestion(false)

	// The mod hangs up (the user answered in the pane): the question goes.
	m, err := Dial(p, proto.KindControl)
	if err != nil {
		t.Fatal(err)
	}
	call(t, m, proto.MethodSessionAsk, proto.SessionAskParams{ID: id, Question: menu}, nil)
	waitQuestion(true)
	m.Close()
	waitQuestion(false)

	// A new ask closes the old one unanswered.
	first := ask()
	waitQuestion(true)
	second := ask()
	select {
	case g := <-first:
		if g.err != nil || g.ans != nil {
			t.Fatalf("replaced ask: %v %v", g.ans, g.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the replaced ask didn't end")
	}
	// The session ends: so does its question.
	waitQuestion(true)
	call(t, c, proto.MethodSessionStop, proto.SessionIDParams{ID: id}, nil)
	select {
	case g := <-second:
		if g.err != nil || g.ans != nil {
			t.Fatalf("ask on an ended session: %v %v", g.ans, g.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the ask outlived its session")
	}
}
