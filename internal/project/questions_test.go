package project

import (
	"strings"
	"testing"
)

func TestQuestions(t *testing.T) {
	setup(t)
	p, err := New(Options{Slug: "demo-app"})
	if err != nil {
		t.Fatal(err)
	}
	if qs, err := p.Questions(); err != nil || qs != nil {
		t.Fatalf("none: %v %v", qs, err)
	}
	q, err := p.AddQuestion(Question{Task: "T3", Question: " Pick one? ", Recommended: "B",
		Options: []QuestionOption{{Label: "A"}, {Label: " B ", Description: "the safe one"}}})
	if err != nil || q.ID != "Q1" || q.Header != "T3" || q.Question != "Pick one?" || q.Asked.IsZero() {
		t.Fatalf("add %+v %v", q, err)
	}
	if got := QuestionLine(q); got != "Q1 T3 [T3] Pick one? Options: A; B (recommended) — the safe one" {
		t.Fatalf("line %q", got)
	}
	// The context shows them, so a cleared coordinator still knows.
	secs, err := p.Context(Ticked{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(RenderContext(secs), "Open questions (tm ask)") {
		t.Fatal("context lacks the open questions")
	}
	if _, err := p.DoneQuestion("Q1"); err != nil {
		t.Fatal(err)
	}
	secs, _ = p.Context(Ticked{})
	if strings.Contains(RenderContext(secs), "Open questions (tm ask)") {
		t.Fatal("context shows answered questions")
	}
	for _, c := range []struct {
		name string
		q    Question
	}{
		{"no options", Question{Question: "x?"}},
		{"five options", Question{Question: "x?", Options: []QuestionOption{{Label: "a"}, {Label: "b"}, {Label: "c"}, {Label: "d"}, {Label: "e"}}}},
		{"same label twice", Question{Question: "x?", Options: []QuestionOption{{Label: "a"}, {Label: "a"}}}},
		{"empty", Question{Options: []QuestionOption{{Label: "a"}, {Label: "b"}}}},
	} {
		if _, err := p.AddQuestion(c.q); err == nil || !strings.Contains(err.Error(), "invalid-question") && !strings.Contains(err.(*Error).Code, "invalid-question") {
			t.Errorf("%s: %v", c.name, err)
		}
	}
	if QuestionsWaiting(0) != "" || QuestionsWaiting(1) != "1 question waiting" || QuestionsWaiting(3) != "3 questions waiting" {
		t.Fatal("QuestionsWaiting")
	}
}
