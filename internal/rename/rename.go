// Package rename renames a project's slug (tm project rename,
// docs/SPEC.md §5.1): its folder, its threads' worktrees folder, its
// settings in config.toml and the paths in its thread records. The
// journal, inbox, tasks and memory move with the folder unchanged.
// Branches keep their names: tm/<slug>/ names only new threads' branches.
//
// The caller makes sure no agent of the project runs: the server, or
// the CLI when no server runs, and carries the ticker's memos over.
package rename

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/theclifmeister/terminatr/internal/caller"
	"github.com/theclifmeister/terminatr/internal/config"
	"github.com/theclifmeister/terminatr/internal/home"
	"github.com/theclifmeister/terminatr/internal/project"
	"github.com/theclifmeister/terminatr/internal/thread"
	"github.com/theclifmeister/terminatr/internal/worktree"
)

// Options say what to rename.
type Options struct {
	From, To string
	// Name is the new display name; "" keeps it.
	Name   string
	Caller caller.Caller
	// MoveAgentDir carries agents' state kept by folder path (their
	// conversations, to resume) from a moved folder to its new path; nil
	// moves none (agent.Mover).
	MoveAgentDir func(from, to string) error
}

// Result is what a rename did.
type Result struct {
	Dir       string // the project's folder now
	Worktrees string // its threads' worktrees folder now; "" if it had none
	Threads   int    // thread records whose worktree path changed
	// Notes are what didn't go through after the rename stood, e.g. a
	// worktree git couldn't reconnect.
	Notes []string
}

func refuse(code, format string, a ...any) error {
	return &project.Error{Code: code, Msg: fmt.Sprintf(format, a...)}
}

// Check refuses a rename that can't be done, before anything moves:
// an unknown project, an invalid or taken slug, settings tm can't
// rename.
func Check(o Options) error {
	if _, err := project.Open(o.From); err != nil {
		return err
	}
	if !project.ValidSlug(o.To) {
		return refuse("invalid-project", "%q is not a project slug (a-z, 0-9 and -)", o.To)
	}
	if o.To == o.From {
		if o.Name == "" {
			return refuse("unchanged", "%s is already called %s; pass a new slug, or --name to change only its name", o.From, o.To)
		}
		return nil
	}
	dir, err := project.Dir(o.To)
	if err != nil {
		return err
	}
	if _, err := os.Lstat(dir); err == nil {
		return refuse("project-exists", "a project %s exists already (%s)", o.To, dir)
	}
	wt, err := home.WorktreesDir()
	if err != nil {
		return err
	}
	if _, err := os.Lstat(filepath.Join(wt, o.To)); err == nil {
		return refuse("project-exists", "%s exists already: worktrees of an earlier project %s (tm doctor lists leftovers)", filepath.Join(wt, o.To), o.To)
	}
	path, err := config.Path()
	if err != nil {
		return err
	}
	data, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if data != nil {
		switch _, err := config.RenameTable(data, o.From, o.To); {
		case errors.Is(err, config.ErrProjectSet):
			return refuse("project-exists", "%s already has settings for %s ([projects.%s]); remove them first", path, o.To, o.To)
		case errors.Is(err, config.ErrForm):
			return refuse("settings-form", "%s sets %s's settings in a form tm can't rename (dotted keys or an inline table): rename them to [projects.%s] by hand, or make them a [projects.%s] table first", path, o.From, o.To, o.From)
		case err != nil:
			return err
		}
	}
	return nil
}

