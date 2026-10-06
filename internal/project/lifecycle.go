package project

// A project's lifecycle (docs/SPEC.md §5.1, §11.2): pausing, archiving
// and deleting it are the human's. Paused and archived are settings in
// config.toml, out of the coordinator's reach; delete moves the folder
// to <home>/.trash/.

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/theclifmeister/terminatr/internal/caller"
	"github.com/theclifmeister/terminatr/internal/config"
	"github.com/theclifmeister/terminatr/internal/home"
)

// SetFlag sets the project's "paused" or "archived" setting and journals
// the change (project.pause, project.resume, project.archive,
// project.unarchive). changed is false when it was already so.
func (p *Project) SetFlag(c caller.Caller, key string, on bool) (changed bool, err error) {
	cfg, err := config.Load()
	if err != nil {
		return false, err
	}
	s, err := cfg.Safety(p.Slug)
	if err != nil {
		return false, err
	}
	var action string
	switch key {
	case "paused":
		if s.Paused == on {
			return false, nil
		}
		action = map[bool]string{true: "project.pause", false: "project.resume"}[on]
	case "archived":
		if s.Archived == on {
			return false, nil
		}
		action = map[bool]string{true: "project.archive", false: "project.unarchive"}[on]
	default:
		return false, fmt.Errorf("unknown project flag %q", key)
	}
	if err := config.SetProject(p.Slug, key, on); err != nil {
		return false, err
	}
	return true, p.Journal(c, action, p.Slug, "")
}

// Trash moves the project's folder to <home>/.trash/<slug>-<UTC time>
// and returns where it went. Its threads' worktrees stay where they are
// (tm doctor lists them as leftovers), and its archived and paused
// settings are dropped, so a new project of the slug starts without them.
func (p *Project) Trash(c caller.Caller, now time.Time) (string, error) {
	trash, err := home.TrashDir()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(trash, 0o700); err != nil {
		return "", err
	}
	base := filepath.Join(trash, p.Slug+"-"+now.UTC().Format("20060102T150405Z"))
	dst := base
	for i := 2; ; i++ {
		if _, err := os.Lstat(dst); os.IsNotExist(err) {
			break
		}
		dst = fmt.Sprintf("%s-%d", base, i)
	}
	// The journal line goes in first, so it travels with the folder.
	if err := p.Journal(c, "project.delete", p.Slug, "moved to "+dst); err != nil {
		return "", err
	}
	if err := os.Rename(p.Dir, dst); err != nil {
		return "", err
	}
	if err := config.ClearProject(p.Slug, "archived", "paused"); err != nil {
		return dst, fmt.Errorf("moved to %s, but its settings stay: %w", dst, err)
	}
	return dst, nil
}
