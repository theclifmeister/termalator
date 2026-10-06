package cli

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/theclifmeister/terminatr/internal/agent"
	"github.com/theclifmeister/terminatr/internal/caller"
	"github.com/theclifmeister/terminatr/internal/config"
	"github.com/theclifmeister/terminatr/internal/project"
	"github.com/theclifmeister/terminatr/internal/proto"
	"github.com/theclifmeister/terminatr/internal/rename"
	"github.com/theclifmeister/terminatr/internal/server"
	"github.com/theclifmeister/terminatr/internal/skill"
	"github.com/theclifmeister/terminatr/internal/ticker"
	"github.com/theclifmeister/terminatr/internal/tui"
	"github.com/theclifmeister/terminatr/internal/version"
)

const projectUsage = `usage: tm project new <name> [--goal "…"] [--repo PATH]... [--json]
       tm project list [--json]
       tm project repo add|remove PATH [--project <slug>]
       tm project open <slug> [--agent NAME]   (start or attach its coordinator)
       tm project remote on|off [<slug>]   (remote control of its running coordinator)
       tm project pause|resume [<slug>]    (no nudges, PR follow-up or new threads while paused)
       tm project archive|unarchive <slug> (hidden from the sidebar; the ticker leaves it alone)
       tm project delete <slug> [--yes]    (moves it to the trash; asks first)
       tm project rename <slug> <new-slug> [--name "…"] [--json]   (no thread may run; restarts its coordinator)`

func runProject(e *Env, args []string) error {
	if len(args) == 0 {
		return usagef("%s", projectUsage)
	}
	switch args[0] {
	case "new":
		return projectNew(e, args[1:])
	case "list", "ls":
		return projectList(e, args[1:])
	case "repo", "repos":
		return projectRepo(e, args[1:])
	case "remote":
		return projectRemote(e, args[1:])
	case "pause", "resume", "archive", "unarchive", "delete":
		return projectLifecycle(e, args[0], args[1:])
	case "rename", "mv":
		return projectRename(e, args[1:])
	case "open":
		f := newFlags()
		agentName := f.String("agent")
		pos, err := f.Parse(args[1:])
		if err != nil {
			return err
		}
		if len(pos) != 1 {
			return usagef("%s", projectUsage)
		}
		if *agentName == "" {
			*agentName = config.DefaultAgent(defaultAgent)
		}
		if e.Caller.IsAgent() {
			return &project.Error{Code: "human-only", Msg: "the human opens projects"}
		}
		return e.openCmd(pos[0], *agentName)
	}
	return usagef("unknown subcommand %q\n%s", args[0], projectUsage)
}

func projectNew(e *Env, args []string) error {
	f := newFlags()
	goal, repos, asJSON := f.String("goal"), f.List("repo"), f.Bool("json")
	pos, err := f.Parse(args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usagef("%s", projectUsage)
	}
	if e.Caller.IsAgent() {
		return &project.Error{Code: "human-only", Msg: "only the human creates projects"}
	}
	p, err := project.New(project.Options{Name: pos[0], Goal: *goal, Repos: *repos})
	if err != nil {
		return err
	}
	if *asJSON {
		return e.printJSON(map[string]any{"slug": p.Slug, "name": p.Meta.Name, "dir": p.Dir, "goal": p.Meta.Goal, "repos": p.Meta.Repos})
	}
	fmt.Fprintf(e.Stdout, "created project %s at %s\n", p.Slug, p.Dir)
	return nil
}

// projectLifecycle pauses, resumes, archives, unarchives or deletes a
// project: the human's (docs/SPEC.md §10).
func projectLifecycle(e *Env, verb string, args []string) error {
	f := newFlags()
	yes := f.Bool("yes")
	pos, err := f.Parse(args)
	if err != nil {
		return err
	}
	pause := verb == "pause" || verb == "resume"
	if len(pos) > 1 || (len(pos) == 0 && !pause) || (*yes && verb != "delete") {
		return usagef("%s", projectUsage)
	}
	if e.Caller.IsAgent() {
		return &project.Error{Code: "human-only", Msg: "the user pauses, archives and deletes projects"}
	}
	flag := ""
	if len(pos) == 1 {
		flag = pos[0]
	}
	p, err := e.openProject(flag)
	if err != nil {
		return err
	}
	var call func(method string, params, result any) error
	if !pause {
		c, _, err := connect(false)
		if err == nil {
			defer c.Close()
			call = c.Call
		}
	}
	var msg string
	switch verb {
	case "pause", "resume":
		msg, err = tui.PauseProject(e.Caller, p.Slug, verb == "pause")
	case "archive", "unarchive":
		msg, err = tui.ArchiveProject(call, e.Caller, p.Slug, verb == "archive")
	case "delete":
		if !*yes {
			if err := e.confirmDelete(p); err != nil {
				return err
			}
		}
		msg, err = tui.DeleteProject(call, e.Caller, p.Slug)
	}
	if err != nil {
		var pe *proto.Error
		if errors.As(err, &pe) {
			return &project.Error{Code: pe.Code, Msg: pe.Message}
		}
		return err
	}
	fmt.Fprintln(e.Stdout, msg)
	return nil
}

