package cli

import (
	"fmt"
	"path/filepath"
	"strings"
	"text/tabwriter"

	"github.com/theclifmeister/termalator/internal/caller"
	"github.com/theclifmeister/termalator/internal/project"
	"github.com/theclifmeister/termalator/internal/skill"
	"github.com/theclifmeister/termalator/internal/version"
)

const projectUsage = `usage: tm project new <name> [--goal "…"] [--repo PATH]... [--json]
       tm project list [--json]
       tm project repo add|remove PATH [--project <slug>]
       tm project open <slug>        (needs the server; not yet)`

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
	case "open":
		if len(args) != 2 {
			return usagef("%s", projectUsage)
		}
		if _, err := project.Open(args[1]); err != nil {
			return err
		}
		return &project.Error{Code: "not-implemented", Msg: "starting the coordinator session needs the server (M1) and Claude sessions (M3); for now run claude in " + mustDir(args[1])}
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
		fmt.Fprintf(w, "%s\t%s\t%d needs you · %d in motion · %d on deck\t%s\n",
			s.Slug, s.Name, c["needs_you"], c["in_motion"], c["on_deck"], strings.TrimSpace(s.Goal))
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
	sections, err := p.Context()
	if err != nil {
		return err
	}
	if *asJSON {
		return e.printJSON(map[string]any{"project": p.Slug, "sections": sections})
	}
	_, err = fmt.Fprint(e.Stdout, project.RenderContext(sections))
	return err
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

func mustDir(slug string) string {
	d, _ := project.Dir(slug)
	return d
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
