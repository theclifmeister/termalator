package server

// Open questions (session.ask / session.answer, docs/SPEC.md §3.3, Ask):
// a session's mod sends the question menu it sees open and waits on the
// connection; `tm thread answer` fills in its questions one by one, and
// the last answer goes back to the mod, which answers the menu with it.
// The question lives as long as the mod's connection: the mod hangs up
// when the user answers in the pane, and the question goes with it.

import (
	"bufio"
	"io"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/theclifmeister/terminatr/internal/proto"
)

// asks holds each session's open question.
type asks struct {
	mu   sync.Mutex
	open map[string]*openAsk
}

type openAsk struct {
	q proto.Question
	// answered gets the answers once every question has one; closed
	// when another ask replaces this one.
	answered chan map[string]string
	replaced chan struct{}
}

// of is a copy of session id's open question, nil for none.
func (a *asks) of(id string) *proto.Question {
	a.mu.Lock()
	defer a.mu.Unlock()
	o := a.open[id]
	if o == nil {
		return nil
	}
	q := o.q
	q.Questions = append([]proto.QuestionItem(nil), o.q.Questions...)
	return &q
}

// put opens q on session id, replacing (and closing) any open one.
func (a *asks) put(id string, q proto.Question) *openAsk {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.open == nil {
		a.open = map[string]*openAsk{}
	}
	if old := a.open[id]; old != nil {
		close(old.replaced)
	}
	o := &openAsk{q: q, answered: make(chan map[string]string, 1), replaced: make(chan struct{})}
	a.open[id] = o
	return o
}

// drop removes o if it is still session id's open question.
func (a *asks) drop(id string, o *openAsk) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.open[id] == o {
		delete(a.open, id)
	}
}

// answer gives question i of session id's open question its answer.
// When none is left unanswered, the answers go to the mod and the
// question closes.
func (a *asks) answer(id string, i int, ans string) (proto.SessionAnswerResult, *proto.Error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	o := a.open[id]
	if o == nil {
		return proto.SessionAnswerResult{}, proto.Errorf(proto.ErrNoQuestion, "session %s has no open question", id)
	}
	if i < 0 || i >= len(o.q.Questions) {
		return proto.SessionAnswerResult{}, proto.Errorf(proto.ErrBadParams, "the open question has %d question(s), not %d", len(o.q.Questions), i+1)
	}
	if strings.TrimSpace(ans) == "" {
		return proto.SessionAnswerResult{}, proto.Errorf(proto.ErrBadParams, "empty answer")
	}
	o.q.Questions[i].Answered, o.q.Questions[i].Answer = true, ans
	left := 0
	for _, it := range o.q.Questions {
		if !it.Answered {
			left++
		}
	}
	if left == 0 {
		out := map[string]string{}
		for _, it := range o.q.Questions {
			out[it.Question] = it.Answer
		}
		o.answered <- out
		delete(a.open, id)
	}
	return proto.SessionAnswerResult{Remaining: left}, nil
}

// validQuestion checks what a mod sent: 1-4 questions, each with its
// text and options with labels.
func validQuestion(q proto.Question) *proto.Error {
	if len(q.Questions) == 0 || len(q.Questions) > 4 {
		return proto.Errorf(proto.ErrBadParams, "a question menu has 1-4 questions, not %d", len(q.Questions))
	}
	for _, it := range q.Questions {
		if strings.TrimSpace(it.Question) == "" {
			return proto.Errorf(proto.ErrBadParams, "a question without its text")
		}
		for _, o := range it.Options {
			if strings.TrimSpace(o.Label) == "" {
				return proto.Errorf(proto.ErrBadParams, "an option without a label")
			}
		}
	}
	return nil
}

// serveAsk answers session.ask, keeps the question open while the client
// stays, and sends the answers (ask.answered) when they are all in, or
// ask.closed when another ask replaced it or the session ended.
func (s *Server) serveAsk(c net.Conn, br *bufio.Reader, req proto.Request) {
	var p proto.SessionAskParams
	resp := proto.Response{ID: req.ID}
	if perr := decodeParams(req.Params, &p); perr != nil {
		resp.Error = perr
		writeJSONLine(c, resp)
		return
	}
	sess, perr := s.session(p.ID)
	if perr == nil {
		perr = validQuestion(p.Question)
	}
	if perr != nil {
		resp.Error = perr
		writeJSONLine(c, resp)
		return
	}
	q := p.Question
	q.Since = time.Now().UTC()
	for i := range q.Questions {
		q.Questions[i].Answered, q.Questions[i].Answer = false, ""
	}
	o := s.asks.put(p.ID, q)
	defer s.asks.drop(p.ID, o)
	s.watch.wake()
	defer s.watch.wake()
	resp.Result = []byte("{}")
	if err := writeJSONLine(c, resp); err != nil {
		return
	}
	// The client sends nothing more; a read returning is it leaving.
	gone := make(chan struct{})
	go func() {
		io.Copy(io.Discard, br)
		close(gone)
	}()
	select {
	case <-gone:
	case ans := <-o.answered:
		writeJSONLine(c, proto.AskEvent{Event: proto.EventAskAnswered, Answers: ans})
	case <-o.replaced:
		writeJSONLine(c, proto.AskEvent{Event: proto.EventAskClosed})
	case <-sess.Done():
		writeJSONLine(c, proto.AskEvent{Event: proto.EventAskClosed})
	}
}
