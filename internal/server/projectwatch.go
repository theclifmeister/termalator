package server

// Watching a project (project.watch, docs/SPEC.md §3.3, Watch): what the
// coordinator's /tm pane shows, sent again whenever it changes. It
// streams like session.watch, and wakes and re-reads the same way: at
// once on what the server changes itself, every watchPoll for the rest
// (the board, the inbox, thread files, the ticker's PR polls).

import (
	"bufio"
	"bytes"
	"cmp"
	"encoding/json"
	"io"
	"net"
	"slices"
	"time"

	"github.com/theclifmeister/terminatr/internal/project"
	"github.com/theclifmeister/terminatr/internal/proto"
	"github.com/theclifmeister/terminatr/internal/tasks"
	"github.com/theclifmeister/terminatr/internal/thread"
	"github.com/theclifmeister/terminatr/internal/ticker"
)

// projectWatchState is project slug's state now, from its files, the
// ticker's state file and the server's sessions.
func (s *Server) projectWatchState(slug string) (proto.ProjectWatch, error) {
	p, err := project.Open(slug)
	if err != nil {
		return proto.ProjectWatch{}, err
	}
	return projectWatchOf(p, s.list().Sessions, ticker.StatePath(s.opts.Paths.Sessions)), nil
}

// projectWatchOf is project p's ProjectWatch, with sessions the server's
// and tickerState the ticker's state file.
func projectWatchOf(p *project.Project, sessions []proto.SessionInfo, tickerState string) proto.ProjectWatch {
	w := proto.ProjectWatch{Project: p.Slug, NeedsYou: []proto.WatchNeed{}, Inbox: []proto.WatchItem{},
		Threads: []proto.WatchThread{}, Ready: []proto.WatchTodo{}}
	items, _ := p.Inbox()
	for _, it := range items {
		w.Inbox = append(w.Inbox, proto.WatchItem{ID: it.ID, Kind: it.Kind, Subject: it.Subject, Summary: it.Summary, NeedsUser: it.NeedsUser})
	}
	b, _ := p.Tasks().Load()
	byThread := map[string]proto.SessionInfo{}
	for _, info := range sessions {
		if info.Project == p.Slug && info.Role == proto.RoleThread && info.Thread != "" {
			byThread[info.Thread] = info
		}
	}
	prs := ticker.PRs(tickerState, p.Slug)

	// The threads, and per task the latest one working on it.
	byTask := map[string]int{} // index in w.Threads
	prOf := map[string]ticker.PR{}
	recs, _ := thread.List(p)
	for _, r := range recs {
		if r.State == thread.Resolved {
			continue
		}
		wt := proto.WatchThread{ID: r.ID, Title: r.Title, Reports: r.Reports, Done: r.Done}
		if info, ok := byThread[r.ID]; ok {
			wt.Session, wt.State, wt.Reason = info.ID, info.State, info.Reason
			wt.NeedsYou = openQuestion(info.Question)
		}
		st, _ := thread.ReadStatus(p, r.ID)
		if st != nil && st.NeedsYou != "" {
			wt.NeedsYou = st.NeedsYou
		}
		if b != nil && r.TaskID() > 0 {
			if t := b.Find(r.TaskID()); t != nil {
				wt.Task = &proto.WatchTask{ID: t.Ref(), Title: t.Title, Status: string(t.Status),
					StepsDone: t.StepsDone(), StepsTotal: len(t.Steps)}
				if st != nil {
					wt.Task.Current = st.Current
				}
			}
		}
		pr := prs[r.ID]
		wt.PR, wt.PRURL, wt.PRBad = prWords(pr), pr.URL, isPRBad(pr)
		if wt.PRURL == "" {
			if rep, _ := thread.ReadReport(p, r.ID); rep != nil {
				wt.PRURL = rep.PR
			}
		}
		prOf[r.ID] = pr
		if wt.Task != nil {
			byTask[wt.Task.ID] = len(w.Threads)
		} else if wt.NeedsYou != "" {
			w.NeedsYou = append(w.NeedsYou, proto.WatchNeed{Why: proto.WhyQuestion, Title: r.Title, Thread: r.ID, Question: wt.NeedsYou})
		}
		w.Threads = append(w.Threads, wt)
	}

	if b != nil {
		for _, t := range b.Tasks {
			ref := t.Ref()
			asked := project.TaskAsked(items, ref)
			switch t.Status {
			case tasks.Open, tasks.Ready:
				w.Ready = append(w.Ready, proto.WatchTodo{Task: ref, Title: t.Title, Status: string(t.Status), Asked: asked})
				continue
			case tasks.Done:
				continue
			}
			n := proto.WatchNeed{Task: ref, Title: t.Title, Status: string(t.Status), Asked: asked}
			if i, ok := byTask[ref]; ok {
				wt := w.Threads[i]
				pr := prOf[wt.ID]
				n.Thread, n.Question = wt.ID, wt.NeedsYou
				n.PR, n.PRURL, n.PRNumber = wt.PR, wt.PRURL, pr.Number
				n.Mergeable = isMergeable(pr)
				switch {
				case t.Status == tasks.Review:
					n.Why = proto.WhyReview
				case n.Question != "":
					n.Why = proto.WhyQuestion
				case pr.State == "OPEN" && (pr.Checks == "fail" || pr.Mergeable == "CONFLICTING"):
					n.Why = proto.WhyCI
				}
			}
			if n.Why == "" {
				switch t.Status {
				case tasks.Review:
					n.Why = proto.WhyReview
				case tasks.Blocked:
					n.Why = proto.WhyBlocked
				default:
					continue
				}
			}
			w.NeedsYou = append(w.NeedsYou, n)
		}
	}
	slices.SortStableFunc(w.NeedsYou, func(a, b proto.WatchNeed) int {
		if c := cmp.Compare(whyRank(a.Why), whyRank(b.Why)); c != 0 {
			return c
		}
		// A PR ready to merge goes before one that isn't.
		return cmp.Compare(boolRank(!a.Mergeable), boolRank(!b.Mergeable))
	})
	return w
}