// projectRename renames a project's slug (docs/SPEC.md §5.1): through
// the server when one runs, which stops and restarts the coordinator and
// carries the ticker's memos over; else here.
func projectRename(e *Env, args []string) error {
	f := newFlags()
	name, asJSON := f.String("name"), f.Bool("json")
	pos, err := f.Parse(args)
	if err != nil {
		return err
	}
	if len(pos) != 2 {
		return usagef("%s", projectUsage)
	}
	if e.Caller.IsAgent() {
		return &project.Error{Code: "human-only", Msg: "the user renames projects"}
	}
	params := proto.ProjectRenameParams{From: pos[0], To: pos[1], Name: *name}
	var res proto.ProjectRenameResult
	c, _, err := connect(false)
	switch {
	case err == nil:
		defer c.Close()
		if err := c.Call(proto.MethodProjectRename, params, &res); err != nil {
			var pe *proto.Error
			if errors.As(err, &pe) {
				return &project.Error{Code: pe.Code, Msg: pe.Message}
			}
			return err
		}
	case errors.Is(err, server.ErrNotRunning):
		if res, err = renameOffline(e, params); err != nil {
			return err
		}
	default:
		return err
	}
	if *asJSON {
		return e.printJSON(res)
	}
	if params.To == params.From {
		fmt.Fprintf(e.Stdout, "renamed %s to %q\n", params.From, *name)
	} else {
		fmt.Fprintf(e.Stdout, "renamed %s to %s: %s\n", params.From, params.To, res.Dir)
	}
	if res.Worktrees != "" {
		fmt.Fprintf(e.Stdout, "  worktrees in %s (%d thread records updated); branches keep their names\n", res.Worktrees, res.Threads)
	}
	if res.Coordinator != "" {
		fmt.Fprintf(e.Stdout, "  coordinator started again: %s (tm project open %s)\n", res.Coordinator, params.To)
	}
	for _, n := range res.Notes {
		fmt.Fprintf(e.Stdout, "  note: %s\n", n)
	}
	return nil
}

// renameOffline renames with no server running: nothing of the project
// can run, and the ticker's memos are in its state file.
func renameOffline(e *Env, p proto.ProjectRenameParams) (proto.ProjectRenameResult, error) {
	var out proto.ProjectRenameResult
	paths, err := server.ResolvePaths()
	if err != nil {
		return out, err
	}
	o := rename.Options{From: p.From, To: p.To, Name: p.Name, Caller: e.Caller}
	if hd, err := os.UserHomeDir(); err == nil {
		if reg, _ := agent.Load(paths.AgentsDir()); reg != nil {
			o.MoveAgentDir = func(from, to string) error { return server.MoveAgentDirs(reg, hd, from, to) }
		}
	}
	res, err := rename.Project(o)
	if res == nil {
		return out, err
	}
	out = proto.ProjectRenameResult{Dir: res.Dir, Worktrees: res.Worktrees, Threads: res.Threads, Notes: res.Notes}
	if err != nil {
		out.Notes = append(out.Notes, err.Error())
	}
	if p.To != p.From {
		if err := ticker.RenameProjectState(ticker.StatePath(paths.Sessions), p.From, p.To); err != nil {
			out.Notes = append(out.Notes, "ticker memos not carried over: "+err.Error())
		}
	}
	return out, nil
}

