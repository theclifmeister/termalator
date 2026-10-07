package server

import (
	"encoding/json"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/theclifmeister/terminatr/internal/proto"
)

// TestGuardRulesFor: a thread gets every rule, a coordinator all but
// the thread's own two, and the human's settings turn rules off.
func TestGuardRulesFor(t *testing.T) {
	testPaths(t)
	p := newWatchProject(t)
	s := &Server{log: log.New(os.Stderr, "", 0)}
	wt := t.TempDir()
	thread := SessionRecord{ID: "s-2", Role: proto.RoleThread, Project: p.Slug, Thread: "t-0001", Cwd: wt}
	g := s.guardRulesFor(thread)
	if !g.On || !slices.Equal(g.Rules, []string{"force-push", "push-default", "worktree-only", "delete-branch", "merge", "credentials"}) {
		t.Fatalf("thread %+v", g)
	}
	if g.Writable[0] != wt || g.Cwd != wt || !slices.Contains(g.Protected, "main") || len(g.Secrets) == 0 {
		t.Errorf("thread %+v", g)
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
	s.records["s-2"] = SessionRecord{ID: "s-2", Role: proto.RoleThread, Project: p.Slug, Thread: "t-0001", Cwd: rt}
	s.mu.Unlock()
	resp, err = c.Get("http://terminatr/v1/rules")
	if err != nil {
		t.Fatal(err)
	}
	var g GuardRules
	err = json.NewDecoder(resp.Body).Decode(&g)
	resp.Body.Close()
	if err != nil || !g.On || !slices.Contains(g.Rules, "merge") {
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
