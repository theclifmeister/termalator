package cli

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/theclifmeister/termilator/internal/project"
	"github.com/theclifmeister/termilator/internal/tasks"
)

const taskUsage = `usage: tm task <command> [--project <slug>] [--json]

  add "title" [--notes "…" | --notes-file F] [--step "…"]... [--status S] [--owner O]
  add --json < plan.json        bulk: [{"title","notes","steps":[…],"status","owner"}]
  list [--status a,b] [--needs-you] [--archived]
  show T12
  status T12 <open|ready|started|blocked|review|done> [--note "…"]
  status T12 done --approved-by-user
                                the coordinator, once the user accepted the work
  edit T12 [--title "…"] [--notes "…" | --notes-file F] [--owner O]
  steps T12 add "text" | check N | uncheck N | rename N "text" | remove N
  archive T12 | unarchive T12
  delegate T12 [--agent A] [--repo PATH] [--base B] [--approved-by-user] [--over-cap]
                                = tm thread start --task T12

Exit codes: 0 done or already true, 1 refused, 2 usage, 3 I/O.`

func runTask(e *Env, args []string) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "-h" || args[0] == "--help" {
		if len(args) == 0 {
			return usagef("%s", taskUsage)
		}
		fmt.Fprintln(e.Stdout, taskUsage)
		return nil
	}
	sub, rest := args[0], args[1:]
	f := newFlags()
	slug := f.String("project")
	asJSON := f.Bool("json")
	var run func(p *project.Project, s *tasks.Store, pos []string) error
	switch sub {
	case "add":
		notes, notesFile := f.String("notes"), f.String("notes-file")
		steps, status, owner := f.List("step"), f.String("status"), f.String("owner")
		run = func(p *project.Project, s *tasks.Store, pos []string) error {
			return taskAdd(e, s, pos, *asJSON, f, *notes, *notesFile, *steps, *status, *owner)
		}
	case "list", "ls":
		status, needsYou, archived := f.String("status"), f.Bool("needs-you"), f.Bool("archived")
		run = func(p *project.Project, s *tasks.Store, pos []string) error {
			return taskList(e, s, pos, *asJSON, *status, *needsYou, *archived)
		}
	case "show":
		run = func(p *project.Project, s *tasks.Store, pos []string) error {
			id, err := oneRef(pos, 1, "show T12")
			if err != nil {
				return err
			}
			t, err := s.Get(id)
			if err != nil {
				return err
			}
			if *asJSON {
				return e.printJSON(tasks.ToJSON(t))
			}
			_, err = fmt.Fprint(e.Stdout, tasks.Detail(t))
			return err
		}
	case "status":
		note, approved := f.String("note"), f.Bool("approved-by-user")
		run = func(p *project.Project, s *tasks.Store, pos []string) error {
			if len(pos) != 2 {
				return usagef("usage: tm task status T12 <status> [--note \"…\"]")
			}
			id, err := ref(pos[0])
			if err != nil {
				return err
			}
			st, ok := tasks.ParseStatus(pos[1])
			if !ok {
				return &tasks.Error{Code: "invalid-status", Msg: fmt.Sprintf("%q is not one of open, ready, started, blocked, review, done", pos[1])}
			}
			var res tasks.Result
			switch {
			case *approved && st != tasks.Done:
				return usagef("--approved-by-user only goes with done")
			case *approved:
				res, err = s.SetDoneApproved(e.Caller, id, *note)
			default:
				res, err = s.SetStatus(e.Caller, id, st, *note)
			}
			return e.done(res, err, *asJSON, string(st))
		}
	case "edit":
		title, notes, notesFile, owner := f.String("title"), f.String("notes"), f.String("notes-file"), f.String("owner")
		run = func(p *project.Project, s *tasks.Store, pos []string) error {
			id, err := oneRef(pos, 1, "edit T12 [--title …] [--notes … | --notes-file F] [--owner O]")
			if err != nil {
				return err
			}
			if !f.anySet("title", "notes", "notes-file", "owner") {
				return usagef("nothing to edit: pass --title, --notes, --notes-file or --owner")
			}
			var tp, np, op *string
			if f.IsSet("title") {
				tp = title
			}
			if np, err = e.notesArg(f, notes, notesFile); err != nil {
				return err
			}
			if f.IsSet("owner") {
				op = owner
			}
			res, err := s.Edit(e.Caller, id, tp, np, op)
			return e.done(res, err, *asJSON, "edited")
		}
	case "steps", "step":
		run = func(p *project.Project, s *tasks.Store, pos []string) error {
			return taskSteps(e, p, s, pos, *asJSON)
		}
	case "archive", "unarchive":
		run = func(p *project.Project, s *tasks.Store, pos []string) error {
			id, err := oneRef(pos, 1, sub+" T12")
			if err != nil {
				return err
			}
			move := s.Archive
			if sub == "unarchive" {
				move = s.Unarchive
			}
			res, err := move(e.Caller, id)
			return e.done(res, err, *asJSON, sub+"d")
		}
	case "delegate":
		o := startOpts{task: new(string), agent: f.String("agent"), repo: f.String("repo"),
			base: f.String("base"), approved: f.Bool("approved-by-user"), overCap: f.Bool("over-cap")}
		run = func(p *project.Project, s *tasks.Store, pos []string) error {
			id, err := oneRef(pos, 1, "delegate T12 [--agent A] [--repo PATH] [--base B] [--approved-by-user] [--over-cap]")
			if err != nil {
				return err
			}
			*o.task = fmt.Sprintf("T%d", id)
			return e.threadStart(p, o, *asJSON)
		}
	default:
		return usagef("unknown subcommand %q\n%s", sub, taskUsage)
	}
	pos, err := f.Parse(rest)
	if err != nil {
		return err
	}
	p, err := e.openProject(*slug)
	if err != nil {
		return err
	}
	return run(p, p.Tasks(), pos)
}