// confirmDelete asks on the terminal for the slug; without one, delete
// needs --yes.
func (e *Env) confirmDelete(p *project.Project) error {
	in, ok := e.Stdin.(*os.File)
	if !ok || !isTTY(in) {
		return usagef("tm project delete %s moves the project to the trash: confirm with --yes", p.Slug)
	}
	fmt.Fprintf(e.Stdout, "Delete project %s (%s)? Its folder moves to the trash; its worktrees and branches stay. Type %s to confirm: ", p.Slug, p.Dir, p.Slug)
	line, _ := bufio.NewReader(in).ReadString('\n')
	if strings.TrimSpace(line) != p.Slug {
		return &project.Error{Code: "not-confirmed", Msg: "nothing deleted"}
	}
	return nil
}

func projectList(e *Env, args []string) error {
	f := newFlags()
	asJSON := f.Bool("json")
	pos, err := f.Parse(args)
	if err != nil {
		return err
	}
	if len(pos) != 0 {
		return usagef("%s", projectUsage)
	}
	list, err := project.List()
	if err != nil {
		return err
	}
	if *asJSON {
		if list == nil {
			list = []project.Summary{}
		}
		return e.printJSON(list)
	}
	if len(list) == 0 {
		fmt.Fprintln(e.Stdout, "no projects; create one with tm project new <name>")
		return nil
	}
	w := tabwriter.NewWriter(e.Stdout, 0, 4, 2, ' ', 0)
	for _, s := range list {
		if s.Error != "" {
			fmt.Fprintf(w, "%s\t(error: %s)\n", s.Slug, s.Error)
			continue
		}
		c := s.Counts
		name := s.Name
		if s.Safety != nil && s.Safety.Archived {
			name += " (archived)"
		} else if s.Safety != nil && s.Safety.Paused {
			name += " (paused)"
		}
		fmt.Fprintf(w, "%s\t%s\t%d needs you · %d in motion · %d on deck\t%s\n",
			s.Slug, name, c["needs_you"], c["in_motion"], c["on_deck"], strings.TrimSpace(s.Goal))
	}
	return w.Flush()
}

// openProject resolves and opens the project for a command (§6.3).
func (e *Env) openProject(flag string) (*project.Project, error) {
	slug, err := project.Resolve(flag, e.Getenv, e.Cwd)
	if err != nil {
		return nil, err
	}
	if slug == "" {
		return nil, usagef("no project here; pass --project <slug> (tm project list)")
	}
	return project.Open(slug)
}

const contextUsage = `usage: tm context [--project <slug>] [--json]`

func runContext(e *Env, args []string) error {
	f := newFlags()
	slug, asJSON := f.String("project"), f.Bool("json")
	pos, err := f.Parse(args)
	if err != nil {
		return err
	}
	if len(pos) != 0 {
		return usagef("%s", contextUsage)
	}
	p, err := e.openProject(*slug)
	if err != nil {
		return err
	}
	seen := ticker.Seen(tickerState(), p.Slug)
	seen.Queues, seen.Questions = liveWaits(p.Slug)
	sections, err := p.Context(seen)
	if err != nil {
		return err
	}
	if *asJSON {
		return e.printJSON(map[string]any{"project": p.Slug, "sections": sections})
	}
	_, err = fmt.Fprint(e.Stdout, project.RenderContext(sections))
	return err
}

// liveWaits asks a running server (it starts none) for the project's
// sessions whose queued prompts are held while the agent is idle, and
// those with a question menu open.
func liveWaits(slug string) (queues []project.HeldQueue, questions []project.OpenQuestion) {
	c, _, err := connect(false)
	if err != nil {
		return nil, nil
	}
	defer c.Close()
	var res proto.SessionListResult
	if c.Call(proto.MethodSessionList, nil, &res) != nil {
		return nil, nil
	}
	for _, s := range res.Sessions {
		if s.Project != slug {
			continue
		}
		if s.QueueNote(time.Now()) != "" {
			queues = append(queues, project.HeldQueue{Session: s.ID, Role: s.Role, Thread: s.Thread, Task: s.Task, Queued: s.Queued, Why: s.QueueHeld, Since: s.QueueHeldSince})
		}
		if s.Question != nil {
			questions = append(questions, project.OpenQuestion{Session: s.ID, Role: s.Role, Thread: s.Thread, Task: s.Task, Since: s.Question.Since, Lines: questionLines(s.Question)})
		}
	}
	return queues, questions
}

const inboxUsage = `usage: tm inbox list [--project <slug>] [--json]
       tm inbox done <id>... [--project <slug>]`

