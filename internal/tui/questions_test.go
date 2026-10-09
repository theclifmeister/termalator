package tui

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/theclifmeister/terminatr/internal/proto"
)

// TestQuestionsRow: a coordinator's open questions (tm ask) are a row in
// NEEDS YOU; enter on it has the coordinator open them, the footer says
// so, and the sidebar hints at the project.
func TestQuestionsRow(t *testing.T) {
	d := testData()
	d.Projects[1].Questions = 2
	src := &fakeSource{data: d}
	m := newDash(DashOptions{Source: src, Width: 120, Height: 30, State: DashState{Current: "beta"}})
	m.setData(src.data)
	if out := screen(m); !strings.Contains(out, "2 questions waiting") || !strings.Contains(out, "enter answers them") {
		t.Fatalf("no questions row:\n%s", out)
	}
	m.sel = "nq:beta"
	if foot := m.footKeys(); !strings.Contains(foot, "enter answer") {
		t.Errorf("footer %q", foot)
	}
	run(m, press(m, "enter"))
	if len(src.answered) != 1 || src.answered[0] != "beta" || m.result.Attach != "" {
		t.Fatalf("answered %v attach %q", src.answered, m.result.Attach)
	}
	if !strings.Contains(m.msg, "asked beta's coordinator to open its 2 questions") {
		t.Errorf("msg %q", m.msg)
	}
	tree := buildTree(src.data.Projects, src.data.Sessions, treeIn{})
	for _, r := range tree {
		if r.kind == treeProject && r.slug == "beta" && !r.hint {
			t.Error("the sidebar doesn't hint at beta's questions")
		}
	}
	// None open: no row.
	d.Projects[1].Questions = 0
	m.setData(d)
	if out := screen(m); strings.Contains(out, "questions waiting") {
		t.Fatalf("row without questions:\n%s", out)
	}
}

// TestCoordQuestions: the coordinator's info panel leads NEEDS YOU with
// its open questions; a click on the line, or a, has it open them.
func TestCoordQuestions(t *testing.T) {
	w := &proto.ProjectWatch{Project: "demo", Questions: 1}
	lines, hits := coordLines(w, 50, time.Now())
	text := ansi.Strip(strings.Join(lines, "\n"))
	if strings.Contains(text, "Nothing waits for you.") || !strings.Contains(text, "NEEDS YOU") ||
		!strings.Contains(text, "1 question waiting · a or click answers") || !strings.Contains(text, "1 needs you") {
		t.Fatalf("panel:\n%s", text)
	}
	found := false
	for i, l := range lines {
		if strings.Contains(ansi.Strip(l), "question waiting") {
			found = hits[i].kind == hitQuestions
		}
	}
	if !found {
		t.Error("a click on the questions line doesn't open them")
	}
	if got := questionsFlash(proto.QuestionsOpenResult{Open: 1}, nil); !strings.Contains(got, "asked the coordinator") {
		t.Errorf("flash %q", got)
	}
	if got := questionsFlash(proto.QuestionsOpenResult{}, &proto.Error{Code: proto.ErrRefused, Message: "project demo has no coordinator running; open it first"}); got != "not asked: project demo has no coordinator running; open it first" {
		t.Errorf("flash %q", got)
	}
	if got := questionsFlash(proto.QuestionsOpenResult{}, errors.New("no server")); got != "not asked: no server" {
		t.Errorf("flash %q", got)
	}
}