// Project renames project From to To. It checks first (Check); an error
// means nothing changed. Once the folders have moved the rename stands,
// and what didn't go through after that is in Result.Notes.
func Project(o Options) (*Result, error) {
	if err := Check(o); err != nil {
		return nil, err
	}
	if o.To == o.From {
		p, err := project.Open(o.From)
		if err != nil {
			return nil, err
		}
		was := p.Meta.Name
		if err := p.SetName(o.Name); err != nil {
			return nil, err
		}
		return &Result{Dir: p.Dir}, p.Journal(o.Caller, "project.rename", p.Slug, fmt.Sprintf("name %q → %q", was, p.Meta.Name))
	}
	if o.Name != "" && (strings.TrimSpace(o.Name) == "" || strings.ContainsAny(o.Name, "\r\n")) {
		return nil, refuse("invalid-name", "the name must be one non-empty line")
	}
	oldDir, _ := project.Dir(o.From)
	newDir, _ := project.Dir(o.To)
	root, err := home.WorktreesDir()
	if err != nil {
		return nil, err
	}
	oldWT, newWT := filepath.Join(root, o.From), filepath.Join(root, o.To)
	res := &Result{Dir: newDir}

	// The two moves are the rename; the second failing undoes the first.
	movedWT := false
	if _, err := os.Lstat(oldWT); err == nil {
		if err := os.Rename(oldWT, newWT); err != nil {
			return nil, err
		}
		movedWT, res.Worktrees = true, newWT
	}
	if err := os.Rename(oldDir, newDir); err != nil {
		if movedWT {
			os.Rename(newWT, oldWT)
		}
		return nil, err
	}
	note := func(format string, a ...any) { res.Notes = append(res.Notes, fmt.Sprintf(format, a...)) }

	if err := config.RenameProject(o.From, o.To); err != nil {
		note("config.toml still has [projects.%s]: %v (rename it to [projects.%s] by hand)", o.From, err, o.To)
	}
	p, err := project.Open(o.To)
	if err != nil {
		return res, err
	}
	// Thread records name their worktree by its full path.
	recs, err := thread.List(p)
	if err != nil {
		note("thread records unreadable: %v", err)
	}
	var moved [][2]string // worktree paths, old and new
	for _, r := range recs {
		rel, ok := under(oldWT, r.Worktree)
		if !ok {
			continue // an adopted thread's folder elsewhere stays
		}
		to := filepath.Join(newWT, rel)
		if _, err := thread.Update(p, r.ID, func(x *thread.Record) error { x.Worktree = to; return nil }); err != nil {
			note("thread %s: record not updated: %v", r.ID, err)
			continue
		}
		res.Threads++
		if _, err := os.Stat(to); err == nil {
			moved = append(moved, [2]string{r.Worktree, to})
		}
	}
	// git knows each worktree by path: run in a moved worktree, repair
	// points its repo's record of it at the new path.
	if movedWT {
		ents, _ := os.ReadDir(newWT)
		for _, e := range ents {
			dir := filepath.Join(newWT, e.Name())
			if fi, err := os.Lstat(filepath.Join(dir, ".git")); err != nil || !fi.Mode().IsRegular() {
				continue
			}
			if err := worktree.Reconnect(dir); err != nil {
				note("%s: git doesn't know its new place: %v", dir, err)
			}
		}
	}
	if o.MoveAgentDir != nil {
		for _, m := range append([][2]string{{oldDir, newDir}}, moved...) {
			if err := o.MoveAgentDir(m[0], m[1]); err != nil {
				note("%s: agent state not carried over: %v", m[1], err)
			}
		}
	}
	detail := o.From + " → " + o.To
	if o.Name != "" {
		was := p.Meta.Name
		if err := p.SetName(o.Name); err != nil {
			note("name not changed: %v", err)
		} else {
			detail += fmt.Sprintf(", name %q → %q", was, p.Meta.Name)
		}
	}
	if err := p.WriteRoleFile(); err != nil {
		note("AGENTS.md not rewritten: %v", err)
	}
	return res, p.Journal(o.Caller, "project.rename", o.To, detail)
}

// under reports path's place below dir, when it is below it.
func under(dir, path string) (string, bool) {
	rel, err := filepath.Rel(dir, filepath.Clean(path))
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	return rel, true
}
