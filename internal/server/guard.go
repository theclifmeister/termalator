package server

// The mod's guard (docs/SPEC.md §8.6, Guard): the standing rules as
// checks on each tool call. The server decides which rules a session's
// mod enforces, from the human's settings (config.toml: guard,
// guard_off, merge) and the session's role, and serves them on the mod
// socket as GET /v1/rules; the mod matches each call itself
// (hooks/guard.ts) and reports a refusal with POST /v1/denied, which the
// server journals and puts in the project's inbox.

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
	"github.com/theclifmeister/terminatr/internal/project"
	"github.com/theclifmeister/terminatr/internal/proto"
	"github.com/theclifmeister/terminatr/internal/worktree"
)

// GuardRules is the body of GET /v1/rules: what the session's mod
// refuses. Off, the mod refuses nothing and the agent's own permission
// rules decide alone, as without the mod.
type GuardRules struct {
	On   bool   `json:"on"`
	Role string `json:"role,omitempty"`
	// Rules are the config.GuardRules ids in force.
	Rules []string `json:"rules,omitempty"`
	// Home is the user's home directory, for ~ in paths and commands.
	Home string `json:"home,omitempty"`
	// Writable are the folders the file tools may write in under
	// worktree-only: the thread's worktree first, then the temporary
	// folders and Claude's own (plans, memory). Each as given and
	// with its symlinks resolved.
	Writable []string `json:"writable,omitempty"`
	// Protected are the branches no push may target: the repos' default
	// branches, main and master.
	Protected []string `json:"protected,omitempty"`
	// Secrets are the files and folders no tool may read under
	// credentials.
	Secrets []string `json:"secrets,omitempty"`
}

// GuardDenial is the body of POST /v1/denied.
type GuardDenial struct {
	Rule    string `json:"rule"`
	Tool    string `json:"tool"`
	Summary string `json:"summary,omitempty"`
}

// guardInboxGap spaces a session's inbox items for one rule: a denial
// is always journaled, but an agent that keeps trying files one item.
const guardInboxGap = 10 * time.Minute

// guardSecrets are the credential stores, relative to home.
var guardSecrets = []string{
	".ssh", ".aws", ".gnupg", ".netrc", ".git-credentials", ".npmrc", ".pypirc",
	".config/gh", ".config/gcloud", ".azure", ".docker/config.json", ".kube",
	".claude/.credentials.json",
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
	g := GuardRules{On: true, Role: r.Role, Home: home}
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

// guardLast is when each session last filed an inbox item for a rule.
var guardLast = struct {
	sync.Mutex
	at map[string]time.Time
}{at: map[string]time.Time{}}

// guardDenied journals denial d of session r's mod and, at most once a
// guardInboxGap per session and rule, tells the coordinator in the
// inbox. The tool's own words never reach either: the summary is the
// mod's, cut short and folded to one line.
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
	key := r.ID + "/" + d.Rule
	now := time.Now()
	guardLast.Lock()
	fresh := now.Sub(guardLast.at[key]) >= guardInboxGap
	if fresh {
		guardLast.at[key] = now
	}
	guardLast.Unlock()
	if !fresh {
		return
	}
	msg := fmt.Sprintf("%s: the guard refused a %s call (%s): %s", ref, tool, d.Rule, summary)
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
