package server

// The mod's guard (docs/SPEC.md §8.6, Guard): the standing rules as
// checks on each tool call. The server decides which rules a session's
// mod enforces, from the human's settings (config.toml: guard,
// guard_off, merge) and the session's role, and serves them on the mod
// socket as GET /v1/rules; the mod matches each call itself
// (hooks/guard.ts) and reports a refusal with POST /v1/denied, which the
// server journals (and puts in the project's inbox only when a session is refused repeatedly).

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/theclifmeister/terminatr/internal/caller"
	"github.com/theclifmeister/terminatr/internal/config"
	"github.com/theclifmeister/terminatr/internal/guard"
	"github.com/theclifmeister/terminatr/internal/project"
	"github.com/theclifmeister/terminatr/internal/proto"
	"github.com/theclifmeister/terminatr/internal/worktree"
)

// GuardRules is the body of GET /v1/rules: what the session's mod
// refuses, and what the server judges a hook's tool call by
// (guard.Rules.Judge). Off, nothing is refused and the agent's own
// permission rules decide alone, as without the mod.
type GuardRules = guard.Rules

// GuardDenial is the body of POST /v1/denied.
type GuardDenial struct {
	Rule    string `json:"rule"`
	Tool    string `json:"tool"`
	Summary string `json:"summary,omitempty"`
}

// A refusal is journaled and shown on the thread's info panel, not put
// in the inbox: only a session refused guardInboxCount times within
// guardInboxWindow files one item, and the count starts again after it.
const (
	guardInboxCount  = 3
	guardInboxWindow = 10 * time.Minute
)

// guardNow is the clock of the inbox window (a variable for tests).
var guardNow = time.Now

// guardSecrets are the credential stores, relative to home.
var guardSecrets = []string{
	".ssh", ".aws", ".gnupg", ".netrc", ".git-credentials", ".npmrc", ".pypirc",
	".config/gh", ".config/gcloud", ".azure", ".azure-devops", ".docker/config.json", ".kube",
	".claude/.credentials.json", ".codex/auth.json",
}

// guardSettings is a project's resolved settings; a config.toml that
// doesn't load leaves the defaults, so the guard stays on.
var guardSettings = func(slug string) config.Safety {
	cfg, err := config.Load()
	if err != nil {
		return config.Defaults
	}
	s, err := cfg.Safety(slug)
	if err != nil {
		return config.Defaults
	}
	return s
}

// guardRulesFor is session record r's rules. Only a project's agents are
// guarded; a coordinator neither merges under a thread's rule nor keeps
// to a worktree.
func (s *Server) guardRulesFor(r SessionRecord) GuardRules {
	if r.Project == "" || (r.Role != proto.RoleThread && r.Role != proto.RoleCoordinator) {
		return GuardRules{}
	}
	set := guardSettings(r.Project)
	if !set.Guard {
		return GuardRules{}
	}
	home, _ := os.UserHomeDir()
	g := GuardRules{On: true, Role: r.Role, Home: home, Cwd: r.Cwd}
	if s.opts.Paths.Home != "" {
		g.Worktrees = realPath(filepath.Join(s.opts.Paths.Home, "worktrees"))
	}
	for _, id := range config.GuardRules {
		if slices.Contains(set.GuardOff, id) {
			continue
		}
		switch id {
		case "worktree-only":
			if r.Role != proto.RoleThread || r.Cwd == "" {
				continue
			}
		case "merge":
			if r.Role != proto.RoleThread || set.Merge != config.MergeCoordinator {
				continue
			}
		}
		g.Rules = append(g.Rules, id)
	}
	if slices.Contains(g.Rules, "worktree-only") {
		dirs := []string{r.Cwd, os.TempDir(), "/tmp", "/private/tmp", "/var/folders", "/private/var/folders"}
		if home != "" {
			dirs = append(dirs, filepath.Join(home, ".claude", "plans"), filepath.Join(home, ".claude", "projects"))
		}
		g.Writable = withReal(dirs)
	}
	g.Protected = []string{"main", "master"}
	repos := []string{r.Cwd}
	if r.Role == proto.RoleCoordinator {
		if p, err := project.Open(r.Project); err == nil {
			repos = p.Meta.Repos
		}
	}
	for _, repo := range repos {
		// origin's default branch only: a worktree's own HEAD is the
		// thread's branch.
		if b, ok := strings.CutPrefix(worktree.DefaultRef(repo), "origin/"); ok && !slices.Contains(g.Protected, b) {
			g.Protected = append(g.Protected, b)
		}
	}
	if home != "" {
		for _, p := range guardSecrets {
			g.Secrets = append(g.Secrets, filepath.Join(home, p))
		}
		// Codex keeps its login in $CODEX_HOME/auth.json, ~/.codex by default.
		if ch := os.Getenv("CODEX_HOME"); ch != "" {
			g.Secrets = append(g.Secrets, filepath.Join(ch, "auth.json"))
		}
		g.Secrets = withReal(g.Secrets)
	}
	return g
}

// withReal is paths, each followed by its symlinks resolved where that
// differs, without repeats.
func withReal(paths []string) []string {
	var out []string
	for _, p := range paths {
		if p == "" {
			continue
		}
		p = filepath.Clean(p)
		for _, q := range []string{p, realPath(p)} {
			if !slices.Contains(out, q) {
				out = append(out, q)
			}
		}
	}
	return out
}