func ref(s string) (int, error) {
	id, ok := tasks.ParseRef(s)
	if !ok {
		return 0, usagef("%q is not a task id like T12", s)
	}
	return id, nil
}

func oneRef(pos []string, n int, usage string) (int, error) {
	if len(pos) != n {
		return 0, usagef("usage: tm task %s", usage)
	}
	return ref(pos[0])
}

// notesArg returns the --notes or --notes-file value, or nil if neither
// was given.
func (e *Env) notesArg(f *flagSet, notes, notesFile *string) (*string, error) {
	switch {
	case f.IsSet("notes") && f.IsSet("notes-file"):
		return nil, usagef("pass --notes or --notes-file, not both")
	case f.IsSet("notes"):
		return notes, nil
	case f.IsSet("notes-file"):
		s, err := e.readArg(*notesFile)
		return &s, err
	}
	return nil, nil
}

// done prints the outcome of a single-task change.
func (e *Env) done(res tasks.Result, err error, asJSON bool, what string) error {
	if err != nil {
		return err
	}
	if asJSON {
		return e.printJSON(map[string]any{"changed": res.Changed, "task": tasks.ToJSON(res.Task)})
	}
	if res.Changed {
		fmt.Fprintf(e.Stdout, "%s %s\n", res.Task.Ref(), what)
	} else {
		fmt.Fprintf(e.Stdout, "%s unchanged (already %s)\n", res.Task.Ref(), what)
	}
	return nil
}

func taskAdd(e *Env, s *tasks.Store, pos []string, asJSON bool, f *flagSet, notes, notesFile string, steps []string, status, owner string) error {
	var items []tasks.NewTask
	switch {
	case len(pos) == 0 && asJSON:
		if f.anySet("notes", "notes-file", "step", "status", "owner") {
			return usagef("a JSON plan on stdin carries its own fields; drop the other flags")
		}
		dec := json.NewDecoder(e.Stdin)
		dec.DisallowUnknownFields()
		if err := dec.Decode(&items); err != nil {
			return usagef("plan on stdin: %v (want [{\"title\",\"notes\",\"steps\",\"status\",\"owner\"}])", err)
		}
		if len(items) == 0 {
			return usagef("the plan is empty")
		}
	case len(pos) == 1:
		it := tasks.NewTask{Title: pos[0], Steps: steps, Status: status, Owner: owner}
		np, err := e.notesArg(f, &notes, &notesFile)
		if err != nil {
			return err
		}
		if np != nil {
			it.Notes = *np
		}
		items = []tasks.NewTask{it}
	default:
		return usagef("usage: tm task add \"title\" [flags] | tm task add --json < plan.json")
	}
	results, err := s.Add(e.Caller, items)
	if err != nil {
		return err
	}
	failed := 0
	for _, r := range results {
		if r.Result == "failed" {
			failed++
		}
	}
	if asJSON {
		if err := e.printJSON(results); err != nil {
			return err
		}
	} else {
		for _, r := range results {
			switch r.Result {
			case "failed":
				fmt.Fprintf(e.Stderr, "tm task: failed %q: %s: %s\n", r.Title, r.Code, r.Error)
			default:
				fmt.Fprintf(e.Stdout, "%s %s %s\n", r.Result, r.ID, r.Title)
			}
		}
	}
	if failed > 0 {
		return &exitError{ExitRefused}
	}
	return nil
}

