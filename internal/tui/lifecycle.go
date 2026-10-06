package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/theclifmeister/termilator/internal/caller"
	"github.com/theclifmeister/termilator/internal/home"
	"github.com/theclifmeister/termilator/internal/project"
	"github.com/theclifmeister/termilator/internal/proto"
)

// A project's lifecycle, the human's (docs/SPEC.md §4, §10): pause and
// resume, archive and unarchive, delete. tm project … and the project
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
	var out []string
	for _, s := range list.Sessions {
		if s.Project != slug {
			continue
		}
		switch s.Role {
		case proto.RoleCoordinator:
			out = append(out, "its coordinator")
		case proto.RoleThread:
			out = append(out, s.Thread)
		}
	}
	return out, nil
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

// ArchiveProject archives or unarchives a project. An archived project
// is hidden from the sidebar and the switcher, and the ticker leaves it
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
		return "archived " + slug + ": hidden from the sidebar and the switcher, and the ticker leaves it alone (tm project unarchive " + slug + ")", nil
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
