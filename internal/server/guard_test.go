package server

import (
	"encoding/json"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/theclifmeister/terminatr/internal/guard"
	"github.com/theclifmeister/terminatr/internal/proto"
)

// TestGuardRulesFor: a thread gets every rule, a coordinator all but
// the thread's own two, and the human's settings turn rules off.
func TestGuardRulesFor(t *testing.T) {
	testPaths(t)
	p := newWatchProject(t)
	s := &Server{log: log.New(os.Stderr, "", 0)}
	wt := t.TempDir()
	thread := SessionRecord{ID: "s-2", Role: proto.RoleThread, Agent: "claude", Project: p.Slug, Thread: "t-0001", Cwd: wt}
	g := s.guardRulesFor(thread)
	if !g.On || !slices.Equal(g.Rules, []string{"force-push", "push-default", "worktree-only", "delete-branch", "merge", "credentials"}) {
		t.Fatalf("thread %+v", g)
	}
	if g.Writable[0] != wt || g.Cwd != wt || !slices.Contains(g.Protected, "main") || len(g.Secrets) == 0 {
		t.Errorf("thread %+v", g)
	}
	// The session agent's tools, from its manifest's [guard.tools].
	if g.Tools["Bash"].Kind != guard.KindShell || g.Tools["Edit"].Kind != guard.KindWrite {
		t.Errorf("claude tools %+v", g.Tools)
	}
	if g := s.guardRulesFor(SessionRecord{ID: "s-4", Role: proto.RoleThread, Agent: "codex", Project: p.Slug, Cwd: wt}); g.Tools["apply_patch"].Kind != guard.KindPatch || g.Tools["Edit"].Kind != "" {
		t.Errorf("codex tools %+v", g.Tools)
	}
	coord := SessionRecord{ID: "s-1", Role: proto.RoleCoordinator, Project: p.Slug, Cwd: p.Dir}
	if g := s.guardRulesFor(coord); !slices.Equal(g.Rules, []string{"force-push", "push-default", "delete-branch", "credentials"}) || g.Writable != nil {
		t.Errorf("coordinator %+v", g)
	}
	if g := s.guardRulesFor(SessionRecord{ID: "s-3", Role: proto.RoleShell}); g.On {
		t.Errorf("shell %+v", g)
	}

	writeConfig(t, "[defaults]\nguard_off = [\"credentials\"]\n[projects."+p.Slug+"]\nmerge = \"thread\"\n")
	if g := s.guardRulesFor(thread); !slices.Equal(g.Rules, []string{"force-push", "push-default", "worktree-only", "delete-branch"}) || g.Secrets == nil {
		t.Errorf("merge = thread, credentials off: %+v", g)
	}
	writeConfig(t, "[projects."+p.Slug+"]\nguard = false\n")
	if g := s.guardRulesFor(thread); g.On || g.Rules != nil {
		t.Errorf("guard = false: %+v", g)
	}
	// A broken file leaves the guard on.
	writeConfig(t, "[projects."+p.Slug+"]\nguard = \"maybe\"\n")
	if g := s.guardRulesFor(thread); !g.On {
		t.Errorf("broken config: %+v", g)
	}
}

