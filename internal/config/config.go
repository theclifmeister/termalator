// Package config reads ~/.termalator/config.toml, the human's settings
// (docs/SPEC.md §5.1, §11.2). This package covers the per-project safety
// settings under [projects.<slug>]; other sections (keys, default agent)
// belong to the packages that use them and are ignored here.
//
// tm never writes this file: changing safety settings is a human action,
// done by editing it. It lives outside every project folder, so a
// coordinator editing PROJECT.md can't touch it.
package config

import (
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"

	"github.com/BurntSushi/toml"
	"github.com/theclifmeister/termalator/internal/home"
)

// Values of start_threads.
const (
	StartPropose = "propose" // threads start only after the human's go-ahead
	StartAuto    = "auto"
)

// Safety is one project's resolved safety settings.
type Safety struct {
	StartThreads        string `json:"start_threads"`
	Yolo                bool   `json:"yolo"`
	CoordinatorApproves bool   `json:"coordinator_approves"`
	// AutoResolve lets the ticker resolve a thread once its PR merged and
	// its agent is idle (§9), under resolve's usual rules (never forced).
	AutoResolve bool `json:"auto_resolve"`
	// PRFollowup prompts a thread when its PR's checks fail or a reviewer
	// asks for changes (§7.5).
	PRFollowup bool `json:"pr_followup"`
	// CoordinatorRemoteControl starts the coordinator with the agent's
	// remote control on (e.g. Claude Code's Remote Control), named after
	// the project (§8.2 [remote_control]). The prefix key and tm project
	// remote change only the running session.
	CoordinatorRemoteControl bool `json:"coordinator_remote_control"`
}

// Defaults are the settings of a project that config.toml doesn't name.
var Defaults = Safety{StartThreads: StartPropose, Yolo: false, CoordinatorApproves: true, AutoResolve: true, PRFollowup: true}

type rawSafety struct {
	StartThreads        *string `toml:"start_threads"`
	Yolo                *bool   `toml:"yolo"`
	CoordinatorApproves *bool   `toml:"coordinator_approves"`
	AutoResolve         *bool   `toml:"auto_resolve"`
	PRFollowup          *bool   `toml:"pr_followup"`
	CoordinatorRC       *bool   `toml:"coordinator_remote_control"`
}

// Config is the parsed file.
type Config struct {
	Path     string
	projects map[string]rawSafety
}

// Path returns <home>/config.toml.
func Path() (string, error) {
	d, err := home.Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "config.toml"), nil
}

// Load reads config.toml. A missing file gives the defaults. Unknown keys
// inside a [projects.<slug>] table are errors, so a typo can't silently
// leave a safety setting at its default.
func Load() (*Config, error) {
	path, err := Path()
	if err != nil {
		return nil, err
	}
	var raw struct {
		Projects map[string]rawSafety `toml:"projects"`
	}
	md, err := toml.DecodeFile(path, &raw)
	if errors.Is(err, fs.ErrNotExist) {
		return &Config{Path: path}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	for _, k := range md.Undecoded() {
		if len(k) >= 3 && k[0] == "projects" {
			return nil, fmt.Errorf("%s: unknown setting %s", path, k.String())
		}
	}
	c := &Config{Path: path, projects: raw.Projects}
	for slug := range raw.Projects {
		if _, err := c.Safety(slug); err != nil {
			return nil, err
		}
	}
	return c, nil
}

// Safety returns a project's settings, defaults filled in.
func (c *Config) Safety(slug string) (Safety, error) {
	s := Defaults
	r, ok := c.projects[slug]
	if !ok {
		return s, nil
	}
	if r.StartThreads != nil {
		switch *r.StartThreads {
		case StartPropose, StartAuto:
			s.StartThreads = *r.StartThreads
		default:
			return s, fmt.Errorf("%s: projects.%s.start_threads must be %q or %q, not %q", c.Path, slug, StartPropose, StartAuto, *r.StartThreads)
		}
	}
	if r.Yolo != nil {
		s.Yolo = *r.Yolo
	}
	if r.CoordinatorApproves != nil {
		s.CoordinatorApproves = *r.CoordinatorApproves
	}
	if r.AutoResolve != nil {
		s.AutoResolve = *r.AutoResolve
	}
	if r.PRFollowup != nil {
		s.PRFollowup = *r.PRFollowup
	}
	if r.CoordinatorRC != nil {
		s.CoordinatorRemoteControl = *r.CoordinatorRC
	}
	return s, nil
}
