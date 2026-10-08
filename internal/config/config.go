// Package config reads and writes ~/.terminatr/config.toml, the human's
// settings (docs/SPEC.md §5.1, §11.2): the per-project safety settings
// under [projects.<slug>], the all-projects ones under [defaults] that a
// project follows for every key it doesn't set, the default agent, and
// the TUI's prefix key ([keys] prefix) and icon set ([ui] icons), whose
// values the TUI checks, and whether sessions load terminatr's mod
// ([mods] enabled, docs/SPEC.md §8.6).
//
// Changing safety settings is a human action: tm writes this file only
// from the TUI's settings popups, on the human's keypress (write.go). It
// lives outside every project folder, so a coordinator editing
// PROJECT.md can't touch it, and coordinators get an Edit deny rule on it.
package config

import (
	"cmp"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/theclifmeister/terminatr/internal/home"
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

// Values of complete_tasks: when a task is done (§6.4, §7.5).
// CompleteUser leaves it to the user; CompleteMerged is the user's
// standing acceptance, applied by the ticker to a task in review once
// its thread's pull request merged.
const (
	CompleteUser   = "user"
	CompleteMerged = "merged" // its PR merged
	// CompleteRemoved is the former "when released" (a tag contains the
	// PR's merge commit), dropped as too specific to one release flow.
	// It is read as CompleteUser, so nothing completes silently, and tm
	// doctor warns until the user picks again (Removed).
	CompleteRemoved = "released"
)

// Values of merge: who merges a thread's pull request. The mod's guard
// (docs/SPEC.md §8.6, Guard) refuses a thread's `gh pr merge` (or the Azure DevOps equivalent) under
// MergeCoordinator.
const (
	MergeCoordinator = "coordinator"
	MergeThread      = "thread"
)

// GuardRules are the ids of the mod's guard rules (docs/SPEC.md §8.6,
// Guard), which guard_off may name.
var GuardRules = []string{"force-push", "push-default", "worktree-only", "delete-branch", "merge", "credentials"}

// DefaultContextHint is [ui] context_hint when unset: the percent of
// its model's context window at which a coordinator is told to consider
// /clear.
const DefaultContextHint = 40

// Limits of the number settings.
const (
	MaxParallelThreads = 99
	MaxAutoCloseDays   = 365
	MaxArchiveDays     = 365
	// MinPRPollSeconds and MaxPRPollSeconds bound pr_poll_seconds.
	MinPRPollSeconds     = 30
	MaxPRPollSeconds     = 3600
	DefaultPRPollSeconds = 120
)

// ArchiveKeys are the retention settings (docs/SPEC.md §7.6): how many
// days the ticker keeps done tasks on the board, resolved threads'
// folders, handled inbox items as loose files, and lines in JOURNAL.md
// before it moves them to their archives.
var ArchiveKeys = []string{"archive_tasks_days", "archive_threads_days", "archive_inbox_days", "archive_journal_days"}

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
	// PRPollSeconds is how often the ticker asks the PR host about each open
	// thread's PR, and fetches the repos (§7.5).
	PRPollSeconds int `json:"pr_poll_seconds"`
	// CompleteTasks is when a task is done: CompleteUser (only on the
	// user's word) or CompleteMerged (the ticker marks a task in review
	// done once its thread's PR merged).
	CompleteTasks string `json:"complete_tasks"`
	// CoordinatorRemoteControl starts the coordinator with the agent's
	// remote control on (e.g. Claude Code's Remote Control), named after
	// the project (§8.2 [remote_control]). The prefix key and tm project
	// remote change only the running session.
	CoordinatorRemoteControl bool `json:"coordinator_remote_control"`
	// AutoClear lets the ticker clear the coordinator's conversation once
	// its context reaches [ui] context_hint, only while nothing waits on
	// it (§7.5): the project's state lives in files, so nothing is lost.
	AutoClear bool `json:"auto_clear"`
	// CoordinatorMerges adds a permission allow rule for the PR merge
	// command (gh pr merge; the Azure DevOps equivalent) to the
	// coordinator's launch settings only, so an auto-mode classifier that
	// doesn't see the standing merge rule stops refusing it. Threads never
	// get it, and the guard still refuses their merges. Only effective
	// with Merge = MergeCoordinator.
	CoordinatorMerges bool `json:"coordinator_merges"`
	// FastForwardCheckout lets the ticker fast-forward the user's own
	// checkout of a project repo to origin's default branch when that
	// branch is checked out, clean and only behind (§7.5).
	FastForwardCheckout bool `json:"fast_forward_checkout"`
	// Models is the allow-list of the models the coordinator may pick for
	// a thread (tm thread start --model, §8.2): names from the agents'
	// manifests. nil allows every model the manifest lists.
	Models []string `json:"models,omitempty"`
	// Paused stops the ticker's prompts (nudges, PR follow-up) and new
	// threads of the project; state polling goes on (§7.5, §11.2).
	Paused bool `json:"paused"`
	// Archived hides the project from the sidebar and
	// stops all ticker work for it (§5.1).
	Archived bool `json:"archived"`
	// Merge is who merges a thread's PR: MergeCoordinator or MergeThread.
	Merge string `json:"merge"`
	// Guard turns the mod's guard on (docs/SPEC.md §8.6, Guard): it
	// refuses tool calls that break the standing rules. GuardOff are the
	// GuardRules it leaves out. Only the human sets them, here.
	Guard    bool     `json:"guard"`
	GuardOff []string `json:"guard_off,omitempty"`
	// The retention ages in days (ArchiveKeys): after them the ticker
	// moves done tasks to the task archive, packs resolved threads'
	// folders, and bundles handled inbox items and journal lines into
	// compressed monthly files.
	ArchiveTasksDays   int `json:"archive_tasks_days"`
	ArchiveThreadsDays int `json:"archive_threads_days"`
	ArchiveInboxDays   int `json:"archive_inbox_days"`
	ArchiveJournalDays int `json:"archive_journal_days"`
}

