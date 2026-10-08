package cli

import (
	"fmt"
	"time"

	"github.com/theclifmeister/terminatr/internal/caller"
	"github.com/theclifmeister/terminatr/internal/project"
	"github.com/theclifmeister/terminatr/internal/thread"
)

const libraryUsage = `usage: tm library list [--project <slug>] [--json]
       tm library rm <thread> <file> | --all [--project <slug>]

list shows the files every thread of the project attached to its reports
(its library), the resolved and archived threads' too, newest first, by
name and size. rm deletes one file of a thread, or all of its files with
--all; it is the coordinator's, and only on the user's word in chat.`

// runLibrary lists the project's library files, or deletes them
// (docs/SPEC.md §7.2, Library).
func runLibrary(e *Env, args []string) error {
	if len(args) == 0 {
		return usagef("%s", libraryUsage)
	}
	f := newFlags()
	slug, asJSON, all := f.String("project"), f.Bool("json"), f.Bool("all")
	pos, err := f.Parse(args[1:])
	if err != nil {
		return err
	}
	if e.Caller.Kind == caller.Thread {
		return &project.Error{Code: "coordinator-only", Msg: "the library is the coordinator's to list and clean up"}
	}
	switch args[0] {
	case "list", "ls":
		if len(pos) > 0 {
			return usagef("%s", libraryUsage)
		}
		p, err := e.openProject(*slug)
		if err != nil {
			return err
		}
		files, err := thread.Library(p)
		if err != nil {
			return err
		}
		if *asJSON {
			if files == nil {
				files = []thread.LibFile{}
			}
			return e.printJSON(files)
		}
		for _, l := range files {
			task := l.Task
			if task == "" {
				task = "-"
			}
			fmt.Fprintf(e.Stdout, "%s  %s  %s  %s  %d bytes  %s\n", l.Time.Local().Format(time.DateOnly), task, l.Thread, l.Name, l.Size, l.State)
		}
		return nil
	case "rm":
		name := ""
		switch {
		case *all && len(pos) == 1:
		case !*all && len(pos) == 2:
			name = pos[1]
		default:
			return usagef("%s", libraryUsage)
		}
		p, err := e.openProject(*slug)
		if err != nil {
			return err
		}
		n, err := thread.RemoveLibrary(p, e.Caller, pos[0], name)
		if err != nil {
			return err
		}
		fmt.Fprintf(e.Stdout, "deleted %d file(s) of %s\n", n, pos[0])
		return nil
	}
	return usagef("unknown subcommand %q\n%s", args[0], libraryUsage)
}