func whyRank(why string) int {
	return slices.Index([]string{proto.WhyReview, proto.WhyQuestion, proto.WhyCI, proto.WhyBlocked}, why)
}

func boolRank(b bool) int {
	if b {
		return 1
	}
	return 0
}

// openQuestion is the first unanswered question of the menu open in the
// agent, "" for none.
func openQuestion(q *proto.Question) string {
	if q == nil {
		return ""
	}
	for _, it := range q.Questions {
		if !it.Answered {
			return it.Question
		}
	}
	return ""
}

// prWords is pr in the ticker's words, with its base when it is behind
// it, as session.watch says it; "" for no PR.
func prWords(pr ticker.PR) string {
	s := pr.Summary()
	if s != "" && pr.State == "OPEN" && pr.MergeState == "BEHIND" {
		s += ", behind " + cmp.Or(pr.Base, "its base")
	}
	return s
}

// isPRBad reports an open PR the thread has to act on.
func isPRBad(pr ticker.PR) bool {
	return pr.State == "OPEN" && (pr.Checks == "fail" || pr.Mergeable == "CONFLICTING" ||
		pr.Review == "CHANGES_REQUESTED" || pr.MergeState == "BEHIND")
}

// isMergeable reports an open PR with its checks passed (or none), no
// conflicts and no changes requested: the coordinator may merge it once
// the user says so.
func isMergeable(pr ticker.PR) bool {
	return pr.State == "OPEN" && (pr.Checks == "pass" || pr.Checks == "") &&
		pr.Mergeable != "CONFLICTING" && pr.Review != "CHANGES_REQUESTED"
}

// serveProjectWatch answers project.watch and then streams the
// project's state: a project.changed line for every new one, until the
// client hangs up or the server stops.
func (s *Server) serveProjectWatch(c net.Conn, br *bufio.Reader, req proto.Request) {
	var p proto.ProjectWatchParams
	resp := proto.Response{ID: req.ID}
	if perr := decodeParams(req.Params, &p); perr != nil {
		resp.Error = perr
		writeJSONLine(c, resp)
		return
	}
	wake := s.watch.add()
	defer s.watch.remove(wake)
	w, err := s.projectWatchState(p.Project)
	if err != nil {
		resp.Error = proto.Errorf(proto.ErrBadParams, "%v", err)
		writeJSONLine(c, resp)
		return
	}
	resp.Result, _ = json.Marshal(w)
	if err := writeJSONLine(c, resp); err != nil {
		return
	}
	last := resp.Result
	gone := make(chan struct{})
	go func() {
		io.Copy(io.Discard, br)
		close(gone)
	}()
	poll := envDuration(envWatchPoll)
	if poll <= 0 {
		poll = defaultWatchPoll
	}
	t := time.NewTicker(poll)
	defer t.Stop()
	for {
		select {
		case <-gone:
			return
		case <-wake:
		case <-t.C:
		}
		if s.isStopping() {
			return
		}
		next, err := s.projectWatchState(p.Project)
		if err != nil {
			continue // a project being renamed or moved: try again
		}
		b, _ := json.Marshal(next)
		if bytes.Equal(b, last) {
			continue
		}
		last = b
		if err := writeJSONLine(c, proto.ProjectWatchEvent{Event: proto.EventProjectChanged, Watch: next}); err != nil {
			return
		}
	}
}

// ProjectWatchStream is a project watch (project.watch).
type ProjectWatchStream struct{ c *Client }

// WatchProject watches project slug and returns the stream and the
// project's state now.
func WatchProject(p Paths, slug string) (*ProjectWatchStream, proto.ProjectWatch, error) {
	c, err := Dial(p, proto.KindControl)
	if err != nil {
		return nil, proto.ProjectWatch{}, err
	}
	var w proto.ProjectWatch
	if err := c.Call(proto.MethodProjectWatch, proto.ProjectWatchParams{Project: slug}, &w); err != nil {
		c.Close()
		return nil, proto.ProjectWatch{}, err
	}
	return &ProjectWatchStream{c: c}, w, nil
}

// Next blocks until the project's next state.
func (s *ProjectWatchStream) Next() (proto.ProjectWatch, error) {
	for {
		var ev proto.ProjectWatchEvent
		if err := readJSONLine(s.c.br, &ev); err != nil {
			return proto.ProjectWatch{}, err
		}
		if ev.Event == proto.EventProjectChanged {
			return ev.Watch, nil
		}
	}
}

// Close ends the watch.
func (s *ProjectWatchStream) Close() error { return s.c.Close() }