// ArchiveDays is the retention setting key names (ArchiveKeys), 0 for
// another key.
func (s Safety) ArchiveDays(key string) int {
	switch key {
	case "archive_tasks_days":
		return s.ArchiveTasksDays
	case "archive_threads_days":
		return s.ArchiveThreadsDays
	case "archive_inbox_days":
		return s.ArchiveInboxDays
	case "archive_journal_days":
		return s.ArchiveJournalDays
	}
	return 0
}

// SetArchiveDays sets the retention setting key names (ArchiveKeys).
func (s *Safety) SetArchiveDays(key string, n int) {
	switch key {
	case "archive_tasks_days":
		s.ArchiveTasksDays = n
	case "archive_threads_days":
		s.ArchiveThreadsDays = n
	case "archive_inbox_days":
		s.ArchiveInboxDays = n
	case "archive_journal_days":
		s.ArchiveJournalDays = n
	}
}

// Defaults are the settings of a project that neither its own table nor
// [defaults] (all projects) name.
var Defaults = Safety{StartThreads: StartPropose, Yolo: false, CoordinatorApproves: true,
	ParallelThreads: 10, AutoClose: CloseMerged, AutoCloseDays: 7, PRFollowup: true, PRPollSeconds: DefaultPRPollSeconds,
	CompleteTasks: CompleteUser, FastForwardCheckout: true, Merge: MergeCoordinator, Guard: true,
	ArchiveTasksDays: 30, ArchiveThreadsDays: 30, ArchiveInboxDays: 30, ArchiveJournalDays: 30}

type rawSafety struct {
	StartThreads        *string `toml:"start_threads"`
	Yolo                *bool   `toml:"yolo"`
	CoordinatorApproves *bool   `toml:"coordinator_approves"`
	ParallelThreads     *int    `toml:"parallel_threads"`
	AutoClose           *string `toml:"auto_close"`
	AutoCloseDays       *int    `toml:"auto_close_days"`
	// AutoResolve is auto_close's older form: true is "merged", false
	// "off"; auto_close wins when both are set.
	AutoResolve    *bool     `toml:"auto_resolve"`
	PRFollowup     *bool     `toml:"pr_followup"`
	PRPoll         *int      `toml:"pr_poll_seconds"`
	CompleteTasks  *string   `toml:"complete_tasks"`
	CoordinatorRC  *bool     `toml:"coordinator_remote_control"`
	AutoClear      *bool     `toml:"auto_clear"`
	CoordMerges    *bool     `toml:"coordinator_merges"`
	FastForward    *bool     `toml:"fast_forward_checkout"`
	Models         *[]string `toml:"models"`
	Paused         *bool     `toml:"paused"`
	Archived       *bool     `toml:"archived"`
	Merge          *string   `toml:"merge"`
	Guard          *bool     `toml:"guard"`
	GuardOff       *[]string `toml:"guard_off"`
	ArchiveTasks   *int      `toml:"archive_tasks_days"`
	ArchiveThreads *int      `toml:"archive_threads_days"`
	ArchiveInbox   *int      `toml:"archive_inbox_days"`
	ArchiveJournal *int      `toml:"archive_journal_days"`
}

