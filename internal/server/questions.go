package server

// Opening the coordinator's questions (questions.open, docs/SPEC.md
// §7.5, Questions): the band in the coordinator's pane, the dashboard's
// key and `tm ask open` all come here. The server queues one fixed-word
// prompt to the project's coordinator, which then puts the questions
// stored with `tm ask` to the user in its question dialog. The prompt
// waits in the queue while the coordinator works, and is dropped at
// delivery when the questions were answered meanwhile (in chat, say), so
// a second click asks nothing twice.

import (
	"fmt"

	"github.com/theclifmeister/terminatr/internal/agent"
	"github.com/theclifmeister/terminatr/internal/project"
	"github.com/theclifmeister/terminatr/internal/proto"
	"github.com/theclifmeister/terminatr/internal/session"
)

// QuestionsPrompt is the prompt that has a coordinator open its n open
// questions: fixed words only.
func QuestionsPrompt(n int) string {
	return fmt.Sprintf("[tm] answer questions: the user wants to answer your %d open question(s) now. Put them to the user with AskUserQuestion as `tm ask list --json` holds them (up to %d per dialog, each with its header, options and recommended option first), then `tm ask done <id>` for each one answered, and update CONTEXT.md's ## Needs you.",
		n, project.MaxDialogQuestions)
}

func (s *Server) openQuestions(p proto.QuestionsOpenParams) (any, *proto.Error) {
	pr, err := project.Open(p.Project)
	if err != nil {
		return nil, proto.Errorf(proto.ErrBadParams, "%v", err)
	}
	qs, err := pr.Questions()
	if err != nil {
		return nil, proto.Errorf(proto.ErrInternal, "%v", err)
	}
	if len(qs) == 0 {
		return nil, proto.Errorf(proto.ErrRefused, "project %s has no open questions", p.Project)
	}
	id := ""
	for _, info := range s.list().Sessions {
		if info.Project == p.Project && info.Role == proto.RoleCoordinator && info.State != string(agent.StateExited) {
			id = info.ID
			break
		}
	}
	if id == "" {
		return nil, proto.Errorf(proto.ErrRefused, "project %s has no coordinator running; open it first", p.Project)
	}
	refresh := func() (string, bool) {
		qs, err := pr.Questions()
		if err != nil || len(qs) == 0 {
			return "", false
		}
		return QuestionsPrompt(len(qs)), true
	}
	res, perr := s.promptWith(proto.SessionPromptParams{ID: id, Text: QuestionsPrompt(len(qs))},
		session.PromptOptions{Channel: true, Refresh: refresh})
	if perr != nil {
		return nil, perr
	}
	return proto.QuestionsOpenResult{Session: id, Via: res.(proto.SessionPromptResult).Via, Open: len(qs)}, nil
}