// guardRoutes adds the guard's two routes to session id's mod handler.
func (s *Server) guardRoutes(mux *http.ServeMux, id string) {
	mux.HandleFunc("GET /v1/rules", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		rec, ok := s.records[id]
		s.mu.Unlock()
		if !ok {
			http.Error(w, "session "+id+" is gone", http.StatusGone)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(s.guardRulesFor(rec))
	})
	mux.HandleFunc("POST /v1/denied", func(w http.ResponseWriter, r *http.Request) {
		var d GuardDenial
		b, err := io.ReadAll(io.LimitReader(r.Body, maxModReport))
		if err == nil {
			err = json.Unmarshal(b, &d)
		}
		if err == nil && !slices.Contains(config.GuardRules, d.Rule) {
			err = fmt.Errorf("unknown rule %q", d.Rule)
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		s.mu.Lock()
		rec, ok := s.records[id]
		s.mu.Unlock()
		if !ok {
			http.Error(w, "session "+id+" is gone", http.StatusGone)
			return
		}
		s.guardDenied(rec, d)
		w.WriteHeader(http.StatusNoContent)
	})
}

// hookGuardOf judges the tool calls session id's hooks report against
// its rules (agent.HookEnv.Guard: agents without a mod, which answer
// PreToolUse with the refusal) and records a refusal as POST /v1/denied
// does. With a mod, the mod judges: nil.
func (s *Server) hookGuardOf(id string, mod bool) func(string, map[string]any) *guard.Denial {
	if mod {
		return nil
	}
	return func(tool string, input map[string]any) *guard.Denial {
		s.mu.Lock()
		rec, ok := s.records[id]
		s.mu.Unlock()
		if !ok {
			return nil
		}
		d := s.hookRules(rec).Judge(tool, input)
		if d != nil {
			s.guardDenied(rec, GuardDenial{Rule: d.Rule, Tool: tool, Summary: d.Summary})
		}
		return d
	}
}

// hookRulesFor is how long a session's rules are kept for its hooks: a
// tool call must not wait on config.toml and git each time, and a
// change of the human's settings still reaches running sessions.
const hookRulesFor = time.Minute

// hookRulesCache holds each session's rules for its hooks.
var hookRulesCache = struct {
	sync.Mutex
	m map[string]hookRulesEntry
}{m: map[string]hookRulesEntry{}}

type hookRulesEntry struct {
	rules GuardRules
	at    time.Time
}

// hookRules is session r's rules, worked out at most once per
// hookRulesFor.
func (s *Server) hookRules(r SessionRecord) GuardRules {
	now := guardNow()
	hookRulesCache.Lock()
	e, ok := hookRulesCache.m[r.ID]
	hookRulesCache.Unlock()
	if ok && now.Sub(e.at) < hookRulesFor {
		return e.rules
	}
	g := s.guardRulesFor(r)
	hookRulesCache.Lock()
	for id, old := range hookRulesCache.m {
		if now.Sub(old.at) >= hookRulesFor {
			delete(hookRulesCache.m, id)
		}
	}
	hookRulesCache.m[r.ID] = hookRulesEntry{rules: g, at: now}
	hookRulesCache.Unlock()
	return g
}

// guardRecent is when each session was last refused, within the window.
var guardRecent = struct {
	sync.Mutex
	at map[string][]time.Time
}{at: map[string][]time.Time{}}

// guardRepeated records a refusal of session id and says whether it is
// the guardInboxCount-th within guardInboxWindow, which starts the count
// again.
func guardRepeated(id string) bool {
	now := guardNow()
	guardRecent.Lock()
	defer guardRecent.Unlock()
	keep := guardRecent.at[id][:0]
	for _, t := range guardRecent.at[id] {
		if now.Sub(t) < guardInboxWindow {
			keep = append(keep, t)
		}
	}
	keep = append(keep, now)
	if len(keep) >= guardInboxCount {
		delete(guardRecent.at, id)
		return true
	}
	guardRecent.at[id] = keep
	return false
}

// guardDenied journals denial d of session r's mod; the thread's info
// panel shows it from there. Only when the session keeps being refused
// (guardInboxCount times in guardInboxWindow) does it tell the
// coordinator, with one inbox item. The tool's own words never reach
// either: the summary is the mod's, cut short and folded to one line.
func (s *Server) guardDenied(r SessionRecord, d GuardDenial) {
	s.log.Printf("session %s: guard denied %s (%s)", r.ID, d.Rule, d.Tool)
	if r.Project == "" {
		return
	}
	p, err := project.Open(r.Project)
	if err != nil {
		return
	}
	who := caller.Caller{Kind: caller.Coordinator, Project: r.Project}
	ref := r.ID
	if r.Role == proto.RoleThread && r.Thread != "" {
		who = caller.Caller{Kind: caller.Thread, Project: r.Project, Thread: r.Thread}
		ref = r.Thread
	}
	summary := oneLine(d.Summary, 120)
	tool := oneLine(d.Tool, 40)
	if err := p.Journal(who, "guard.deny", ref, fmt.Sprintf("%s %s: %s", d.Rule, tool, summary)); err != nil {
		s.log.Printf("session %s: journal: %v", r.ID, err)
	}
	if !guardRepeated(r.ID) {
		return
	}
	msg := fmt.Sprintf("%s: the guard refused %d calls in %d minutes, the last a %s call (%s): %s",
		ref, guardInboxCount, int(guardInboxWindow/time.Minute), tool, d.Rule, summary)
	if _, err := p.AddItem("guard", ref, msg, false); err != nil {
		s.log.Printf("session %s: inbox: %v", r.ID, err)
	}
}

// oneLine is s folded to one line of at most n runes.
func oneLine(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > n {
		s = string(r[:n-1]) + "…"
	}
	return s
}
