package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/theclifmeister/terminatr/internal/caller"
	"github.com/theclifmeister/terminatr/internal/home"
	"github.com/theclifmeister/terminatr/internal/project"
	"github.com/theclifmeister/terminatr/internal/proto"
	"github.com/theclifmeister/terminatr/internal/thread"
)

// A project's lifecycle, the human's (docs/SPEC.md §4, §10): activate
// and deactivate, pause and resume, archive and unarchive, delete. tm project … and the project
// popup's Settings tab share these. call is a control call, nil when no
// server runs.

type callFn func(method string, params, result any) error

func humanOnly(c caller.Caller, what string) error {
	if c.IsAgent() {
		return &project.Error{Code: "human-only", Msg: "the user " + what}
	}
	return nil
}

// running lists the project's coordinator and thread sessions.
func running(call callFn, slug string) ([]string, error) {
	if call == nil {
		return nil, nil
	}
	var list proto.SessionListResult
	if err := call(proto.MethodSessionList, nil, &list); err != nil {
		return nil, err
	}
	return runningIn(list.Sessions, slug), nil
}

// runningIn names the project's coordinator and thread sessions among
// sessions: "its coordinator", a thread's task id.
func runningIn(sessions []proto.SessionInfo, slug string) []string {
	var out []string
	for _, s := range sessions {
		if s.Project != slug {
			continue
		}
		switch s.Role {
		case proto.RoleCoordinator:
			out = append(out, "its coordinator")
		case proto.RoleThread:
			out = append(out, threadName(s.Task, s.Thread))
		}
	}
	return out
}

// idle refuses while any of the project's agents runs.
func idle(call callFn, slug, what string) error {
	busy, err := running(call, slug)
	if err != nil {
		return err
	}
	if len(busy) > 0 {
		return &project.Error{Code: "sessions-running", Msg: fmt.Sprintf("%s still runs %s: stop them first (tm thread stop <id>; quit the coordinator), then %s it", slug, strings.Join(busy, ", "), what)}
	}
	return nil
}

// PauseProject pauses or resumes a project: while paused the ticker
// sends nothing to its agents (no nudges, no pull request follow-up) and
// tm thread start refuses with project-paused; state polling goes on.
func PauseProject(c caller.Caller, slug string, on bool) (string, error) {
	if err := humanOnly(c, "pauses and resumes projects"); err != nil {
		return "", err
	}
	p, err := project.Open(slug)
	if err != nil {
		return "", err
	}
	changed, err := p.SetFlag(c, "paused", on)
	switch {
	case err != nil:
		return "", err
	case !changed:
		return slug + " is already " + map[bool]string{true: "paused", false: "running"}[on], nil
	case on:
		return "paused " + slug + ": no nudges, pull request follow-up or new threads until you resume it", nil
	}
	return "resumed " + slug, nil
}

// Running names the project's running coordinator and threads ("its
// coordinator", T3, t-0004): what deactivating it stops, for its
// confirmation. None without a server.
func Running(call callFn, slug string) ([]string, error) { return running(call, slug) }

// DeactivateQuestion is the confirmation deactivating a project asks
// while busy (its Running) run.
func DeactivateQuestion(slug string, busy []string) string {
	return "Deactivate " + slug + "? " + upperFirst(joinAnd(busy)) + " stop now and resume when you activate it."
}

// ActivateProject activates or deactivates a project (docs/SPEC.md
// §5.1): through the server when one runs (project.active), which also
// resumes the project's dormant coordinator and threads or stops its
// running ones, keeping them to resume; else the setting alone, which
// the next server start follows. Deactivating asks nothing here: the
// caller confirms first while Running names anything.
func ActivateProject(call callFn, c caller.Caller, slug string, on bool) (string, error) {
	if err := humanOnly(c, "activates and deactivates projects"); err != nil {
		return "", err
	}
	p, err := project.Open(slug)
	if err != nil {
		return "", err
	}
	verb := map[bool]string{true: "activated ", false: "deactivated "}[on]
	if call == nil {
		changed, err := p.SetFlag(c, "active", on)
		switch {
		case err != nil:
			return "", err
		case !changed:
			return slug + " is already " + map[bool]string{true: "active", false: "inactive"}[on], nil
		}
		return verb + slug, nil
	}
	var res proto.ProjectActiveResult
	if err := call(proto.MethodProjectActive, proto.ProjectActiveParams{Project: slug, Active: on}, &res); err != nil {
		return "", err
	}
	var did []string
	if l := sessionNames(p, res.Stopped); len(l) > 0 {
		did = append(did, "stopped "+joinAnd(l)+", to resume when you activate it")
	}
	if l := sessionNames(p, res.Resumed); len(l) > 0 {
		did = append(did, "resumed "+joinAnd(l))
	}
	if l := sessionNames(p, res.Lost); len(l) > 0 {
		did = append(did, "couldn't resume "+joinAnd(l))
	}
	msg := verb + slug
	if !res.Changed {
		msg = slug + " is already " + map[bool]string{true: "active", false: "inactive"}[on]
	}
	if len(did) > 0 {
		msg += ": " + strings.Join(did, "; ")
	}
	return msg, nil
}

// sessionNames are the server's session labels (coordinator, a thread
// id) as the user reads them: "its coordinator", a thread's task id.
func sessionNames(p *project.Project, labels []string) []string {
	var out []string
	for _, l := range labels {
		if l == "coordinator" {
			out = append(out, "its coordinator")
			continue
		}
		task := ""
		if r, err := thread.Load(p, l); err == nil {
			task = r.Task
		}
		out = append(out, threadName(task, l))
	}
	return out
}

// upperFirst capitalizes s's first letter.
func upperFirst(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// ArchiveProject archives or unarchives a project. An archived project
// is hidden from the sidebar, and the ticker leaves it
// alone; archiving is refused while its agents run.
func ArchiveProject(call callFn, c caller.Caller, slug string, on bool) (string, error) {
	if err := humanOnly(c, "archives projects"); err != nil {
		return "", err
	}
	p, err := project.Open(slug)
	if err != nil {
		return "", err
	}
	if on {
		if err := idle(call, slug, "archive"); err != nil {
			return "", err
		}
	}
	changed, err := p.SetFlag(c, "archived", on)
	switch {
	case err != nil:
		return "", err
	case !changed:
		return slug + " is already " + map[bool]string{true: "archived", false: "not archived"}[on], nil
	case on:
		return "archived " + slug + ": hidden from the sidebar, and the ticker leaves it alone (tm project unarchive " + slug + ")", nil
	}
	return "unarchived " + slug, nil
}

// DeleteProject moves a project's folder to the trash, after the
// caller's confirmation; refused while its agents run.
func DeleteProject(call callFn, c caller.Caller, slug string) (string, error) {
	if err := humanOnly(c, "deletes projects"); err != nil {
		return "", err
	}
	p, err := project.Open(slug)
	if err != nil {
		return "", err
	}
	if err := idle(call, slug, "delete"); err != nil {
		return "", err
	}
	dst, err := p.Trash(c, time.Now())
	if err != nil {
		return "", err
	}
	msg := "deleted " + slug + ": moved to " + dst
	if wt, err := home.WorktreesDir(); err == nil {
		dir := filepath.Join(wt, slug)
		if ents, err := os.ReadDir(dir); err == nil && len(ents) > 0 {
			msg += "; its thread worktrees stay in " + dir + " (tm doctor lists them)"
		}
	}
	return msg, nil
}
