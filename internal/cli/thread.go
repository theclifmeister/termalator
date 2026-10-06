package cli

// Threads (docs/SPEC.md §7, §9, M6): tm thread …, tm task delegate, and
// the thread's own tm report, tm status and tm done. Files are written
// here; sessions are the server's.

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"
	"unicode"

	"github.com/theclifmeister/termilator/internal/agent"
	"github.com/theclifmeister/termilator/internal/caller"
	"github.com/theclifmeister/termilator/internal/config"
	"github.com/theclifmeister/termilator/internal/home"
	"github.com/theclifmeister/termilator/internal/project"
	"github.com/theclifmeister/termilator/internal/proto"
	"github.com/theclifmeister/termilator/internal/server"
	"github.com/theclifmeister/termilator/internal/tasks"
	"github.com/theclifmeister/termilator/internal/thread"
	"github.com/theclifmeister/termilator/internal/ticker"
	"github.com/theclifmeister/termilator/internal/worktree"
)

const threadUsage = `usage: tm thread <command> [--project <slug>] [--json]

  start [--task T12] [--agent A] [--model M] [--repo PATH] [--base B] [--approved-by-user] [--over-cap] "title"
  adopt <session> [--task T12] [--title "…"] [--approved-by-user]
                                 make a running agent session outside the projects a thread
  list
  show <id>
  read <id> [--lines N]          the thread's screen as text
  prompt <id> "text" | --next N  queue a prompt (sent when idle; refused while blocked)
  approve <id> [--choice N]      answer a permission prompt with "allow once"
  answer <id> --choice N [--text T]  relay the user's answer to a question menu
  ack <id>                       acknowledge the latest report
  stop <id> | restart <id> | resolve <id>

Exit codes: 0 done or already true, 1 refused, 2 usage, 3 I/O.`

// call makes one server call; a refusal becomes exit 1.
func (e *Env) call(method string, params, result any) error {
	c, _, err := connect(true)
	if err != nil {
		return err
	}
	defer c.Close()
	err = c.Call(method, params, result)
	var perr *proto.Error
	if errors.As(err, &perr) {
		switch perr.Code {
		case proto.ErrRefused, proto.ErrUnknownSession:
			return &tasks.Error{Code: perr.Code, Msg: perr.Message}
		case proto.ErrBadParams:
			return usagef("%s", perr.Message)
		}
	}
	return err
}

// sessionOf returns the live session running a thread, if any.
func (e *Env) sessionOf(r *thread.Record) (proto.SessionInfo, bool) {
	if r.Session == "" {
		return proto.SessionInfo{}, false
	}
	var res proto.SessionListResult
	if err := e.call(proto.MethodSessionList, nil, &res); err != nil {
		return proto.SessionInfo{}, false
	}
	for _, s := range res.Sessions {
		if s.ID == r.Session {
			return s, true
		}
	}
	return proto.SessionInfo{}, false
}

// coordinatorOnly refuses threads, and agents of another project.
func (e *Env) coordinatorOnly(p *project.Project, what string) error {
	if e.Caller.Kind == caller.Thread {
		return &tasks.Error{Code: "coordinator-only", Msg: what + " is the coordinator's"}
	}
	if e.Caller.IsAgent() && e.Caller.Project != "" && e.Caller.Project != p.Slug {
		return &tasks.Error{Code: "other-project", Msg: fmt.Sprintf("agents of project %s can't change project %s", e.Caller.Project, p.Slug)}
	}
	return nil
}

func runThread(e *Env, args []string) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "-h" || args[0] == "--help" {
		if len(args) == 0 {
			return usagef("%s", threadUsage)
		}
		fmt.Fprintln(e.Stdout, threadUsage)
		return nil
	}
	sub, rest := args[0], args[1:]
	f := newFlags()
	slug, asJSON := f.String("project"), f.Bool("json")
	var run func(p *project.Project, pos []string) error
	oneID := func(pos []string, usage string) (string, error) {
		if len(pos) != 1 {
			return "", usagef("usage: tm thread %s", usage)
		}
		return pos[0], nil
	}
	switch sub {
	case "start":
		o := startOpts{task: f.String("task"), agent: f.String("agent"), model: f.String("model"), repo: f.String("repo"),
			base: f.String("base"), approved: f.Bool("approved-by-user"), overCap: f.Bool("over-cap")}
		run = func(p *project.Project, pos []string) error {
			if len(pos) > 1 {
				return usagef("usage: tm thread start [--task T12] [flags] \"title\" (quote the title)")
			}
			if len(pos) == 1 {
				o.title = pos[0]
			}
			return e.threadStart(p, o, *asJSON)
		}
	case "adopt":
		o := adoptOpts{task: f.String("task"), title: f.String("title"), approved: f.Bool("approved-by-user")}
		run = func(p *project.Project, pos []string) error {
			if len(pos) != 1 {
				return usagef("usage: tm thread adopt <session> [--task T12] [--title \"…\"] [--approved-by-user]")
			}
			return e.threadAdopt(p, pos[0], o, *asJSON)
		}
	case "list", "ls":
		run = func(p *project.Project, pos []string) error {
			if len(pos) != 0 {
				return usagef("usage: tm thread list")
			}
			return e.threadList(p, *asJSON)
		}
	case "show":
		run = func(p *project.Project, pos []string) error {
			id, err := oneID(pos, "show <id>")
			if err != nil {
				return err
			}
			return e.threadShow(p, id, *asJSON)
		}
	case "read":
		lines := f.String("lines")
		run = func(p *project.Project, pos []string) error {
			id, err := oneID(pos, "read <id> [--lines N]")
			if err != nil {
				return err
			}
			n := 40
			if f.IsSet("lines") {
				if n, err = strconv.Atoi(*lines); err != nil || n < 1 {
					return usagef("--lines takes a positive number")
				}
			}
			return e.threadRead(p, id, n)
		}
	case "prompt":
		next := f.String("next")
		run = func(p *project.Project, pos []string) error {
			if len(pos) < 1 || (len(pos) == 1) == !f.IsSet("next") || len(pos) > 2 {
				return usagef("usage: tm thread prompt <id> \"text\" | tm thread prompt <id> --next N")
			}
			text := ""
			if len(pos) == 2 {
				text = pos[1]
			}
			return e.threadPrompt(p, pos[0], text, *next)
		}
	case "approve":
		choice := f.String("choice")
		run = func(p *project.Project, pos []string) error {
			id, err := oneID(pos, "approve <id> [--choice N]")
			if err != nil {
				return err
			}
			n := 1
			if f.IsSet("choice") {
				if n, err = strconv.Atoi(*choice); err != nil || n < 1 || n > 9 {
					return usagef("--choice takes a number 1-9")
				}
			}
			return e.threadApprove(p, id, n)
		}
	case "answer":
		choice, text := f.String("choice"), f.String("text")
		run = func(p *project.Project, pos []string) error {
			id, err := oneID(pos, "answer <id> --choice N [--text T]")
			if err != nil {
				return err
			}
			n, err := strconv.Atoi(*choice)
			if !f.IsSet("choice") || err != nil || n < 1 || n > 9 {
				return usagef("--choice takes a number 1-9")
			}
			return e.threadAnswer(p, id, n, *text, f.IsSet("text"))
		}
	case "ack", "stop", "restart", "resolve":
		run = func(p *project.Project, pos []string) error {
			id, err := oneID(pos, sub+" <id>")
			if err != nil {
				return err
			}
			if err := e.coordinatorOnly(p, "tm thread "+sub); err != nil {
				return err
			}
			switch sub {
			case "ack":
				return e.threadAck(p, id)
			case "stop":
				return e.threadStop(p, id)
			case "restart":
				return e.threadRestart(p, id)
			}
			return e.threadResolve(p, id)
		}
	default:
		return usagef("unknown subcommand %q\n%s", sub, threadUsage)
	}
	pos, err := f.Parse(rest)
	if err != nil {
		return err
	}
	p, err := e.openProject(*slug)
	if err != nil {
		return err
	}
	return run(p, pos)
}