func writeConfig(t *testing.T, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(os.Getenv("TERMINATR_HOME"), "config.toml"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestGuardRoutes: the mod fetches its rules over its socket, and each
// refusal it reports is journaled, and a session refused three times in
// ten minutes files one inbox item.
func TestGuardRoutes(t *testing.T) {
	testPaths(t)
	p := newWatchProject(t)
	rt, err := os.MkdirTemp("/tmp", "tmguard")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(rt) })
	s := &Server{log: log.New(os.Stderr, "", 0), records: map[string]SessionRecord{}}
	s.mu.Lock()
	sock, err := s.listenMod("s-2", rt)
	s.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.closeMods)
	c := modClient(sock)

	resp, err := c.Get("http://terminatr/v1/rules")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusGone {
		t.Fatalf("no record: %d", resp.StatusCode)
	}
	s.mu.Lock()
	s.records["s-2"] = SessionRecord{ID: "s-2", Role: proto.RoleThread, Agent: "claude", Project: p.Slug, Thread: "t-0001", Cwd: rt}
	s.mu.Unlock()
	resp, err = c.Get("http://terminatr/v1/rules")
	if err != nil {
		t.Fatal(err)
	}
	var g GuardRules
	err = json.NewDecoder(resp.Body).Decode(&g)
	resp.Body.Close()
	if err != nil || !g.On || !slices.Contains(g.Rules, "merge") || g.Tools["Bash"].Kind != guard.KindShell {
		t.Fatalf("rules %+v, %v", g, err)
	}

	if code := postMod(t, c, "/v1/denied", `{"rule":"nope","tool":"Bash"}`); code != http.StatusBadRequest {
		t.Errorf("unknown rule: %d", code)
	}
	clock := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	guardNow = func() time.Time { return clock }
	t.Cleanup(func() { guardNow = time.Now })
	deny := func(rule string) {
		t.Helper()
		body := `{"rule":"` + rule + `","tool":"Bash","summary":"gh pr merge\n42"}`
		if code := postMod(t, c, "/v1/denied", body); code != http.StatusNoContent {
			t.Fatalf("denied: %d", code)
		}
	}
	guards := func() int {
		items, err := p.Inbox()
		if err != nil {
			t.Fatal(err)
		}
		n := 0
		for _, it := range items {
			if it.Kind == "guard" {
				n++
			}
		}
		return n
	}
	// Two refusals are journaled only; the third within ten minutes
	// raises one item, and the count starts again.
	deny("merge")
	clock = clock.Add(4 * time.Minute)
	deny("force-push")
	if n := guards(); n != 0 {
		t.Fatalf("after 2 refusals: %d inbox items", n)
	}
	clock = clock.Add(4 * time.Minute)
	deny("merge")
	if n := guards(); n != 1 {
		t.Fatalf("after 3 refusals: %d inbox items", n)
	}
	// Three spread over more than ten minutes raise nothing.
	for range 3 {
		clock = clock.Add(6 * time.Minute)
		deny("merge")
	}
	if n := guards(); n != 1 {
		t.Fatalf("slow refusals: %d inbox items", n)
	}
	lines, _, _ := p.JournalTail(20)
	n := 0
	for _, l := range lines {
		if strings.Contains(l, "t-0001 guard.deny t-0001 merge Bash: gh pr merge 42") {
			n++
		}
	}
	if n != 5 {
		t.Errorf("journal %q", lines)
	}
}