// CheckModels checks a models allow-list's shape: at least one name,
// each one word, none twice. Whether an agent lists them is checked where
// they are used (AllowsModel's callers), since config doesn't read the
// manifests.
func CheckModels(names []string) error {
	if len(names) == 0 {
		return errors.New("must name at least one model (leave it out to allow every model)")
	}
	seen := map[string]bool{}
	for _, n := range names {
		if n == "" || strings.ContainsAny(n, " \t\n\r\"\\") {
			return fmt.Errorf("must be one-word model names, not %q", n)
		}
		if seen[n] {
			return fmt.Errorf("lists %q twice", n)
		}
		seen[n] = true
	}
	return nil
}

// AllowsModel reports whether the allow-list takes model; an empty
// (unset) list takes every model.
func (s Safety) AllowsModel(model string) bool {
	return len(s.Models) == 0 || slices.Contains(s.Models, model)
}

// Config is the parsed file.
type Config struct {
	Path string
	// Prefix is [keys] prefix, or the older [keys] detach that named the
	// same key; Icons is [ui] icons. "" when unset.
	Prefix, Icons string
	// Unknown are the keys under [keys], [ui] and [mods] that tm doesn't know
	// (e.g. "ui.icon"), sorted. tm doctor reports them; unlike a
	// project's, they don't fail Load, so a typo there never stops tm.
	Unknown []string
	// Mods is [mods] enabled: agent sessions load terminatr's mod, where
	// the agent has one and its version is new enough. Off by default
	// while the mods API is early access.
	Mods bool
	// ModsBand is [mods] band: with the mod loaded, it draws the band
	// above the prompt, its status entry and the CI toast. On unless set
	// false.
	ModsBand bool
	// ModsPane is [mods] pane: with the mod loaded, a coordinator session
	// opens its /tm dashboard pane by itself when it starts, where it
	// would dock as a sidebar. Off unless set true (T90: the TUI's info
	// panel shows the same beside the coordinator); /tm opens it anyway.
	ModsPane bool
	// ContextHint is [ui] context_hint: the percent of its model's
	// context window at which a coordinator is told to consider /clear
	// (DefaultContextHint when unset, 0 for never).
	ContextHint int
	projects    map[string]rawSafety
	// defaults is the [defaults] table: the all-projects settings.
	defaults rawSafety
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
// inside a [projects.<slug>] or the [defaults] table are errors, so a
// typo can't silently leave a safety setting at its default; those under
// [keys], [ui] and [mods] are listed in Unknown. With such a safety error, the
// Config is returned too, for its Prefix and Icons: a file that doesn't
// parse gives none.
func Load() (*Config, error) {
	path, err := Path()
	if err != nil {
		return nil, err
	}
	var raw struct {
		DefaultAgent string               `toml:"default_agent"`
		Projects     map[string]rawSafety `toml:"projects"`
		Defaults     rawSafety            `toml:"defaults"`
		Keys         struct {
			Prefix string `toml:"prefix"`
			Detach string `toml:"detach"`
		} `toml:"keys"`
		UI struct {
			Icons       string `toml:"icons"`
			ContextHint *int   `toml:"context_hint"`
		} `toml:"ui"`
		Mods struct {
			Enabled bool  `toml:"enabled"`
			Band    *bool `toml:"band"`
			Pane    *bool `toml:"pane"`
		} `toml:"mods"`
	}
	md, err := toml.DecodeFile(path, &raw)
	if errors.Is(err, fs.ErrNotExist) {
		return &Config{Path: path, ModsBand: true, ContextHint: DefaultContextHint}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	c := &Config{Path: path, projects: raw.Projects, defaults: raw.Defaults, agent: raw.DefaultAgent,
		Prefix: cmp.Or(raw.Keys.Prefix, raw.Keys.Detach), Icons: raw.UI.Icons, Mods: raw.Mods.Enabled,
		ModsBand: raw.Mods.Band == nil || *raw.Mods.Band, ModsPane: raw.Mods.Pane != nil && *raw.Mods.Pane,
		ContextHint: DefaultContextHint}
	if h := raw.UI.ContextHint; h != nil {
		if *h < 0 || *h > 100 {
			return c, fmt.Errorf("%s: ui.context_hint is a percent from 0 (never) to 100", path)
		}
		c.ContextHint = *h
	}
	for _, k := range md.Undecoded() {
		switch {
		case len(k) >= 3 && k[0] == "projects", len(k) >= 2 && k[0] == "defaults":
			return c, fmt.Errorf("%s: unknown setting %s", path, k.String())
		case len(k) >= 2 && (k[0] == "keys" || k[0] == "ui" || k[0] == "mods"):
			c.Unknown = append(c.Unknown, k.String())
		}
	}
	sort.Strings(c.Unknown)
	if _, err := c.AllProjects(); err != nil {
		return c, err
	}
	for slug := range raw.Projects {
		if _, err := c.Safety(slug); err != nil {
			return c, err
		}
	}
	return c, nil
}

// Safety returns a project's settings: each key the project's own
// value, else the all-projects one ([defaults]), else Defaults.
func (c *Config) Safety(slug string) (Safety, error) {
	s, err := c.AllProjects()
	if err != nil {
		return s, err
	}
	r, ok := c.projects[slug]
	if !ok {
		return s, nil
	}
	return s, r.apply(&s, c.Path, "projects."+slug)
}

// AllProjects returns the all-projects settings ([defaults]), Defaults
// filled in: what a project follows for each key it doesn't set.
func (c *Config) AllProjects() (Safety, error) {
	s := Defaults
	if c == nil {
		return s, nil
	}
	// Pausing or archiving is a project's own state: in [defaults] it
	// would stop or hide every project.
	if c.defaults.Paused != nil || c.defaults.Archived != nil {
		return s, fmt.Errorf("%s: defaults can't set paused or archived; they are each project's own", c.Path)
	}
	return s, c.defaults.apply(&s, c.Path, "defaults")
}

// Own lists the keys slug's table sets itself (ProjectKeys order),
// auto_resolve as auto_close; the rest follow all projects.
func (c *Config) Own(slug string) []string {
	if c == nil {
		return nil
	}
	r, ok := c.projects[slug]
	if !ok {
		return nil
	}
	set := map[string]bool{
		"start_threads": r.StartThreads != nil, "yolo": r.Yolo != nil,
		"coordinator_approves": r.CoordinatorApproves != nil, "parallel_threads": r.ParallelThreads != nil,
		"auto_close": r.AutoClose != nil || r.AutoResolve != nil, "auto_close_days": r.AutoCloseDays != nil,
		"pr_followup": r.PRFollowup != nil, "pr_poll_seconds": r.PRPoll != nil, "complete_tasks": r.CompleteTasks != nil,
		"coordinator_remote_control": r.CoordinatorRC != nil, "auto_clear": r.AutoClear != nil, "coordinator_merges": r.CoordMerges != nil, "fast_forward_checkout": r.FastForward != nil,
		"models": r.Models != nil, "archive_tasks_days": r.ArchiveTasks != nil, "archive_threads_days": r.ArchiveThreads != nil,
		"archive_inbox_days": r.ArchiveInbox != nil, "archive_journal_days": r.ArchiveJournal != nil,
	}
	var out []string
	for _, k := range ProjectKeys {
		if set[k] {
			out = append(out, k)
		}
	}
	return out
}

// apply sets in s every setting r names, checked; table names it in
// errors ("projects.demo", "defaults").
func (r rawSafety) apply(s *Safety, path, table string) error {
	if r.StartThreads != nil {
		switch *r.StartThreads {
		case StartPropose, StartAuto:
			s.StartThreads = *r.StartThreads
		default:
			return fmt.Errorf("%s: %s.start_threads must be %q or %q, not %q", path, table, StartPropose, StartAuto, *r.StartThreads)
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
			return fmt.Errorf("%s: %s.parallel_threads must be 1 to %d, not %d", path, table, MaxParallelThreads, n)
		}
		s.ParallelThreads = *r.ParallelThreads
	}
	if r.AutoResolve != nil {
		s.AutoClose = map[bool]string{true: CloseMerged, false: CloseOff}[*r.AutoResolve]
	}
	if r.AutoClose != nil {
		switch *r.AutoClose {
		case CloseOff, CloseMerged, CloseDays:
			s.AutoClose = *r.AutoClose
		default:
			return fmt.Errorf("%s: %s.auto_close must be %q, %q or %q, not %q", path, table, CloseOff, CloseMerged, CloseDays, *r.AutoClose)
		}
	}
	if r.AutoCloseDays != nil {
		if n := *r.AutoCloseDays; n < 1 || n > MaxAutoCloseDays {
			return fmt.Errorf("%s: %s.auto_close_days must be 1 to %d, not %d", path, table, MaxAutoCloseDays, n)
		}
		s.AutoCloseDays = *r.AutoCloseDays
	}
	if r.PRFollowup != nil {
		s.PRFollowup = *r.PRFollowup
	}
	if r.PRPoll != nil {
		if n := *r.PRPoll; n < MinPRPollSeconds || n > MaxPRPollSeconds {
			return fmt.Errorf("%s: %s.pr_poll_seconds must be %d to %d, not %d", path, table, MinPRPollSeconds, MaxPRPollSeconds, n)
		}
		s.PRPollSeconds = *r.PRPoll
	}
	if r.CompleteTasks != nil {
		switch *r.CompleteTasks {
		case CompleteUser, CompleteMerged:
			s.CompleteTasks = *r.CompleteTasks
		case CompleteRemoved:
			s.CompleteTasks = CompleteUser
		default:
			return fmt.Errorf("%s: %s.complete_tasks must be %q or %q, not %q", path, table, CompleteUser, CompleteMerged, *r.CompleteTasks)
		}
	}
	if r.CoordinatorRC != nil {
		s.CoordinatorRemoteControl = *r.CoordinatorRC
	}
	if r.AutoClear != nil {
		s.AutoClear = *r.AutoClear
	}
	if r.CoordMerges != nil {
		s.CoordinatorMerges = *r.CoordMerges
	}
	if r.FastForward != nil {
		s.FastForwardCheckout = *r.FastForward
	}
	if r.Models != nil {
		if err := CheckModels(*r.Models); err != nil {
			return fmt.Errorf("%s: %s.models %w", path, table, err)
		}
		s.Models = slices.Clone(*r.Models)
	}
	if r.Paused != nil {
		s.Paused = *r.Paused
	}
	if r.Archived != nil {
		s.Archived = *r.Archived
	}
	if r.Merge != nil {
		switch *r.Merge {
		case MergeCoordinator, MergeThread:
			s.Merge = *r.Merge
		default:
			return fmt.Errorf("%s: %s.merge must be %q or %q, not %q", path, table, MergeCoordinator, MergeThread, *r.Merge)
		}
	}
	if r.Guard != nil {
		s.Guard = *r.Guard
	}
	if r.GuardOff != nil {
		for _, id := range *r.GuardOff {
			if !slices.Contains(GuardRules, id) {
				return fmt.Errorf("%s: %s.guard_off: unknown rule %q (rules: %s)", path, table, id, strings.Join(GuardRules, ", "))
			}
		}
		s.GuardOff = slices.Clone(*r.GuardOff)
	}
	for _, a := range []struct {
		key string
		v   *int
	}{{"archive_tasks_days", r.ArchiveTasks}, {"archive_threads_days", r.ArchiveThreads}, {"archive_inbox_days", r.ArchiveInbox}, {"archive_journal_days", r.ArchiveJournal}} {
		if a.v == nil {
			continue
		}
		if n := *a.v; n < 1 || n > MaxArchiveDays {
			return fmt.Errorf("%s: %s.%s must be 1 to %d, not %d", path, table, a.key, MaxArchiveDays, n)
		}
		s.SetArchiveDays(a.key, *a.v)
	}
	return nil
}

// Removed lists, sorted, the projects whose complete_tasks still names
// the removed "released" (CompleteRemoved), read as CompleteUser; all
// projects ([defaults]) is AllProjectsName, first.
func (c *Config) Removed() []string {
	var out []string
	for slug, r := range c.projects {
		if r.CompleteTasks != nil && *r.CompleteTasks == CompleteRemoved {
			out = append(out, slug)
		}
	}
	sort.Strings(out)
	if r := c.defaults; r.CompleteTasks != nil && *r.CompleteTasks == CompleteRemoved {
		out = append([]string{AllProjectsName}, out...)
	}
	return out
}

// AllProjectsName is how the UI and tm doctor name [defaults].
const AllProjectsName = "all projects"

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