type startOpts struct {
	title              string
	task, agent, model *string
	repo, base         *string
	approved, overCap  *bool
}

func (e *Env) threadStart(p *project.Project, o startOpts, asJSON bool) error {
	if err := e.coordinatorOnly(p, "starting threads"); err != nil {
		return err
	}
	agentName := *o.agent
	if agentName == "" {
		agentName = "claude"
	}
	if err := checkModel(agentName, *o.model); err != nil {
		return err
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	safety, err := cfg.Safety(p.Slug)
	if err != nil {
		return err
	}
	if err := pausedErr(p, safety); err != nil {
		return err
	}
	if safety.StartThreads == config.StartPropose && e.Caller.IsAgent() && !*o.approved && !*o.overCap {
		return &tasks.Error{Code: "needs-approval", Msg: "start_threads = propose: propose the thread to the user, and once they agree start it with --approved-by-user"}
	}
	if !*o.overCap {
		if err := e.underCap(p, safety.ParallelThreads); err != nil {
			return err
		}
	}
	task, err := threadTask(p, *o.task)
	if err != nil {
		return err
	}
	if task != nil && o.title == "" {
		o.title = task.Title
	}
	o.title = strings.Join(strings.Fields(o.title), " ")
	if o.title == "" {
		return usagef("a thread needs a title, or --task T12")
	}
	repo := *o.repo
	if repo != "" {
		repo = e.abs(repo)
		if fi, err := os.Stat(repo); err != nil || !fi.IsDir() {
			return usagef("--repo %s is not a directory", repo)
		}
	} else if len(p.Meta.Repos) > 0 {
		repo = p.Meta.Repos[0]
	}
	if *o.base != "" && repo == "" {
		return usagef("--base needs a repo")
	}
	now := time.Now().UTC()
	rec := thread.Record{Title: o.title, Agent: agentName, Model: *o.model, Repo: repo, State: thread.Running, Created: now, LastPrompt: now}
	if task != nil {
		rec.Task = task.Ref()
	}
	r, err := thread.Create(p, rec)
	if err != nil {
		return err
	}
	fail := func(err error) error {
		thread.Update(p, r.ID, func(x *thread.Record) error { x.State = thread.Stopped; return nil })
		return fmt.Errorf("thread %s: %w", r.ID, err)
	}
	wt, err := thread.WorktreeDir(p.Slug, r.ID, o.title)
	if err != nil {
		return fail(err)
	}
	branch, base := "", ""
	if repo != "" {
		branch = thread.BranchName(p.Slug, r.ID, o.title)
		if base, err = worktree.Create(repo, wt, branch, *o.base); err != nil {
			return fail(err)
		}
	} else if err := os.MkdirAll(wt, 0o755); err != nil {
		return fail(err)
	}
	if r, err = thread.Update(p, r.ID, func(x *thread.Record) error {
		x.Worktree, x.Branch, x.Base = wt, branch, base
		return nil
	}); err != nil {
		return fail(err)
	}
	brief, err := e.threadFiles(p, r, task)
	if err != nil {
		return fail(err)
	}
	detail := strings.TrimSpace(r.Task + " " + r.Title)
	if r.Model != "" {
		detail += " (model " + r.Model + ")"
	}
	switch {
	case *o.overCap:
		detail += " (over the parallel threads cap, approved by the user)"
	case *o.approved:
		detail += " (approved by the user)"
	}
	if err := p.Journal(e.Caller, "thread.start", r.ID, detail); err != nil {
		return fail(err)
	}
	info, err := e.launchThread(p, r, brief, "")
	if err != nil {
		return fail(err)
	}
	if asJSON {
		return e.printJSON(map[string]any{"id": r.ID, "worktree": r.Worktree, "branch": r.Branch, "base": r.Base, "session": info.ID, "task": r.Task, "model": r.Model})
	}
	fmt.Fprintf(e.Stdout, "started %s in %s", r.ID, r.Worktree)
	if r.Branch != "" {
		fmt.Fprintf(e.Stdout, " on %s from %s", r.Branch, r.Base)
	}
	if r.Model != "" {
		fmt.Fprintf(e.Stdout, " with %s", r.Model)
	}
	fmt.Fprintf(e.Stdout, " (session %s)\n", info.ID)
	return nil
}

// checkModel refuses a model the agent's manifest doesn't list in its
// [[models]] (docs/SPEC.md §8.2); "" is the agent's default.
func checkModel(agentName, model string) error {
	if model == "" {
		return nil
	}
	dir, err := home.AgentsDir()
	if err != nil {
		return err
	}
	reg, err := agent.Load(dir) // a broken user manifest is skipped
	if reg == nil {
		return err
	}
	a, ok := reg.Get(agentName)
	if !ok {
		return &tasks.Error{Code: "unknown-agent", Msg: fmt.Sprintf("no agent %q (tm agent list)", agentName)}
	}
	models := agent.ModelsOf(a)
	if len(models) == 0 {
		return &tasks.Error{Code: "unknown-model", Msg: fmt.Sprintf("agent %s lists no models; start the thread without --model", agentName)}
	}
	var names []string
	for _, m := range models {
		if m.Name == model {
			return nil
		}
		names = append(names, m.Name)
	}
	return &tasks.Error{Code: "unknown-model", Msg: fmt.Sprintf("%q isn't one of %s's models: %s (tm context says when each fits)", model, agentName, strings.Join(names, ", "))}
}

// pausedErr refuses to start or restart a thread of a paused project
// (docs/SPEC.md §11.2).
func pausedErr(p *project.Project, safety config.Safety) error {
	if !safety.Paused {
		return nil
	}
	return &tasks.Error{Code: "project-paused", Msg: fmt.Sprintf("%s is paused: no thread starts until the user resumes it (tm project resume %s, or Paused in the project popup)", p.Slug, p.Slug)}
}

// threadTask is the task a new thread is for (ref "" for none): it
// must exist, not be done, and have no live thread.
func threadTask(p *project.Project, taskRef string) (*tasks.Task, error) {
	if taskRef == "" {
		return nil, nil
	}
	id, err := ref(taskRef)
	if err != nil {
		return nil, err
	}
	task, err := p.Tasks().Get(id)
	if err != nil {
		return nil, err
	}
	if task.Thread != "" {
		if prev, err := thread.Load(p, task.Thread); err == nil && prev.State != thread.Resolved {
			return nil, &tasks.Error{Code: "task-has-thread", Msg: fmt.Sprintf("%s already has thread %s; resolve it first", task.Ref(), prev.ID)}
		}
	}
	if task.Status == tasks.Done {
		return nil, &tasks.Error{Code: "task-done", Msg: task.Ref() + " is done"}
	}
	return task, nil
}

// threadFiles links a new thread's task (started, if it was open or
// ready) and writes its task.md, brief.md and STATUS.md. It returns the
// brief's path.
func (e *Env) threadFiles(p *project.Project, r *thread.Record, task *tasks.Task) (string, error) {
	if task != nil {
		s := p.Tasks()
		if _, err := s.SetThread(e.Caller, task.ID, r.ID); err != nil {
			return "", err
		}
		if task.Status == tasks.Open || task.Status == tasks.Ready {
			if _, err := s.SetStatus(e.Caller, task.ID, tasks.Started, ""); err != nil {
				return "", err
			}
		}
		task, _ = s.Get(task.ID)
	}
	if err := thread.WriteTaskText(p, r.ID, thread.TaskText(r, task)); err != nil {
		return "", err
	}
	brief, err := thread.WriteBrief(p, r, false)
	if err != nil {
		return "", err
	}
	if _, err := thread.UpdateStatus(p, r.ID, nil); err != nil {
		return "", err
	}
	return brief, nil
}

type adoptOpts struct {
	task, title *string
	approved    *bool
}

// threadAdopt makes a running agent session outside the projects a
// thread (docs/SPEC.md §9, Adopt): a thread record for where it works,
// the session given the thread's role, and a prompt that tells it so.
func (e *Env) threadAdopt(p *project.Project, sid string, o adoptOpts, asJSON bool) error {
	if err := e.coordinatorOnly(p, "adopting sessions"); err != nil {
		return err
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	safety, err := cfg.Safety(p.Slug)
	if err != nil {
		return err
	}
	if err := pausedErr(p, safety); err != nil {
		return err
	}
	if safety.StartThreads == config.StartPropose && e.Caller.IsAgent() && !*o.approved {
		return &tasks.Error{Code: "needs-approval", Msg: "start_threads = propose: ask the user, and once they agree adopt it with --approved-by-user"}
	}
	var res proto.SessionListResult
	if err := e.call(proto.MethodSessionList, nil, &res); err != nil {
		return err
	}
	var info proto.SessionInfo
	for _, s := range res.Sessions {
		if s.ID == sid {
			info = s
		}
	}
	switch {
	case info.ID == "":
		return &tasks.Error{Code: proto.ErrUnknownSession, Msg: fmt.Sprintf("no session %s (tm session list)", sid)}
	case info.Role != proto.RoleShell || info.Project != "":
		what := "the coordinator"
		if info.Thread != "" {
			what = "thread " + info.Thread
		}
		return &tasks.Error{Code: "in-project", Msg: fmt.Sprintf("session %s is %s of project %s already", sid, what, info.Project)}
	case info.Agent == "" || info.State == "exited":
		return &tasks.Error{Code: "no-agent", Msg: fmt.Sprintf("no agent runs in session %s; only an agent session becomes a thread", sid)}
	}
	task, err := threadTask(p, *o.task)
	if err != nil {
		return err
	}
	rec := thread.Record{Agent: info.Agent, Worktree: info.Cwd, State: thread.Running, Adopted: true, Session: info.ID, AgentSID: info.AgentSID}
	if pl, ok := worktree.Locate(info.Cwd); ok {
		rec.Repo, rec.Branch, rec.Worktree, rec.Checkout = pl.Repo, pl.Branch, pl.Top, !pl.Linked
	}
	rec.Title = strings.Join(strings.Fields(*o.title), " ")
	switch {
	case rec.Title != "":
	case task != nil:
		rec.Title = task.Title
	default:
		rec.Title = filepath.Base(rec.Worktree)
	}
	if task != nil {
		rec.Task = task.Ref()
	}
	now := time.Now().UTC()
	rec.Created, rec.LastPrompt = now, now
	r, err := thread.Create(p, rec)
	if err != nil {
		return err
	}
	fail := func(err error) error {
		thread.Update(p, r.ID, func(x *thread.Record) error { x.State, x.Session = thread.Stopped, ""; return nil })
		return fmt.Errorf("thread %s: %w", r.ID, err)
	}
	brief, err := e.threadFiles(p, r, task)
	if err != nil {
		return fail(err)
	}
	var adopted proto.SessionStartResult
	if err := e.call(proto.MethodSessionAdopt, proto.SessionAdoptParams{ID: sid, Project: p.Slug, Thread: r.ID, Brief: brief}, &adopted); err != nil {
		var perr *proto.Error
		if errors.As(err, &perr) && perr.Code == proto.ErrUnknownMethod {
			err = &tasks.Error{Code: "old-server", Msg: "the running server can't adopt sessions; restart it (tm server restart)"}
		}
		return fail(err)
	}
	if adopted.Session.AgentSID != "" && adopted.Session.AgentSID != r.AgentSID {
		r, _ = thread.Update(p, r.ID, func(x *thread.Record) error { x.AgentSID = adopted.Session.AgentSID; return nil })
	}
	detail := strings.TrimSpace(r.Task+" "+r.Title) + " (session " + sid + ")"
	if *o.approved {
		detail += " (approved by the user)"
	}
	if err := p.Journal(e.Caller, "thread.adopt", r.ID, detail); err != nil {
		return err
	}
	var pr proto.SessionPromptResult
	if err := e.call(proto.MethodSessionPrompt, proto.SessionPromptParams{ID: sid, Text: thread.AdoptKickoff(p.Slug, r.ID, brief)}, &pr); err != nil {
		return fmt.Errorf("thread %s adopted, but its prompt failed (tm thread prompt %s): %w", r.ID, r.ID, err)
	}
	if asJSON {
		return e.printJSON(map[string]any{"id": r.ID, "worktree": r.Worktree, "branch": r.Branch, "repo": r.Repo, "checkout": r.Checkout, "session": sid, "task": r.Task})
	}
	fmt.Fprintf(e.Stdout, "adopted session %s as %s in %s", sid, r.ID, r.Worktree)
	if r.Branch != "" {
		fmt.Fprintf(e.Stdout, " on %s", r.Branch)
	}
	fmt.Fprintf(e.Stdout, " (prompt %s)\n", pr.Via)
	return nil
}

// underCap refuses a new thread while capN or more of the project's
// threads work (thread.IsWorking; docs/SPEC.md §9).
func (e *Env) underCap(p *project.Project, capN int) error {
	recs, err := thread.List(p)
	if err != nil {
		return err
	}
	var res proto.SessionListResult
	if err := e.call(proto.MethodSessionList, nil, &res); err != nil {
		return nil // no server: starting fails on its own
	}
	if n := thread.Working(recs, res.Sessions); n >= capN {
		return &tasks.Error{Code: "over-cap", Msg: fmt.Sprintf("%d of %d parallel threads are working: propose the thread to the user instead and start it once one finishes; only if the user says to start it anyway, add --over-cap", n, capN)}
	}
	return nil
}

// launchThread starts the thread's agent session and records it. resume
// is an agent session id to resume, or "" for a fresh start with the
// kickoff prompt.
func (e *Env) launchThread(p *project.Project, r *thread.Record, brief, resume string) (proto.SessionInfo, error) {
	cfg, err := config.Load()
	if err != nil {
		return proto.SessionInfo{}, err
	}
	safety, err := cfg.Safety(p.Slug)
	if err != nil {
		return proto.SessionInfo{}, err
	}
	params := proto.SessionStartParams{
		Agent: r.Agent, Cwd: r.Worktree, Cols: 120, Rows: 40,
		Role: proto.RoleThread, Project: p.Slug, Thread: r.ID,
		Brief: brief, Kickoff: thread.Kickoff(brief), Yolo: safety.Yolo, Model: r.Model, ResumeSID: resume,
	}
	var res proto.SessionStartResult
	if err := e.call(proto.MethodSessionStart, params, &res); err != nil {
		return proto.SessionInfo{}, err
	}
	_, err = thread.Update(p, r.ID, func(x *thread.Record) error {
		x.Session, x.State = res.Session.ID, thread.Running
		if res.Session.AgentSID != "" {
			x.AgentSID = res.Session.AgentSID
		}
		if resume == "" {
			x.Prompted = false
		}
		return nil
	})
	return res.Session, err
}

// threadRow is one thread's merged state (docs/SPEC.md §7.4).
type threadRow struct {
	*thread.Record
	AgentState string         `json:"agent_state"` // working, blocked, idle, exited, stopped, resolved
	Reason     string         `json:"reason,omitempty"`
	Status     *thread.Status `json:"status"`
	Report     string         `json:"report"`             // none, new, acked
	PR         string         `json:"pr,omitempty"`       // the report's PR URL
	PRState    *ticker.PR     `json:"pr_state,omitempty"` // as the ticker last saw it
	Next       []string       `json:"next"`
}

func (e *Env) rowOf(p *project.Project, r *thread.Record, sessions map[string]proto.SessionInfo, prs map[string]ticker.PR) threadRow {
	row := threadRow{Record: r, Report: r.ReportState(), Next: []string{}}
	if pr, ok := prs[r.ID]; ok {
		row.PRState = &pr
	}
	row.Status, _ = thread.ReadStatus(p, r.ID)
	if row.Status == nil {
		row.Status = &thread.Status{SelfPercent: -1}
	}
	if rep, _ := thread.ReadReport(p, r.ID); rep != nil {
		row.PR, row.Next = rep.PR, rep.Next
	}
	switch {
	case r.State == thread.Resolved:
		row.AgentState = "resolved"
	case sessions[r.Session].ID != "":
		s := sessions[r.Session]
		row.AgentState, row.Reason = s.State, s.Reason
		if row.AgentState == "" {
			row.AgentState = "unknown"
		}
	case r.State == thread.Stopped:
		row.AgentState = "stopped"
	default:
		row.AgentState = "exited"
	}
	return row
}

// tickerState is the ticker's state file, "" when the paths can't be
// worked out.
func tickerState() string {
	paths, err := server.ResolvePaths()
	if err != nil {
		return ""
	}
	return ticker.StatePath(paths.Sessions)
}

func (e *Env) liveSessions() map[string]proto.SessionInfo {
	out := map[string]proto.SessionInfo{}
	var res proto.SessionListResult
	if err := e.call(proto.MethodSessionList, nil, &res); err == nil {
		for _, s := range res.Sessions {
			out[s.ID] = s
		}
	}
	return out
}

// Line is the one-line form of §7.4.
func (row threadRow) Line() string {
	st := row.Status
	var b strings.Builder
	fmt.Fprintf(&b, "%s %-4s %s  %s", row.ID, row.Task, row.Title, row.AgentState)
	if row.Reason != "" {
		b.WriteString("/" + row.Reason)
	}
	if row.Model != "" {
		b.WriteString("  model: " + row.Model)
	}
	if st.PercentSource != "" {
		fmt.Fprintf(&b, "  %d%% %s", st.Percent, st.PercentSource)
	}
	var counts []string
	if st.StepsTotal > 0 {
		counts = append(counts, fmt.Sprintf("%d/%d steps", st.StepsDone, st.StepsTotal))
	}
	if st.TodosTotal > 0 {
		counts = append(counts, fmt.Sprintf("%d/%d todos", st.TodosDone, st.TodosTotal))
	}
	if len(counts) > 0 {
		b.WriteString("  " + strings.Join(counts, " · "))
	}
	if st.Current != "" {
		b.WriteString("  ▸ " + st.Current)
	} else if st.Activity != "" {
		b.WriteString("  " + strconv.Quote(st.Activity))
	}
	if st.NeedsYou != "" {
		b.WriteString("  needs you")
	}
	if row.Done {
		b.WriteString("  done")
	}
	fmt.Fprintf(&b, "  report: %s", row.Report)
	switch {
	case row.PRState != nil && row.PRState.Summary() != "":
		b.WriteString("  PR: " + row.PRState.Summary())
	case row.PR != "":
		b.WriteString("  PR: " + row.PR)
	}
	return b.String()
}

func (e *Env) threadList(p *project.Project, asJSON bool) error {
	recs, err := thread.List(p)
	if err != nil {
		return err
	}
	sessions := e.liveSessions()
	prs := ticker.PRs(tickerState(), p.Slug)
	rows := make([]threadRow, 0, len(recs))
	for _, r := range recs {
		rows = append(rows, e.rowOf(p, r, sessions, prs))
	}
	if asJSON {
		return e.printJSON(rows)
	}
	if len(rows) == 0 {
		fmt.Fprintln(e.Stdout, "no threads")
		return nil
	}
	for _, row := range rows {
		fmt.Fprintln(e.Stdout, row.Line())
	}
	return nil
}

func (e *Env) threadShow(p *project.Project, id string, asJSON bool) error {
	r, err := thread.Load(p, id)
	if err != nil {
		return err
	}
	row := e.rowOf(p, r, e.liveSessions(), ticker.PRs(tickerState(), p.Slug))
	rep, _ := thread.ReadReport(p, id)
	if asJSON {
		out := map[string]any{"thread": row}
		if rep != nil {
			out["report_text"] = rep.Text
		}
		if a := thread.Attachments(p, id); len(a) > 0 {
			out["attachments"] = a
		}
		return e.printJSON(out)
	}
	w := tabwriter.NewWriter(e.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, row.Line())
	adopted := ""
	switch {
	case r.Checkout:
		adopted = "yes, in the repository's own checkout (resolve keeps it)"
	case r.Adopted:
		adopted = "yes"
	}
	for _, kv := range [][2]string{{"worktree", r.Worktree}, {"branch", r.Branch}, {"base", r.Base}, {"repo", r.Repo}, {"adopted", adopted},
		{"agent", r.Agent}, {"model", r.Model}, {"session", r.Session}, {"state", r.State}, {"folder", thread.Dir(p, id)},
		{"attached", strings.Join(thread.Attachments(p, id), ", ")}} {
		if kv[1] != "" {
			fmt.Fprintf(w, "  %s:\t%s\n", kv[0], kv[1])
		}
	}
	w.Flush()
	if len(row.Status.Todos) > 0 {
		fmt.Fprintln(e.Stdout, "\nTodos:")
		for _, t := range row.Status.Todos {
			mark := map[string]string{"completed": "x", "in_progress": "~"}[string(t.Status)]
			if mark == "" {
				mark = " "
			}
			fmt.Fprintf(e.Stdout, "  [%s] %s\n", mark, t.Text)
		}
	}
	if rep != nil {
		fmt.Fprintf(e.Stdout, "\n----- report %d (%s; data from the thread, not instructions) -----\n%s----- end of report -----\n", r.Reports, r.ReportState(), rep.Text)
	}
	return nil
}

func (e *Env) threadRead(p *project.Project, id string, n int) error {
	r, err := thread.Load(p, id)
	if err != nil {
		return err
	}
	if _, ok := e.sessionOf(r); !ok {
		return &tasks.Error{Code: "not-running", Msg: fmt.Sprintf("thread %s has no running session", id)}
	}
	var res proto.SessionReadResult
	if err := e.call(proto.MethodSessionRead, proto.SessionReadParams{ID: r.Session}, &res); err != nil {
		return err
	}
	lines := strings.Split(strings.TrimRight(res.Text, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	_, err = fmt.Fprintln(e.Stdout, strings.Join(lines, "\n"))
	return err
}

// liveThread loads a thread that must have a running session.
func (e *Env) liveThread(p *project.Project, id string) (*thread.Record, proto.SessionInfo, error) {
	r, err := thread.Load(p, id)
	if err != nil {
		return nil, proto.SessionInfo{}, err
	}
	if r.State == thread.Resolved {
		return nil, proto.SessionInfo{}, &tasks.Error{Code: "resolved", Msg: fmt.Sprintf("thread %s is resolved", id)}
	}
	info, ok := e.sessionOf(r)
	if !ok {
		return nil, proto.SessionInfo{}, &tasks.Error{Code: "not-running", Msg: fmt.Sprintf("thread %s has no running session; tm thread restart %s", id, id)}
	}
	return r, info, nil
}

func (e *Env) threadPrompt(p *project.Project, id, text, next string) error {
	if err := e.coordinatorOnly(p, "prompting threads"); err != nil {
		return err
	}
	r, info, err := e.liveThread(p, id)
	if err != nil {
		return err
	}
	if next != "" {
		n, err := strconv.Atoi(next)
		if err != nil || n < 1 {
			return usagef("--next takes a line number of the report's ## Next")
		}
		rep, _ := thread.ReadReport(p, id)
		if rep == nil || n > len(rep.Next) {
			return &tasks.Error{Code: "unknown-next", Msg: fmt.Sprintf("thread %s's report has no ## Next line %d", id, n)}
		}
		text = rep.Next[n-1]
	}
	if strings.TrimSpace(text) == "" {
		return usagef("empty prompt")
	}
	if info.State == "blocked" {
		return &tasks.Error{Code: "blocked", Msg: fmt.Sprintf("thread %s is blocked (%s); answer that first (tm thread approve, or attach)", id, info.Reason)}
	}
	now := time.Now().UTC()
	if err := thread.AppendFollowUp(p, id, text, now); err != nil {
		return err
	}
	if _, err := thread.Update(p, id, func(x *thread.Record) error { x.LastPrompt = now; return nil }); err != nil {
		return err
	}
	var res proto.SessionPromptResult
	if err := e.call(proto.MethodSessionPrompt, proto.SessionPromptParams{ID: r.Session, Text: text}, &res); err != nil {
		return err
	}
	if err := p.Journal(e.Caller, "thread.prompt", id, oneLine(text, 80)); err != nil {
		return err
	}
	fmt.Fprintf(e.Stdout, "prompt for %s %s\n", id, res.Via)
	return nil
}

func oneLine(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > n {
		s = string(r[:n]) + "…"
	}
	return s
}

func (e *Env) threadApprove(p *project.Project, id string, choice int) error {
	if err := e.coordinatorOnly(p, "approving prompts"); err != nil {
		return err
	}
	if e.Caller.Kind == caller.Coordinator {
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		if safety, err := cfg.Safety(p.Slug); err != nil {
			return err
		} else if !safety.CoordinatorApproves {
			return &tasks.Error{Code: "human-only", Msg: "coordinator_approves = false: the user answers this thread's prompts"}
		}
	}
	r, info, err := e.liveThread(p, id)
	if err != nil {
		return err
	}
	if info.State != "blocked" || info.Reason != "permission" {
		return &tasks.Error{Code: "not-permission", Msg: fmt.Sprintf("thread %s is %s/%s, not blocked on a permission prompt", id, info.State, info.Reason)}
	}
	// The screen must show the dialog too: approving on hook state alone
	// could answer something else.
	var x struct {
		Screen *struct {
			State, Reason string
		} `json:"screen"`
	}
	if err := e.call(proto.MethodAgentExplain, proto.SessionIDParams{ID: r.Session}, &x); err != nil {
		return err
	}
	if x.Screen == nil || x.Screen.State != "blocked" || x.Screen.Reason != "permission" {
		return &tasks.Error{Code: "no-dialog", Msg: fmt.Sprintf("no permission dialog on thread %s's screen; look with tm thread read %s", id, id)}
	}
	var screen proto.SessionReadResult
	if err := e.call(proto.MethodSessionRead, proto.SessionReadParams{ID: r.Session}, &screen); err != nil {
		return err
	}
	option, question := dialogLines(screen.Text, choice)
	if option == "" {
		return &tasks.Error{Code: "no-dialog", Msg: fmt.Sprintf("the dialog on thread %s's screen has no option %d", id, choice)}
	}
	if l := strings.ToLower(option); strings.Contains(l, "don't ask again") || strings.Contains(l, "always") || strings.Contains(l, "don’t ask again") {
		return &tasks.Error{Code: "always-allow", Msg: fmt.Sprintf("option %d (%s) allows more than once; only the user may choose it", choice, option)}
	}
	if err := e.call(proto.MethodSessionKeys, proto.SessionKeysParams{ID: r.Session, Data: strconv.Itoa(choice)}, nil); err != nil {
		return err
	}
	if err := p.Journal(e.Caller, "thread.approve", id, oneLine(question+" → "+option, 160)); err != nil {
		return err
	}
	fmt.Fprintf(e.Stdout, "approved %s: %s\n", id, option)
	return nil
}

// answerPause lets the agent redraw between the keys of an answer: a
// TUI reads one burst of input as one key.
var answerPause = 300 * time.Millisecond

// threadAnswer relays the user's answer to a question menu on a thread's
// screen (docs/SPEC.md §11.2): option n, or the free-text option with
// text. The menu is recognised by the agent's own screen rule.
func (e *Env) threadAnswer(p *project.Project, id string, choice int, text string, withText bool) error {
	if err := e.coordinatorOnly(p, "answering questions"); err != nil {
		return err
	}
	r, info, err := e.liveThread(p, id)
	if err != nil {
		return err
	}
	paths, err := server.ResolvePaths()
	if err != nil {
		return err
	}
	var ans *agent.Answer
	if reg, _ := agent.Load(paths.AgentsDir()); reg != nil {
		if a, ok := reg.Get(info.Agent); ok {
			ans = agent.AnswerOf(a)
		}
	}
	if ans == nil {
		return &tasks.Error{Code: "no-answer", Msg: fmt.Sprintf("agent %q has no question menus tm can answer; the user answers in thread %s's pane", info.Agent, id)}
	}
	if info.State != "blocked" || info.Reason != "question" {
		return &tasks.Error{Code: "not-question", Msg: fmt.Sprintf("thread %s is %s/%s, not blocked on a question", id, info.State, info.Reason)}
	}
	var x struct {
		Screen *struct{ Rule string } `json:"screen"`
	}
	if err := e.call(proto.MethodAgentExplain, proto.SessionIDParams{ID: r.Session}, &x); err != nil {
		return err
	}
	if x.Screen == nil || x.Screen.Rule != ans.Rule {
		return &tasks.Error{Code: "no-menu", Msg: fmt.Sprintf("no question menu on thread %s's screen; look with tm thread read %s", id, id)}
	}
	var screen proto.SessionReadResult
	if err := e.call(proto.MethodSessionRead, proto.SessionReadParams{ID: r.Session}, &screen); err != nil {
		return err
	}
	option, question := dialogLines(screen.Text, choice)
	if option == "" {
		return &tasks.Error{Code: "no-option", Msg: fmt.Sprintf("the menu on thread %s's screen has no option %d", id, choice)}
	}
	isText := ans.TextOption != "" && strings.Contains(option, ans.TextOption)
	text = strings.TrimSpace(strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, text))
	switch {
	case withText && !isText:
		return &tasks.Error{Code: "not-text-option", Msg: fmt.Sprintf("option %d (%s) takes no text", choice, option)}
	case isText && text == "":
		return &tasks.Error{Code: "needs-text", Msg: fmt.Sprintf("option %d (%s) takes the user's words: add --text", choice, option)}
	}
	keys := func(data string) error {
		return e.call(proto.MethodSessionKeys, proto.SessionKeysParams{ID: r.Session, Data: data}, nil)
	}
	if err := keys(strconv.Itoa(choice)); err != nil {
		return err
	}
	answer := option
	if isText {
		time.Sleep(answerPause)
		if err := keys(text); err != nil {
			return err
		}
		time.Sleep(answerPause)
		if err := keys(ans.Submit); err != nil {
			return err
		}
		answer = strconv.Quote(text)
	}
	if err := p.Journal(e.Caller, "thread.answer", id, oneLine(question+" → "+answer, 160)); err != nil {
		return err
	}
	fmt.Fprintf(e.Stdout, "answered %s: %s\n", id, answer)
	return nil
}

var optionRE = regexp.MustCompile(`^[^0-9A-Za-z]*([0-9])\.\s+(.+)$`)

// dialogLines finds option n of a numbered dialog on screen, and the
// question above the options.
func dialogLines(screen string, n int) (option, question string) {
	lines := strings.Split(screen, "\n")
	first := -1
	for i, l := range lines {
		m := optionRE.FindStringSubmatch(strings.TrimSpace(l))
		if m == nil {
			continue
		}
		if first < 0 {
			first = i
		}
		if m[1] == strconv.Itoa(n) && option == "" {
			option = strings.TrimSpace(m[2])
		}
	}
	for i := first - 1; i >= 0; i-- {
		if t := strings.TrimSpace(lines[i]); strings.HasSuffix(t, "?") {
			question = t
			break
		}
	}
	return option, question
}

func (e *Env) threadAck(p *project.Project, id string) error {
	changed := false
	r, err := thread.Update(p, id, func(x *thread.Record) error {
		if x.Reports == 0 {
			return &tasks.Error{Code: "no-report", Msg: fmt.Sprintf("thread %s has no report", id)}
		}
		changed = x.ReportAck != x.Reports
		x.ReportAck = x.Reports
		return nil
	})
	if err != nil {
		return err
	}
	if !changed {
		fmt.Fprintf(e.Stdout, "%s report %d unchanged (already acked)\n", id, r.Reports)
		return nil
	}
	if err := p.Journal(e.Caller, "thread.ack", id, fmt.Sprintf("report %d", r.Reports)); err != nil {
		return err
	}
	fmt.Fprintf(e.Stdout, "%s report %d acked\n", id, r.Reports)
	return nil
}

func (e *Env) threadStop(p *project.Project, id string) error {
	r, err := thread.Load(p, id)
	if err != nil {
		return err
	}
	if r.State == thread.Resolved {
		return &tasks.Error{Code: "resolved", Msg: fmt.Sprintf("thread %s is resolved", id)}
	}
	_, live := e.sessionOf(r)
	if live {
		if err := e.call(proto.MethodSessionStop, proto.SessionIDParams{ID: r.Session}, nil); err != nil {
			return err
		}
	}
	if _, err := thread.Update(p, id, func(x *thread.Record) error { x.State = thread.Stopped; return nil }); err != nil {
		return err
	}
	if !live && r.State == thread.Stopped {
		fmt.Fprintf(e.Stdout, "%s unchanged (already stopped)\n", id)
		return nil
	}
	if err := p.Journal(e.Caller, "thread.stop", id, ""); err != nil {
		return err
	}
	fmt.Fprintf(e.Stdout, "%s stopped\n", id)
	return nil
}

func (e *Env) threadRestart(p *project.Project, id string) error {
	r, err := thread.Load(p, id)
	if err != nil {
		return err
	}
	if r.State == thread.Resolved {
		return &tasks.Error{Code: "resolved", Msg: fmt.Sprintf("thread %s is resolved; start a new one", id)}
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	safety, err := cfg.Safety(p.Slug)
	if err != nil {
		return err
	}
	if err := pausedErr(p, safety); err != nil {
		return err
	}
	if _, live := e.sessionOf(r); live {
		if err := e.call(proto.MethodSessionStop, proto.SessionIDParams{ID: r.Session}, nil); err != nil {
			return err
		}
	}
	// A worktree removed by hand comes back on its branch: nothing of the
	// thread's lived in it.
	if _, err := os.Stat(r.Worktree); errors.Is(err, os.ErrNotExist) {
		if r.Checkout {
			return &tasks.Error{Code: "no-worktree", Msg: fmt.Sprintf("thread %s's checkout %s is gone; resolve it", id, r.Worktree)}
		}
		if r.Repo != "" && r.Branch != "" {
			if err := worktree.Restore(r.Repo, r.Worktree, r.Branch); err != nil {
				return err
			}
		} else if err := os.MkdirAll(r.Worktree, 0o755); err != nil {
			return err
		}
	}
	brief, err := thread.WriteBrief(p, r, true)
	if err != nil {
		return err
	}
	resume := ""
	if r.Prompted && r.AgentSID != "" {
		resume = r.AgentSID
	}
	if _, err := thread.Update(p, id, func(x *thread.Record) error { x.LastPrompt = time.Now().UTC(); return nil }); err != nil {
		return err
	}
	info, err := e.launchThread(p, r, brief, resume)
	if err != nil {
		return err
	}
	how := "fresh"
	if resume != "" {
		how = "resumed " + resume
	}
	if err := p.Journal(e.Caller, "thread.restart", id, how); err != nil {
		return err
	}
	fmt.Fprintf(e.Stdout, "restarted %s (%s, session %s)\n", id, how, info.ID)
	return nil
}

func (e *Env) threadResolve(p *project.Project, id string) error {
	r, err := thread.Load(p, id)
	if err != nil {
		return err
	}
	if r.State == thread.Resolved {
		fmt.Fprintf(e.Stdout, "%s unchanged (already resolved)\n", id)
		return nil
	}
	if _, live := e.sessionOf(r); live {
		if err := e.call(proto.MethodSessionStop, proto.SessionIDParams{ID: r.Session}, nil); err != nil {
			return err
		}
	}
	var did []string
	switch {
	case r.Checkout:
		// Adopted in the repository's own checkout: not tm's to remove,
		// and its branch is the checkout's (docs/SPEC.md §9, Adopt).
		did = append(did, "kept checkout "+r.Worktree+" (adopted; tm removes only worktrees)")
		more, gone := otherBranches(p, r)
		did = append(did, more...)
		for _, d := range gone {
			if err := p.Journal(e.Caller, "branch.delete", id, d); err != nil {
				return err
			}
		}
	case r.Repo != "":
		_, statErr := os.Stat(r.Worktree)
		err := worktree.Remove(r.Repo, r.Worktree)
		switch {
		case errors.Is(err, worktree.ErrDirty):
			did = append(did, "kept worktree "+r.Worktree+" (uncommitted changes)")
		case err != nil:
			did = append(did, "kept worktree "+r.Worktree+" ("+oneLine(err.Error(), 120)+")")
		case statErr != nil:
			did = append(did, "worktree was already gone")
		default:
			did = append(did, "removed worktree "+r.Worktree)
		}
		var deleted []string
		if r.Branch != "" && worktree.BranchExists(r.Repo, r.Branch) {
			switch st := worktree.PRState(r.Repo, r.Branch); st {
			case "MERGED":
				if err := worktree.DeleteBranch(r.Repo, r.Branch); err != nil {
					did = append(did, "kept branch "+r.Branch+" ("+oneLine(err.Error(), 120)+")")
				} else {
					did = append(did, "deleted branch "+r.Branch+" (PR merged)")
					deleted = append(deleted, r.Branch+" (PR merged)")
				}
			default:
				// No merged PR (a repo without remote or gh, or one merged
				// by hand): a branch whose commits are all on the default
				// branch loses nothing.
				if base := worktree.MergedBase(r.Repo, r.Branch); base != "" {
					if err := worktree.DeleteBranch(r.Repo, r.Branch); err != nil {
						did = append(did, "kept branch "+r.Branch+" ("+oneLine(err.Error(), 120)+")")
					} else {
						did = append(did, "deleted branch "+r.Branch+" (merged into "+base+")")
						deleted = append(deleted, r.Branch+" (merged into "+base+")")
					}
				} else if st == "" {
					did = append(did, "kept branch "+r.Branch+" (no merged PR found)")
				} else {
					did = append(did, "kept branch "+r.Branch+" (PR "+strings.ToLower(st)+")")
				}
			}
		}
		more, gone := otherBranches(p, r)
		did, deleted = append(did, more...), append(deleted, gone...)
		for _, d := range deleted {
			if err := p.Journal(e.Caller, "branch.delete", id, d); err != nil {
				return err
			}
		}
	case r.Adopted:
		did = append(did, "kept folder "+r.Worktree+" (adopted)")
	default:
		if err := os.Remove(r.Worktree); err == nil || errors.Is(err, os.ErrNotExist) {
			did = append(did, "removed folder "+r.Worktree)
		} else {
			did = append(did, "kept folder "+r.Worktree+" (not empty)")
		}
	}
	if _, err := thread.Update(p, id, func(x *thread.Record) error { x.State = thread.Resolved; return nil }); err != nil {
		return err
	}
	summary := thread.Label(p, id) + " resolved: " + strings.Join(did, "; ")
	if _, err := p.AddItem("thread-resolved", id, summary, false); err != nil {
		return err
	}
	if err := p.Journal(e.Caller, "thread.resolve", id, strings.Join(did, "; ")); err != nil {
		return err
	}
	fmt.Fprintln(e.Stdout, summary)
	return nil
}

// otherBranches cleans up the thread's branches other than its own: the
// head branches of the PRs its reports named, and local branches named
// tm/<slug>/<id>-… (docs/SPEC.md §9). Each is deleted with git branch
// -d, and only when worktree.PruneBranch finds nothing would be lost; an
// open PR's branch is kept. It returns what it did, for the resolve
// item, and the deleted branches, for the journal.
func otherBranches(p *project.Project, r *thread.Record) (did, deleted []string) {
	prOf := map[string]string{} // branch → its PR, "PR #12 merged", or ""
	open := map[string]bool{}
	for _, url := range thread.ReportPRs(p, r.ID) {
		st, n, head := worktree.PRHead(r.Repo, url)
		if head == "" || head == r.Branch || !worktree.BranchExists(r.Repo, head) {
			continue
		}
		prOf[head] = fmt.Sprintf("PR #%d %s", n, strings.ToLower(st))
		open[head] = open[head] || st == "OPEN"
	}
	for _, b := range worktree.ThreadBranches(r.Repo, p.Slug, r.ID) {
		if _, ok := prOf[b]; !ok && b != r.Branch {
			prOf[b] = ""
		}
	}
	branches := make([]string, 0, len(prOf))
	for b := range prOf {
		branches = append(branches, b)
	}
	sort.Strings(branches)
	for _, b := range branches {
		pr := prOf[b]
		if open[b] {
			did = append(did, "kept branch "+b+" ("+pr+")")
			continue
		}
		ok, how := worktree.PruneBranch(r.Repo, b)
		if !ok {
			if pr != "" {
				how = pr + ", but " + how
			}
			did = append(did, "kept branch "+b+" ("+oneLine(how, 120)+")")
			continue
		}
		how = "merged into " + how
		if pr != "" {
			how = pr + ", " + how
		}
		did = append(did, "deleted branch "+b+" ("+how+")")
		deleted = append(deleted, b+" ("+how+")")
	}
	return did, deleted
}

// ownThread is the thread a report, status or done call is about: the
// caller's own, or --thread for the human (by hand, or in tests).
func (e *Env) ownThread(p *project.Project, flag string) (*thread.Record, error) {
	switch e.Caller.Kind {
	case caller.Thread:
		if flag != "" && flag != e.Caller.Thread {
			return nil, &tasks.Error{Code: "coordinator-only", Msg: "a thread reports only for itself"}
		}
		if e.Caller.Project != "" && e.Caller.Project != p.Slug {
			return nil, &tasks.Error{Code: "other-project", Msg: "a thread reports only to its own project"}
		}
		flag = e.Caller.Thread
	case caller.Coordinator:
		return nil, &tasks.Error{Code: "thread-only", Msg: "threads report; the coordinator reads reports with tm thread show"}
	default:
		if flag == "" {
			return nil, usagef("outside a thread, pass --thread <id>")
		}
	}
	r, err := thread.Load(p, flag)
	if err != nil {
		return nil, err
	}
	if r.State == thread.Resolved {
		return nil, &tasks.Error{Code: "resolved", Msg: fmt.Sprintf("thread %s is resolved", r.ID)}
	}
	return r, nil
}

const reportUsage = `usage: tm report [--file F] [--attach F]...   (the report on stdin, or from --file)
       tm report --show
Format: optional "PR: <url>" first line, "## Report", required "## Next"
(one action per line, at most 100 characters), optional "## Remember".`

func runReport(e *Env, args []string) error {
	f := newFlags()
	slug, id := f.String("project"), f.String("thread")
	file, attach, show := f.String("file"), f.List("attach"), f.Bool("show")
	pos, err := f.Parse(args)
	if err != nil {
		return err
	}
	if len(pos) != 0 || (*show && f.anySet("file", "attach")) {
		return usagef("%s", reportUsage)
	}
	p, err := e.openProject(*slug)
	if err != nil {
		return err
	}
	if *show {
		tid := *id
		if e.Caller.Kind == caller.Thread {
			tid = e.Caller.Thread
		}
		if tid == "" {
			return usagef("outside a thread, pass --thread <id>")
		}
		if _, err := thread.Load(p, tid); err != nil {
			return err
		}
		data, err := os.ReadFile(thread.Path(p, tid, "REPORT.md"))
		if errors.Is(err, os.ErrNotExist) {
			fmt.Fprintf(e.Stdout, "%s has no report yet\n", tid)
			return nil
		}
		if err != nil {
			return err
		}
		_, err = e.Stdout.Write(data)
		return err
	}
	r, err := e.ownThread(p, *id)
	if err != nil {
		return err
	}
	src := "-"
	if *file != "" {
		src = *file
	}
	text, err := e.readArg(src)
	if err != nil {
		return err
	}
	var files []string
	for _, a := range *attach {
		files = append(files, e.abs(a))
	}
	n, err := thread.StoreReport(p, r.ID, text, files, time.Now())
	if err != nil {
		return err
	}
	summary := fmt.Sprintf("%s handed in report %d", thread.Label(p, r.ID), n)
	if _, err := p.AddItem("report", r.ID, summary, false); err != nil {
		return err
	}
	if err := p.Journal(e.Caller, "thread.report", r.ID, fmt.Sprintf("report %d", n)); err != nil {
		return err
	}
	fmt.Fprintf(e.Stdout, "stored report %d for %s\n", n, r.ID)
	return nil
}

const statusUsage = `usage: tm status --percent N --activity "…" [--needs-you "question"]
       tm status --unknown [--activity "…"] [--needs-you "question"]
       tm status --needs-you "question"`

// runStatus is a thread's self-report (docs/SPEC.md §7.3).
func runStatus(e *Env, args []string) error {
	f := newFlags()
	slug, id := f.String("project"), f.String("thread")
	percent, activity, needsYou, unknown := f.String("percent"), f.String("activity"), f.String("needs-you"), f.Bool("unknown")
	pos, err := f.Parse(args)
	if err != nil {
		return err
	}
	if len(pos) != 0 || !f.anySet("percent", "activity", "needs-you", "unknown") || (f.IsSet("percent") && *unknown) {
		return usagef("%s", statusUsage)
	}
	pct := -1
	if f.IsSet("percent") {
		if pct, err = strconv.Atoi(strings.TrimSuffix(*percent, "%")); err != nil || pct < 0 || pct > 100 {
			return usagef("--percent takes 0-100")
		}
	}
	p, err := e.openProject(*slug)
	if err != nil {
		return err
	}
	r, err := e.ownThread(p, *id)
	if err != nil {
		return err
	}
	wasWaiting := false
	st, err := thread.UpdateStatus(p, r.ID, func(st *thread.Status) error {
		wasWaiting = st.NeedsYou != ""
		if f.anySet("percent", "unknown") {
			st.SelfPercent = pct
		}
		st.SelfAt = time.Now().UTC()
		st.Activity = oneLine(*activity, 100)
		st.NeedsYou = oneLine(*needsYou, 300)
		if st.NeedsYou == "" && strings.EqualFold(st.Activity, "waiting for you") {
			st.NeedsYou = st.Activity
		}
		return nil
	})
	if err != nil {
		return err
	}
	if st.NeedsYou != "" && !wasWaiting {
		// The question itself is the thread's text: data, kept out of
		// the item's summary.
		if _, err := p.AddItem("needs-you", r.ID, thread.Label(p, r.ID)+" is waiting for the user (tm thread show "+r.ID+")", true); err != nil {
			return err
		}
	}
	fmt.Fprintf(e.Stdout, "%s: %d%% %s\n", r.ID, st.Percent, orDash(st.PercentSource))
	return nil
}

func orDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

// runDone is a thread's "finished" (docs/SPEC.md §7.3): it needs a report
// stored since the last prompt. The task's status stays the coordinator's.
func runDone(e *Env, args []string) error {
	f := newFlags()
	slug, id := f.String("project"), f.String("thread")
	pos, err := f.Parse(args)
	if err != nil {
		return err
	}
	if len(pos) > 1 {
		return usagef("usage: tm done [\"summary\"]")
	}
	p, err := e.openProject(*slug)
	if err != nil {
		return err
	}
	r, err := e.ownThread(p, *id)
	if err != nil {
		return err
	}
	if r.Reports == 0 || r.ReportAt.Before(r.LastPrompt) {
		return &tasks.Error{Code: "no-report", Msg: "hand in your report with tm report first (none since your last prompt)"}
	}
	if r.Done && !r.DoneAt.Before(r.ReportAt) {
		fmt.Fprintf(e.Stdout, "%s unchanged (already done)\n", r.ID)
		return nil
	}
	now := time.Now().UTC()
	if _, err := thread.Update(p, r.ID, func(x *thread.Record) error { x.Done, x.DoneAt = true, now; return nil }); err != nil {
		return err
	}
	if _, err := thread.UpdateStatus(p, r.ID, func(st *thread.Status) error { st.Done = true; return nil }); err != nil {
		return err
	}
	summary := thread.Label(p, r.ID) + fmt.Sprintf(" is done; review report %d and move the task", r.Reports)
	if _, err := p.AddItem("thread-done", r.ID, summary, false); err != nil {
		return err
	}
	detail := ""
	if len(pos) == 1 {
		detail = oneLine(pos[0], 80)
	}
	if err := p.Journal(e.Caller, "thread.done", r.ID, detail); err != nil {
		return err
	}
	fmt.Fprintf(e.Stdout, "%s done\n", r.ID)
	return nil
}

// refreshThread recomputes a task's thread's STATUS.md after its steps
// changed.
func refreshThread(p *project.Project, t *tasks.Task) {
	if t != nil && thread.ValidID(t.Thread) {
		thread.UpdateStatus(p, t.Thread, nil)
	}
}