// TestHookGuard: without a mod, the server judges the tool calls a
// session's hooks report against its rules and records each refusal as
// POST /v1/denied does, but not an ask; with a mod there is no hook
// guard.
func TestHookGuard(t *testing.T) {
	testPaths(t)
	p := newWatchProject(t)
	wt := t.TempDir()
	s := &Server{log: log.New(os.Stderr, "", 0), records: map[string]SessionRecord{}}
	if s.hookGuardOf("s-2", true) != nil {
		t.Fatal("a mod session got a hook guard")
	}
	judge := s.hookGuardOf("s-2", false)
	if d := judge("Bash", map[string]any{"command": "gh pr merge 1"}); d != nil {
		t.Fatalf("no record: %+v", d)
	}
	s.records["s-2"] = SessionRecord{ID: "s-2", Role: proto.RoleThread, Agent: "codex", Project: p.Slug, Thread: "t-0001", Cwd: wt}
	clock := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	guardNow = func() time.Time { return clock }
	t.Cleanup(func() { guardNow = time.Now })

	d := judge("Bash", map[string]any{"command": "cd x && gh pr merge 1 --squash"})
	if d == nil || d.Rule != "merge" || !strings.HasPrefix(d.Message, "terminatr guard (merge): ") {
		t.Fatalf("merge: %+v", d)
	}
	if d := judge("apply_patch", map[string]any{"command": "*** Begin Patch\n*** Add File: x.go\n+package x\n*** End Patch"}); d != nil {
		t.Errorf("patch in the worktree: %+v", d)
	}
	if d := judge("apply_patch", map[string]any{"command": "*** Begin Patch\n*** Update File: /etc/hosts\n*** End Patch"}); d == nil || d.Rule != "worktree-only" {
		t.Errorf("patch outside the worktree: %+v", d)
	}
	if d := judge("Bash", map[string]any{"command": "git push -u origin tm/x"}); d != nil {
		t.Errorf("own branch: %+v", d)
	}
	// Claude's PowerShell tool: a command the guard can't read is asked,
	// and an ask is not journaled.
	s.records["s-3"] = SessionRecord{ID: "s-3", Role: proto.RoleThread, Agent: "claude", Project: p.Slug, Thread: "t-0001", Cwd: wt}
	if d := s.hookGuardOf("s-3", false)("PowerShell", map[string]any{"command": "iex $line"}); d == nil || !d.Ask || d.Rule != "unknown" {
		t.Errorf("PowerShell iex: %+v", d)
	}
	lines, _, _ := p.JournalTail(20)
	var got []string
	for _, l := range lines {
		if strings.Contains(l, "guard.deny") {
			got = append(got, l[strings.Index(l, "guard.deny"):])
		}
	}
	want := []string{"guard.deny t-0001 merge Bash: gh pr merge", "guard.deny t-0001 worktree-only apply_patch: apply_patch of /etc/hosts"}
	if !slices.Equal(got, want) {
		t.Errorf("journal %q, want %q", got, want)
	}

	// The rules are kept a minute: the human's settings reach the
	// session's hooks after that.
	writeConfig(t, "[projects."+p.Slug+"]\nguard_off = [\"merge\"]\n")
	if judge("Bash", map[string]any{"command": "gh pr merge 1"}) == nil {
		t.Error("rules changed within the minute")
	}
	clock = clock.Add(hookRulesFor)
	if d := judge("Bash", map[string]any{"command": "gh pr merge 1"}); d != nil {
		t.Errorf("merge turned off: %+v", d)
	}
	// The third refusal within ten minutes filed one inbox item.
	items, _ := p.Inbox()
	n := 0
	for _, it := range items {
		if it.Kind == "guard" {
			n++
		}
	}
	if n != 1 {
		t.Errorf("%d guard inbox items", n)
	}
}

