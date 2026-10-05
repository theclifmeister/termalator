// Package config reads and writes ~/.termilator/config.toml, the human's
// settings (docs/SPEC.md §5.1, §11.2): the per-project safety settings
// under [projects.<slug>] and the default agent; the prefix key ([keys])
// and the icon set ([ui] icons) belong to the TUI, which reads them
// itself.
//
// Changing safety settings is a human action: tm writes this file only
// from the TUI's settings popups, on the human's keypress (write.go). It
// lives outside every project folder, so a coordinator editing
// PROJECT.md can't touch it, and coordinators get an Edit deny rule on it.
package config

import (
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"

	"github.com/BurntSushi/toml"
	"github.com/theclifmeister/termilator/internal/home"
)

// Values of start_threads.
const (
	StartPropose = "propose" // threads start only after the human's go-ahead
	StartAuto    = "auto"
)

// Values of auto_close: when the ticker closes (resolves) a finished
// thread (§9).
const (
	CloseOff    = "off"
	CloseMerged = "merged" // once its PR merged
	CloseDays   = "days"   // auto_close_days after it finished
)

// Limits of the number settings.
const (
	MaxParallelThreads = 99
	MaxAutoCloseDays   = 365
)

// Safety is one project's resolved safety settings.
type Safety struct {
	StartThreads        string `json:"start_threads"`
	Yolo                bool   `json:"yolo"`
	CoordinatorApproves bool   `json:"coordinator_approves"`
	// ParallelThreads is the most threads that may work at once: tm
	// thread start refuses beyond it without --over-cap (§9).
	ParallelThreads int `json:"parallel_threads"`
	// AutoClose is when the ticker resolves a finished thread whose agent
	// rests (§9): CloseOff, CloseMerged, or CloseDays after
	// AutoCloseDays. Resolve's usual rules hold (never forced), and a
	// thread with uncommitted or unpushed work is never closed.
	AutoClose     string `json:"auto_close"`
	AutoCloseDays int    `json:"auto_close_days"`
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
var Defaults = Safety{StartThreads: StartPropose, Yolo: false, CoordinatorApproves: true,
	ParallelThreads: 10, AutoClose: CloseMerged, AutoCloseDays: 7, PRFollowup: true}

type rawSafety struct {
	StartThreads        *string `toml:"start_threads"`
	Yolo                *bool   `toml:"yolo"`
	CoordinatorApproves *bool   `toml:"coordinator_approves"`
	ParallelThreads     *int    `toml:"parallel_threads"`
	AutoClose           *string `toml:"auto_close"`
	AutoCloseDays       *int    `toml:"auto_close_days"`
	// AutoResolve is auto_close's older form: true is "merged", false
	// "off"; auto_close wins when both are set.
	AutoResolve   *bool `toml:"auto_resolve"`
	PRFollowup    *bool `toml:"pr_followup"`
	CoordinatorRC *bool `toml:"coordinator_remote_control"`
}

// Config is the parsed file.
type Config struct {
	Path     string
	projects map[string]rawSafety
	agent    string
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
		DefaultAgent string               `toml:"default_agent"`
		Projects     map[string]rawSafety `toml:"projects"`
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
	c := &Config{Path: path, projects: raw.Projects, agent: raw.DefaultAgent}
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
	if r.ParallelThreads != nil {
		if n := *r.ParallelThreads; n < 1 || n > MaxParallelThreads {
			return s, fmt.Errorf("%s: projects.%s.parallel_threads must be 1 to %d, not %d", c.Path, slug, MaxParallelThreads, n)
		}
		s.ParallelThreads = *r.ParallelThreads
	}
	if r.AutoResolve != nil && !*r.AutoResolve {
		s.AutoClose = CloseOff
	}
	if r.AutoClose != nil {
		switch *r.AutoClose {
		case CloseOff, CloseMerged, CloseDays:
			s.AutoClose = *r.AutoClose
		default:
			return s, fmt.Errorf("%s: projects.%s.auto_close must be %q, %q or %q, not %q", c.Path, slug, CloseOff, CloseMerged, CloseDays, *r.AutoClose)
		}
	}
	if r.AutoCloseDays != nil {
		if n := *r.AutoCloseDays; n < 1 || n > MaxAutoCloseDays {
			return s, fmt.Errorf("%s: projects.%s.auto_close_days must be 1 to %d, not %d", c.Path, slug, MaxAutoCloseDays, n)
		}
		s.AutoCloseDays = *r.AutoCloseDays
	}
	if r.PRFollowup != nil {
		s.PRFollowup = *r.PRFollowup
	}
	if r.CoordinatorRC != nil {
		s.CoordinatorRemoteControl = *r.CoordinatorRC
	}
	return s, nil
}

// DefaultAgent is the agent new coordinators run (default_agent), or
// fallback when the file doesn't set one.
func (c *Config) DefaultAgent(fallback string) string {
	if c == nil || c.agent == "" {
		return fallback
	}
	return c.agent
}

// DefaultAgent reads the default agent from the file; fallback when it
// is unset or the file can't be read.
func DefaultAgent(fallback string) string {
	c, err := Load()
	if err != nil {
		return fallback
	}
	return c.DefaultAgent(fallback)
}