func runInbox(e *Env, args []string) error {
	if len(args) == 0 {
		return usagef("%s", inboxUsage)
	}
	f := newFlags()
	slug, asJSON := f.String("project"), f.Bool("json")
	pos, err := f.Parse(args[1:])
	if err != nil {
		return err
	}
	if e.Caller.Kind == caller.Thread {
		return &project.Error{Code: "coordinator-only", Msg: "the inbox is the coordinator's"}
	}
	p, err := e.openProject(*slug)
	if err != nil {
		return err
	}
	switch args[0] {
	case "list", "ls":
		items, err := p.Inbox()
		if err != nil {
			return err
		}
		if *asJSON {
			if items == nil {
				items = []project.Item{}
			}
			return e.printJSON(items)
		}
		for _, it := range items {
			flag := ""
			if it.NeedsUser {
				flag = " [needs user]"
			}
			fmt.Fprintf(e.Stdout, "%s  %s%s: %s\n", it.ID, it.Kind, flag, it.Summary)
		}
		return nil
	case "done":
		if len(pos) == 0 {
			return usagef("%s", inboxUsage)
		}
		var failed error
		for _, id := range pos {
			if err := p.DoneItem(id); err != nil {
				fmt.Fprintf(e.Stderr, "tm inbox: %v\n", err)
				failed = &exitError{ExitRefused}
				continue
			}
			fmt.Fprintf(e.Stdout, "done %s\n", id)
		}
		return failed
	}
	return usagef("unknown subcommand %q\n%s", args[0], inboxUsage)
}

const skillUsage = `usage: tm skill coordinator|thread`

// runSkill prints a role's standing rules (§7.7), for any caller.
func runSkill(e *Env, args []string) error {
	if len(args) != 1 {
		return usagef("%s", skillUsage)
	}
	text, ok := skill.Text(args[0], version.Version)
	if !ok {
		return usagef("no rules for role %q\n%s", args[0], skillUsage)
	}
	_, err := fmt.Fprint(e.Stdout, text)
	return err
}

// projectRepo changes the project's repo list in PROJECT.md: the human
// or the coordinator.
func projectRepo(e *Env, args []string) error {
	f := newFlags()
	slug := f.String("project")
	pos, err := f.Parse(args)
	if err != nil {
		return err
	}
	if len(pos) != 2 || (pos[0] != "add" && pos[0] != "remove" && pos[0] != "rm") {
		return usagef("usage: tm project repo add|remove PATH [--project <slug>]")
	}
	p, err := e.openProject(*slug)
	if err != nil {
		return err
	}
	if err := e.coordinatorOnly(p, "the repo list"); err != nil {
		return err
	}
	path := e.abs(pos[1])
	if r, err := filepath.EvalSymlinks(path); err == nil {
		path = r
	}
	add := pos[0] == "add"
	changed, err := p.SetRepo(path, add)
	if err != nil {
		return err
	}
	verb := "added"
	if !add {
		verb = "removed"
	}
	if !changed {
		fmt.Fprintf(e.Stdout, "%s unchanged (already %s)\n", path, verb)
		return nil
	}
	if err := p.Journal(e.Caller, "project.repo."+pos[0], p.Slug, path); err != nil {
		return err
	}
	fmt.Fprintf(e.Stdout, "%s %s\n", verb, path)
	return nil
}

// projectRemote turns remote control of a project's running coordinator
// on or off (docs/SPEC.md §8.2). It changes the live session only; the
// project's setting decides how the next coordinator starts.
func projectRemote(e *Env, args []string) error {
	pos, err := newFlags().Parse(args)
	if err != nil {
		return err
	}
	if len(pos) < 1 || len(pos) > 2 || (pos[0] != "on" && pos[0] != "off") {
		return usagef("usage: tm project remote on|off [<slug>]")
	}
	if e.Caller.IsAgent() {
		return &project.Error{Code: "human-only", Msg: "the user decides about remote control"}
	}
	flag := ""
	if len(pos) == 2 {
		flag = pos[1]
	}
	p, err := e.openProject(flag)
	if err != nil {
		return err
	}
	c, _, err := connect(false)
	if err != nil {
		return err
	}
	defer c.Close()
	res, err := tui.SetRemote(c.Call, p.Slug, pos[0] == "on")
	if err != nil {
		var pe *proto.Error
		if errors.As(err, &pe) {
			return &project.Error{Code: pe.Code, Msg: pe.Message}
		}
		return err
	}
	fmt.Fprintln(e.Stdout, tui.RemoteMessage(p.Slug, res))
	return nil
}