func taskList(e *Env, s *tasks.Store, pos []string, asJSON bool, status string, needsYou, archived bool) error {
	if len(pos) != 0 {
		return usagef("usage: tm task list [--status a,b] [--needs-you] [--archived] (tm task show T12 for one task)")
	}
	want := map[tasks.Status]bool{}
	for _, name := range strings.Split(status, ",") {
		if strings.TrimSpace(name) == "" {
			continue
		}
		st, ok := tasks.ParseStatus(name)
		if !ok {
			return usagef("unknown status %q", name)
		}
		want[st] = true
	}
	if needsYou {
		want[tasks.Blocked], want[tasks.Review] = true, true
	}
	load := s.Load
	if archived {
		load = s.LoadArchive
	}
	b, err := load()
	if err != nil {
		return err
	}
	if !archived {
		b.Sort()
	}
	var shown []*tasks.Task
	for _, t := range b.Tasks {
		if len(want) == 0 || want[t.Status] {
			shown = append(shown, t)
		}
	}
	if asJSON {
		out := make([]tasks.JSON, 0, len(shown))
		for _, t := range shown {
			out = append(out, tasks.ToJSON(t))
		}
		return e.printJSON(out)
	}
	if len(shown) == 0 {
		fmt.Fprintln(e.Stdout, "no tasks")
		return nil
	}
	// One alignment for the whole list, across its groups.
	lines := tasks.Lines(shown)
	if archived {
		for _, l := range lines {
			fmt.Fprintln(e.Stdout, l)
		}
		return nil
	}
	var g tasks.Group
	for i, t := range shown {
		if gg := tasks.GroupOf(t.Status); gg != g {
			g = gg
			fmt.Fprintln(e.Stdout, strings.ToUpper(string(g)))
		}
		fmt.Fprintln(e.Stdout, "  "+lines[i])
	}
	return nil
}

const stepsUsage = `usage: tm task steps T12 add "text" | check N | uncheck N | rename N "text" | remove N`

func taskSteps(e *Env, p *project.Project, s *tasks.Store, pos []string, asJSON bool) error {
	if len(pos) < 2 {
		return usagef("%s", stepsUsage)
	}
	id, err := ref(pos[0])
	if err != nil {
		return err
	}
	verb, args := pos[1], pos[2:]
	num := func() (int, error) {
		if len(args) == 0 {
			return 0, usagef("%s", stepsUsage)
		}
		n, err := strconv.Atoi(args[0])
		if err != nil || n < 1 {
			return 0, usagef("%q is not a step number", args[0])
		}
		return n, nil
	}
	var res tasks.Result
	var what string
	switch verb {
	case "add":
		if len(args) != 1 {
			return usagef("%s", stepsUsage)
		}
		res, err = s.StepAdd(e.Caller, id, args[0])
		if err == nil {
			what = fmt.Sprintf("step %d added", len(res.Task.Steps))
		}
	case "check", "uncheck":
		n, uerr := num()
		if uerr != nil || len(args) != 1 {
			return usagef("%s", stepsUsage)
		}
		res, err = s.StepCheck(e.Caller, id, n, verb == "check")
		what = fmt.Sprintf("step %d %sed", n, verb)
	case "rename":
		n, uerr := num()
		if uerr != nil || len(args) != 2 {
			return usagef("%s", stepsUsage)
		}
		res, err = s.StepRename(e.Caller, id, n, args[1])
		what = fmt.Sprintf("step %d renamed", n)
	case "remove", "rm":
		n, uerr := num()
		if uerr != nil || len(args) != 1 {
			return usagef("%s", stepsUsage)
		}
		res, err = s.StepRemove(e.Caller, id, n)
		what = fmt.Sprintf("step %d removed", n)
	default:
		return usagef("unknown steps command %q\n%s", verb, stepsUsage)
	}
	if err == nil && res.Changed {
		refreshThread(p, res.Task)
	}
	return e.done(res, err, asJSON, what)
}
