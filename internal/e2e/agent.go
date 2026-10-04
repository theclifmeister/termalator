package e2e

// Agent helpers (M3): the scripted fake agent (internal/e2e/fakeagent)
// runs under the real claude.toml with only launch.command swapped
// (docs/SPEC.md §16.3), so the manifest itself is under test.

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/theclifmeister/termalator/internal/agent"
	"github.com/theclifmeister/termalator/internal/proto"
)

// Setenv adds a variable to the environment of every later tm command,
// and so of a server they start and its sessions. Call it before the
// server starts.
func (e *Env) Setenv(key, value string) {
	e.Vars = append(e.Vars, key+"="+value)
}

// HomeDir is the test's HOME (the fake agent's ~/.claude lives here).
func (e *Env) HomeDir() string {
	for i := len(e.Vars) - 1; i >= 0; i-- {
		if v, ok := strings.CutPrefix(e.Vars[i], "HOME="); ok {
			return v
		}
	}
	return ""
}

var commandLine = regexp.MustCompile(`(?m)^command = "claude"$`)

// FakeClaude installs the built-in claude manifest as a user manifest
// with launch.command pointed at the fake agent. edit, if given, changes
// the manifest text further (e.g. drops a source). Keys the fake gets
// within its trust debounce are dropped, as with Claude; tests shorten it.
func (e *Env) FakeClaude(edit ...func(string) string) {
	e.T.Helper()
	b, ok := agent.Builtin("claude")
	if !ok {
		e.T.Fatal("no built-in claude manifest")
	}
	m := commandLine.ReplaceAllString(string(b), "command = "+strconv.Quote(filepath.Join(filepath.Dir(e.Bin), "fakeagent")))
	if m == string(b) {
		e.T.Fatal("claude.toml: no command line to replace")
	}
	for _, f := range edit {
		m = f(m)
	}
	dir := filepath.Join(e.Home, "agents")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		e.T.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "claude.toml"), []byte(m), 0o600); err != nil {
		e.T.Fatal(err)
	}
	e.Setenv("FAKEAGENT_TRUST_DEBOUNCE_MS", "100")
}

// Trust marks dir as trusted in the fake's ~/.claude.json, as accepting
// Claude's trust dialog once would.
func (e *Env) Trust(dirs ...string) {
	e.T.Helper()
	p := filepath.Join(e.HomeDir(), ".claude.json")
	cfg := map[string]any{}
	if b, err := os.ReadFile(p); err == nil {
		json.Unmarshal(b, &cfg)
	}
	projects, _ := cfg["projects"].(map[string]any)
	if projects == nil {
		projects = map[string]any{}
	}
	for _, d := range dirs {
		projects[d] = map[string]any{"hasTrustDialogAccepted": true}
		if r, err := filepath.EvalSymlinks(d); err == nil {
			projects[r] = map[string]any{"hasTrustDialogAccepted": true}
		}
	}
	cfg["projects"] = projects
	b, _ := json.Marshal(cfg)
	if err := os.WriteFile(p, b, 0o600); err != nil {
		e.T.Fatal(err)
	}
}

// Workdir returns a fresh directory for an agent session's cwd.
func (e *Env) Workdir() string {
	e.T.Helper()
	d := filepath.Join(e.T.TempDir(), "work")
	if err := os.MkdirAll(d, 0o755); err != nil {
		e.T.Fatal(err)
	}
	return d
}

// StartAgent starts an agent session (`tm session start --agent NAME`)
// in dir with a 100×30 pane; args are extra flags.
func (e *Env) StartAgent(name, dir string, args ...string) *Session {
	e.T.Helper()
	id := strings.TrimSpace(e.MustCLI(append([]string{"session", "start", "--agent", name, "--cwd", dir,
		"--cols", "100", "--rows", "30"}, args...)...))
	s := &Session{ID: id}
	if info, ok := e.Info(s); ok {
		s.PID = info.PID
	}
	e.track(s.PID, "session "+id)
	return s
}

// Info returns the server's view of one session.
func (e *Env) Info(s *Session) (proto.SessionInfo, bool) {
	e.T.Helper()
	for _, info := range e.Sessions() {
		if info.ID == s.ID {
			return info, true
		}
	}
	return proto.SessionInfo{}, false
}

// WaitState waits until the session's agent state is want ("idle",
// "blocked/permission", …: a state, or state/reason) and returns the
// session info. It fails the test with `tm agent explain` on timeout.
func (e *Env) WaitState(s *Session, want string, timeout time.Duration) proto.SessionInfo {
	e.T.Helper()
	var info proto.SessionInfo
	ok := Poll(timeout, func() bool {
		info, _ = e.Info(s)
		got := info.State
		if strings.Contains(want, "/") {
			got += "/" + info.Reason
		}
		return got == want
	})
	if !ok {
		e.T.Fatalf("session %s: state %s/%s (%s), want %s after %v\n%s\nscreen:\n%s", s.ID, info.State, info.Reason,
			info.StateSources, want, timeout, e.CLI("agent", "explain", s.ID).Stdout, e.CLI("session", "read", s.ID).Stdout)
	}
	return info
}

// Explain returns `tm agent explain --json` for a session.
func (e *Env) Explain(s *Session) agent.Explanation {
	e.T.Helper()
	var x agent.Explanation
	if err := json.Unmarshal([]byte(e.MustCLI("agent", "explain", s.ID, "--json")), &x); err != nil {
		e.T.Fatal(err)
	}
	return x
}

// Prompt sends a prompt with `tm session prompt` and returns its output.
func (e *Env) Prompt(s *Session, text string) string {
	e.T.Helper()
	return e.MustCLI("session", "prompt", s.ID, text)
}

func (e *Env) agentSessions() []string {
	r := e.CLI("session", "list", "--json")
	var list []proto.SessionInfo
	json.Unmarshal([]byte(r.Stdout), &list)
	var out []string
	for _, s := range list {
		if s.Agent != "" {
			out = append(out, s.ID)
		}
	}
	return out
}

// FakeLog is the fake agent's log file (FAKEAGENT_LOG's default).
func (e *Env) FakeLog() string { return filepath.Join(e.HomeDir(), ".fakeagent", "log.jsonl") }

// FakeRecord is one line of the fake agent's log.
type FakeRecord map[string]any

// Str returns a string field.
func (r FakeRecord) Str(k string) string { s, _ := r[k].(string); return s }

// FakeRecords returns the fake agent's log records of one kind ("" for
// all).
func (e *Env) FakeRecords(kind string) []FakeRecord {
	f, err := os.Open(e.FakeLog())
	if err != nil {
		return nil
	}
	defer f.Close()
	var out []FakeRecord
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	for sc.Scan() {
		var r FakeRecord
		if json.Unmarshal(sc.Bytes(), &r) == nil && (kind == "" || r.Str("kind") == kind) {
			out = append(out, r)
		}
	}
	return out
}

// WaitFake waits for a fake-agent log record of kind for which match
// returns true, and returns it.
func (e *Env) WaitFake(kind string, timeout time.Duration, match func(FakeRecord) bool) FakeRecord {
	e.T.Helper()
	var found FakeRecord
	if !Poll(timeout, func() bool {
		for _, r := range e.FakeRecords(kind) {
			if match == nil || match(r) {
				found = r
				return true
			}
		}
		return false
	}) {
		e.T.Fatalf("no fake agent %q record after %v", kind, timeout)
	}
	return found
}

// HookEvents lists the hook events the fake agent fired, in order.
func (e *Env) HookEvents() []string {
	var out []string
	for _, r := range e.FakeRecords("hook") {
		out = append(out, r.Str("event"))
	}
	return out
}
