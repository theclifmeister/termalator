package cli

import (
	"errors"
	"strings"
	"testing"

	"github.com/theclifmeister/terminatr/internal/proto"
	"github.com/theclifmeister/terminatr/internal/tasks"
)

// TestMenuAnswer: tm thread answer's flags against a question of an
// open menu, as the agent joins them.
func TestMenuAnswer(t *testing.T) {
	single := proto.QuestionItem{Question: "Which colour?", Options: []proto.QuestionOption{{Label: "Red"}, {Label: "Blue"}}}
	multi := proto.QuestionItem{Question: "Which fruits?", MultiSelect: true, Options: []proto.QuestionOption{{Label: "Apple"}, {Label: "Pear"}, {Label: "Plum"}}}
	for _, c := range []struct {
		name string
		it   proto.QuestionItem
		a    answerArgs
		want string // the answer, or the error's code
	}{
		{"by number", single, answerArgs{choices: []int{2}}, "Blue"},
		{"by label, any case", single, answerArgs{options: []string{" blue "}}, "Blue"},
		{"own words", single, answerArgs{text: "teal", withText: true}, "teal"},
		{"own words by number", single, answerArgs{choices: []int{3}, text: "teal", withText: true}, "teal"},
		{"own words missing", single, answerArgs{choices: []int{3}}, "needs-text"},
		{"empty text", single, answerArgs{withText: true}, "usage"},
		{"no such number", single, answerArgs{choices: []int{4}}, "no-option"},
		{"no such label", single, answerArgs{options: []string{"Green"}}, "no-option"},
		{"two on a single-select", single, answerArgs{choices: []int{1, 2}}, "single-select"},
		{"option and words on a single-select", single, answerArgs{choices: []int{1}, text: "x", withText: true}, "not-text-option"},
		{"multi by numbers", multi, answerArgs{choices: []int{1, 3}}, "Apple, Plum"},
		{"multi by both, once each", multi, answerArgs{choices: []int{1}, options: []string{"apple", "Pear"}}, "Apple, Pear"},
		{"multi with words", multi, answerArgs{choices: []int{2}, text: "kiwi", withText: true}, "Pear, kiwi"},
	} {
		got, err := menuAnswer(c.it, c.a)
		var terr *tasks.Error
		var uerr *usageError
		switch {
		case errors.As(err, &terr):
			got = terr.Code
		case errors.As(err, &uerr):
			got = "usage"
		case err != nil:
			t.Fatalf("%s: %v", c.name, err)
		}
		if got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

// TestQuestionLines: the menu as tm thread show and tm context print it,
// with the numbers --choice takes; an answered question shows its answer.
func TestQuestionLines(t *testing.T) {
	q := &proto.Question{Questions: []proto.QuestionItem{
		{Question: "Which colour?", Header: "Colour", Answered: true, Answer: "Blue", Options: []proto.QuestionOption{{Label: "Red"}, {Label: "Blue"}}},
		{Question: "Which fruits?", MultiSelect: true, Options: []proto.QuestionOption{{Label: "Apple", Description: "crisp"}, {Label: "Pear"}}},
	}}
	got := strings.Join(questionLines(q), "\n")
	want := `1. [Colour] Which colour? → answered: "Blue"
2. Which fruits? (several: --choice 1,3)
   1. Apple — crisp
   2. Pear
   3. (the user's own words: --text)`
	if got != want {
		t.Fatalf("got\n%s\nwant\n%s", got, want)
	}
}