// TestAccessMergeCommands: coordinator_merges (off by default) allows the
// coordinator's PR merge commands, only while merge = coordinator, and a
// thread never gets them.
func TestAccessMergeCommands(t *testing.T) {
	testPaths(t)
	p := newWatchProject(t)
	s := &Server{opts: Options{Paths: Paths{Home: os.Getenv("TERMINATR_HOME")}}}
	cmds := func(role string) []string {
		a, err := s.accessFor(role, p.Slug, t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		return a.Commands
	}
	if got := cmds(proto.RoleCoordinator); got != nil {
		t.Errorf("default: coordinator commands %q", got)
	}
	want := []string{"gh pr merge", "az repos pr update"}
	for _, body := range []string{
		"[projects." + p.Slug + "]\ncoordinator_merges = true\n",
		"[defaults]\ncoordinator_merges = true\n",
	} {
		writeConfig(t, body)
		if got := cmds(proto.RoleCoordinator); !slices.Equal(got, want) {
			t.Errorf("%q: coordinator commands %q, want %q", body, got, want)
		}
		if got := cmds(proto.RoleThread); got != nil {
			t.Errorf("%q: thread commands %q", body, got)
		}
	}
	writeConfig(t, "[projects."+p.Slug+"]\ncoordinator_merges = true\nmerge = \"thread\"\n")
	if got := cmds(proto.RoleCoordinator); got != nil {
		t.Errorf("merge = thread: coordinator commands %q", got)
	}
	writeConfig(t, "[defaults]\ncoordinator_merges = true\n[projects."+p.Slug+"]\ncoordinator_merges = false\n")
	if got := cmds(proto.RoleCoordinator); got != nil {
		t.Errorf("project off: coordinator commands %q", got)
	}
}

// TestAccessThreadGitDirs: a thread in a linked worktree may write the
// main repo's git dir and, by name, the worktree's own git dir below it.
func TestAccessThreadGitDirs(t *testing.T) {
	testPaths(t)
	p := newWatchProject(t)
	s := &Server{opts: Options{Paths: Paths{Home: os.Getenv("TERMINATR_HOME")}}}
	root := realPath(t.TempDir())
	repo, wt := filepath.Join(root, "repo"), filepath.Join(root, "wt")
	for _, args := range [][]string{
		{"init", "-q", repo},
		{"-C", repo, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "--allow-empty", "-m", "c"},
		{"-C", repo, "worktree", "add", "-q", "-b", "x", wt},
	} {
		if b, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, b)
		}
	}
	a, err := s.accessFor(proto.RoleThread, p.Slug, wt)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{filepath.Join(repo, ".git"), filepath.Join(repo, ".git", "worktrees", "wt")}
	if !slices.Equal(a.Write, want) {
		t.Errorf("worktree: write %q, want %q", a.Write, want)
	}
	if a, _ := s.accessFor(proto.RoleThread, p.Slug, repo); !slices.Equal(a.Write, want[:1]) {
		t.Errorf("main checkout: write %q, want %q", a.Write, want[:1])
	}
}

// TestGuardSecrets: the agents' paths come from their manifests. Codex's
// login is a credential store, in ~/.codex or $CODEX_HOME, and a Codex
// thread's hooks refuse reading it; every session keeps every agent's
// secrets.
func TestGuardSecrets(t *testing.T) {
	testPaths(t)
	p := newWatchProject(t)
	home, codexHome := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", codexHome)
	s := &Server{log: log.New(os.Stderr, "", 0), records: map[string]SessionRecord{}}
	rec := SessionRecord{ID: "s-2", Role: proto.RoleThread, Agent: "codex", Project: p.Slug, Thread: "t-0001", Cwd: t.TempDir()}
	g := s.guardRulesFor(rec)
	for _, want := range []string{filepath.Join(home, ".codex", "auth.json"), filepath.Join(codexHome, "auth.json"), filepath.Join(home, ".claude", ".credentials.json")} {
		if !slices.Contains(g.Secrets, want) {
			t.Errorf("secrets %q lack %s", g.Secrets, want)
		}
	}
	s.records["s-2"] = rec
	judge := s.hookGuardOf("s-2", false)
	if d := judge("Bash", map[string]any{"command": "cat " + filepath.Join(codexHome, "auth.json")}); d == nil || d.Rule != "credentials" {
		t.Errorf("cat of $CODEX_HOME/auth.json: %+v", d)
	}
	if d := judge("Bash", map[string]any{"command": "cat ~/.codex/auth.json"}); d == nil || d.Rule != "credentials" {
		t.Errorf("cat of ~/.codex/auth.json: %+v", d)
	}
	if d := judge("Bash", map[string]any{"command": "cat ~/.codex/config.toml"}); d != nil {
		t.Errorf("cat of ~/.codex/config.toml: %+v", d)
	}
	// The writable folders are the session agent's: Claude's plans and
	// memory for a Claude thread, none of them for a Codex thread.
	plans := filepath.Join(home, ".claude", "plans")
	if slices.Contains(g.Writable, plans) {
		t.Errorf("codex thread writable %q", g.Writable)
	}
	rec.Agent = "claude"
	if g := s.guardRulesFor(rec); !slices.Contains(g.Writable, plans) || !slices.Contains(g.Writable, filepath.Join(home, ".claude", "projects")) || !slices.Contains(g.Secrets, filepath.Join(codexHome, "auth.json")) {
		t.Errorf("claude thread %+v", g)
	}
}
