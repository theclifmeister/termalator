// Package e2e is termalator's end-to-end test harness (docs/SPEC.md §16.2).
// Scenarios run the real tm against an isolated server and look at what a
// user would see: a Window is a PTY whose output a libghostty-vt emulator
// parses, playing the user's terminal window.
//
//	func TestSmokeSomething(t *testing.T) {
//		env := e2e.New(t)                      // isolated home, short run dir, tm built once
//		s := env.Start("printer", "-lines", "3") // a session running a deterministic app
//		env.WaitFor(s, "ready", 5*time.Second)
//		w := env.Shell(80, 24)                 // a window running a shell
//		w.Type(`"$TM" session read ` + s.ID + "\r")
//		w.WaitFor("line 3", 5*time.Second)
//		w.CloseWindow()                        // the PTY master goes away
//		env.AssertAlive(s)
//		e2e.Golden(t, env.Screen(s), "printer.txt")
//	}
//
// Scenarios named TestSmoke* form the smoke set that runs on every PR
// (`make e2e-smoke`); `make e2e` runs them all. Both set E2E=1, without
// which scenarios skip. Golden screens live in
// testdata/golden/; `go test ./internal/e2e -update` rewrites them.
package e2e

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/theclifmeister/termalator/internal/proto"
)

var (
	buildOnce sync.Once
	binDir    string
	buildErr  error
)

// apps are the deterministic programs under internal/e2e/apps/ that
// sessions can run by name.
var apps = []string{"printer", "fullscreen", "termquery"}

// build compiles tm and the apps once per test process. E2E_RACE=1 builds
// tm with the race detector (make e2e-smoke-race, nightly in CI).
func build(t testing.TB) string {
	t.Helper()
	buildOnce.Do(func() {
		root, err := moduleRoot()
		if err != nil {
			buildErr = err
			return
		}
		if binDir, err = os.MkdirTemp("", binPrefix); err != nil {
			buildErr = err
			return
		}
		// Marks the dir as this run's for sweepStale (stale.go).
		if err = os.WriteFile(filepath.Join(binDir, ownerFile), []byte(strconv.Itoa(os.Getpid())), 0o644); err != nil {
			buildErr = err
			return
		}
		args := []string{"build", "-o", filepath.Join(binDir, "tm")}
		if os.Getenv("E2E_RACE") == "1" {
			args = append(args, "-race")
		}
		targets := [][]string{append(args, "./cmd/tm")}
		for _, a := range apps {
			targets = append(targets, []string{"build", "-o", filepath.Join(binDir, a), "./internal/e2e/apps/" + a})
		}
		targets = append(targets, []string{"build", "-o", filepath.Join(binDir, "fakeagent"), "./internal/e2e/fakeagent"})
		for _, tg := range targets {
			cmd := exec.Command("go", tg...)
			cmd.Dir = root
			if out, err := cmd.CombinedOutput(); err != nil {
				buildErr = fmt.Errorf("go %s: %v\n%s(run through `make e2e`, which sets up cgo)", strings.Join(tg, " "), err, out)
				return
			}
		}
	})
	if buildErr != nil {
		t.Fatal(buildErr)
	}
	return binDir
}

func moduleRoot() (string, error) {
	out, err := exec.Command("go", "env", "GOMOD").Output()
	if err != nil {
		return "", fmt.Errorf("go env GOMOD: %w", err)
	}
	mod := strings.TrimSpace(string(out))
	if mod == "" || mod == os.DevNull {
		return "", fmt.Errorf("not inside the termalator module")
	}
	return filepath.Dir(mod), nil
}

// DefaultTimeout bounds waits that take no explicit timeout.
var DefaultTimeout = 10 * time.Second

// Env is one isolated termalator installation: its own TERMALATOR_HOME,
// HOME and run dir, so its server never meets the user's. Cleanup stops
// the server, fails the test if any process it started outlives it, and
// saves artifacts when the test failed.
type Env struct {
	T      testing.TB
	Bin    string // the tm under test
	Home   string // TERMALATOR_HOME
	Socket string // TERMALATOR_SOCKET, in a short run dir under /tmp
	// AttachLog is where attach clients log digest checks and keys
	// (TERMALATOR_ATTACH_LOG).
	AttachLog string
	// Vars is the environment of every tm command and window.
	Vars []string

	mu      sync.Mutex
	pids    map[int]string // processes to check for orphans, with a label
	windows []*Window
}

// New sets up an isolated installation for one test. Scenarios run only
// with E2E=1 (`make e2e`, `make e2e-smoke`), so `go test ./...` stays fast
// and doesn't run them twice in CI.
func New(t testing.TB) *Env {
	t.Helper()
	if os.Getenv("E2E") != "1" {
		t.Skip("end-to-end scenario: run with make e2e or make e2e-smoke (E2E=1)")
	}
	dir := build(t)
	// macOS temp dirs are too long for a socket path; /tmp is short.
	runDir, err := os.MkdirTemp("/tmp", "tme2e")
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	e := &Env{
		T:      t,
		Bin:    filepath.Join(dir, "tm"),
		Home:   filepath.Join(root, "termalator"),
		Socket: filepath.Join(runDir, "tm.sock"),
		pids:   map[int]string{},
	}
	e.AttachLog = filepath.Join(root, "attach.log")
	home := filepath.Join(root, "home")
	os.MkdirAll(home, 0o700)
	e.Vars = append(cleanEnv(os.Environ()),
		"HOME="+home,
		"TERMALATOR_HOME="+e.Home,
		"TERMALATOR_SOCKET="+e.Socket,
		"PATH="+dir+":/usr/bin:/bin:/usr/sbin:/sbin",
		"LANG=C.UTF-8",
		"SHELL=/bin/sh",
		"PS1=$ ",
		"TM="+e.Bin,
		"TERMALATOR_ATTACH_LOG="+e.AttachLog,
		// No release checks against GitHub (tm doctor, tm update).
		"TERMALATOR_UPDATE_URL=off",
		// The server stops itself once this test process is gone, even
		// when a timeout or ^C skips the cleanup (docs/OPERATIONS.md).
		"TERMALATOR_TEST_OWNER="+strconv.Itoa(os.Getpid()),
	)
	t.Cleanup(func() {
		e.cleanup()
		os.RemoveAll(runDir)
	})
	return e
}

// cleanEnv drops variables that would point tm at the user's real server
// or make results depend on the user's shell and terminal.
func cleanEnv(env []string) []string {
	var out []string
	for _, kv := range env {
		k, _, _ := strings.Cut(kv, "=")
		switch {
		case strings.HasPrefix(k, "TERMALATOR"), strings.HasPrefix(k, "LC_"),
			k == "HOME", k == "PATH", k == "LANG", k == "PS1", k == "SHELL", k == "TMUX", k == "ENV":
			continue
		}
		out = append(out, kv)
	}
	return out
}

// track records a process that must be gone when the test ends.
func (e *Env) track(pid int, label string) {
	if pid <= 0 {
		return
	}
	e.mu.Lock()
	e.pids[pid] = label
	e.mu.Unlock()
}

// Result is a finished tm command.
type Result struct {
	Stdout, Stderr string
	Code           int
}

// CLI runs tm with args and returns its output and exit code.
func (e *Env) CLI(args ...string) Result {
	e.T.Helper()
	cmd := exec.Command(e.Bin, args...)
	cmd.Env = e.Vars
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	r := Result{Stdout: out.String(), Stderr: errb.String()}
	if ee, ok := err.(*exec.ExitError); ok {
		r.Code = ee.ExitCode()
	} else if err != nil {
		e.T.Fatalf("tm %s: %v", strings.Join(args, " "), err)
	}
	if pid := e.ServerPID(); pid > 0 {
		e.track(pid, "tm server")
	}
	return r
}

// MustCLI runs tm and fails the test unless it exits 0; it returns stdout.
func (e *Env) MustCLI(args ...string) string {
	e.T.Helper()
	r := e.CLI(args...)
	if r.Code != 0 {
		e.T.Fatalf("tm %s: exit %d\n%s%s", strings.Join(args, " "), r.Code, r.Stdout, r.Stderr)
	}
	return r.Stdout
}

// ServerPID reads the server's pid file (0 if there is none).
func (e *Env) ServerPID() int {
	b, err := os.ReadFile(filepath.Join(filepath.Dir(e.Socket), "server.pid"))
	if err != nil {
		return 0
	}
	pid, _ := strconv.Atoi(strings.TrimSpace(string(b)))
	return pid
}

// Session is a session started by the harness.
type Session struct {
	ID  string
	PID int
}

// Start starts a session in "/" with an 80×24 pane (auto-starting the
// server). app is "shell" for /bin/sh, the name of a deterministic app
// (internal/e2e/apps), or any command.
func (e *Env) Start(app string, args ...string) *Session {
	e.T.Helper()
	return e.StartSize(80, 24, app, args...)
}

// StartSize is Start with an explicit pane size.
func (e *Env) StartSize(cols, rows int, app string, args ...string) *Session {
	e.T.Helper()
	argv := append([]string{app}, args...)
	switch {
	case app == "shell":
		argv[0] = "/bin/sh"
	case isApp(app):
		argv[0] = filepath.Join(filepath.Dir(e.Bin), app)
	}
	id := strings.TrimSpace(e.MustCLI(append([]string{"session", "start", "--cwd", "/",
		"--cols", strconv.Itoa(cols), "--rows", strconv.Itoa(rows), "--"}, argv...)...))
	s := &Session{ID: id}
	for _, info := range e.Sessions() {
		if info.ID == id {
			s.PID = info.PID
		}
	}
	e.track(s.PID, "session "+id)
	return s
}

func isApp(name string) bool {
	for _, a := range apps {
		if a == name {
			return true
		}
	}
	return false
}

// Sessions lists the server's sessions.
func (e *Env) Sessions() []proto.SessionInfo {
	e.T.Helper()
	var list []proto.SessionInfo
	if err := json.Unmarshal([]byte(e.MustCLI("session", "list", "--json")), &list); err != nil {
		e.T.Fatalf("session list --json: %v", err)
	}
	return list
}

// Keys types raw text into a session through the server ("\r" is Enter).
func (e *Env) Keys(s *Session, text string) {
	e.T.Helper()
	e.MustCLI("session", "keys", s.ID, text)
}

// Screen returns a session's visible screen as the server sees it.
func (e *Env) Screen(s *Session) string {
	e.T.Helper()
	return TrimScreen(e.MustCLI("session", "read", s.ID))
}

// WaitFor waits until a session's screen contains text and returns it.
func (e *Env) WaitFor(s *Session, text string, timeout time.Duration) string {
	e.T.Helper()
	var last string
	if !Poll(timeout, func() bool { last = e.Screen(s); return strings.Contains(last, text) }) {
		e.T.Fatalf("session %s: no %q after %v; screen:\n%s", s.ID, text, timeout, last)
	}
	return last
}

// AssertAlive fails the test unless the server and the session's process
// are running and the server still lists the session.
func (e *Env) AssertAlive(s *Session) {
	e.T.Helper()
	if pid := e.ServerPID(); !Alive(pid) {
		e.T.Fatalf("server (pid %d) is gone", pid)
	}
	if !Alive(s.PID) {
		e.T.Fatalf("session %s (pid %d) is gone", s.ID, s.PID)
	}
	for _, info := range e.Sessions() {
		if info.ID == s.ID {
			return
		}
	}
	e.T.Fatalf("server no longer lists session %s", s.ID)
}

// KillServer SIGKILLs the server, as a crash would, and waits until it is
// gone. Its sessions die with it (their PTYs close).
func (e *Env) KillServer() {
	e.T.Helper()
	pid := e.ServerPID()
	if !Alive(pid) {
		e.T.Fatalf("no server to kill (pid file says %d)", pid)
	}
	syscall.Kill(pid, syscall.SIGKILL)
	if !Poll(DefaultTimeout, func() bool { return !Alive(pid) }) {
		e.T.Fatalf("server %d survived SIGKILL", pid)
	}
}

// RestartServer stops the server cleanly and starts a new one.
func (e *Env) RestartServer() {
	e.T.Helper()
	e.MustCLI("server", "restart")
}

// cleanup stops the server, then checks that nothing the test started is
// left (the orphan check), and saves artifacts if the test failed.
func (e *Env) cleanup() {
	t := e.T
	artifacts := ""
	if t.Failed() {
		artifacts = e.saveArtifacts()
	}
	// Last, so failures of the checks below count too.
	defer func() {
		if t.Failed() {
			noteFailed(t.Name(), artifacts)
		}
	}()
	for i, w := range e.windows {
		w.KillClient()
		w.CloseWindow()
		// A race-built tm reports races on its stderr: the window.
		if bytes.Contains(w.Raw(), []byte("WARNING: DATA RACE")) {
			t.Errorf("window %d: tm reported a data race:\n%s", i+1, w.Raw())
		}
	}
	// Every server a test started must be stopped here: track it, so the
	// orphan check below kills it if stopping fails.
	if pid := e.ServerPID(); Alive(pid) {
		e.track(pid, "tm server")
	}
	if Alive(e.ServerPID()) {
		// Collect the session processes before stopping, so the check
		// covers sessions started behind the harness's back.
		if r := e.CLI("session", "list", "--json"); r.Code == 0 {
			var list []proto.SessionInfo
			json.Unmarshal([]byte(r.Stdout), &list)
			for _, s := range list {
				e.track(s.PID, "session "+s.ID)
			}
		}
		e.CLI("server", "stop", "--yes")
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	var orphans []string
	deadline := time.Now().Add(3 * time.Second)
	for pid, label := range e.pids {
		// A session leads its own process group: check the whole group.
		for (Alive(pid) || groupAlive(pid)) && time.Now().Before(deadline) {
			time.Sleep(20 * time.Millisecond)
		}
		if Alive(pid) || groupAlive(pid) {
			orphans = append(orphans, fmt.Sprintf("%s (pid %d)", label, pid))
			syscall.Kill(-pid, syscall.SIGKILL)
			syscall.Kill(pid, syscall.SIGKILL)
		}
	}
	if len(orphans) > 0 {
		t.Errorf("processes outlived the test: %s", strings.Join(orphans, ", "))
	}
}

// saveArtifacts writes every window's last screen, the server log and
// sessions.json under $E2E_ARTIFACTS (default: a temp dir) for CI to
// upload, and returns the dir.
func (e *Env) saveArtifacts() string {
	base := os.Getenv("E2E_ARTIFACTS")
	if base == "" {
		base = filepath.Join(os.TempDir(), "tm-e2e-artifacts")
	}
	dir := filepath.Join(base, strings.NewReplacer("/", "_", " ", "_").Replace(e.T.Name()))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return ""
	}
	for i, w := range e.windows {
		os.WriteFile(filepath.Join(dir, fmt.Sprintf("window-%d.txt", i+1)), []byte(w.Screen()+"\n"), 0o644)
		os.WriteFile(filepath.Join(dir, fmt.Sprintf("window-%d.raw", i+1)), w.Raw(), 0o644)
	}
	if Alive(e.ServerPID()) {
		for _, s := range e.agentSessions() {
			if r := e.CLI("agent", "explain", s); r.Code == 0 {
				os.WriteFile(filepath.Join(dir, "explain-"+s+".txt"), []byte(r.Stdout), 0o644)
			}
		}
	}
	if b, err := os.ReadFile(e.FakeLog()); err == nil {
		os.WriteFile(filepath.Join(dir, "fakeagent.jsonl"), b, 0o644)
	}
	if b, err := os.ReadFile(e.AttachLog); err == nil {
		os.WriteFile(filepath.Join(dir, "attach.log"), b, 0o644)
	}
	for _, f := range []string{"logs/server.log", "state/sessions.json"} {
		if b, err := os.ReadFile(filepath.Join(e.Home, f)); err == nil {
			os.WriteFile(filepath.Join(dir, filepath.Base(f)), b, 0o644)
		}
	}
	e.T.Logf("artifacts saved in %s", dir)
	return dir
}

// Alive reports whether a process exists.
func Alive(pid int) bool { return pid > 0 && syscall.Kill(pid, 0) == nil }

func groupAlive(pgid int) bool { return pgid > 0 && syscall.Kill(-pgid, 0) == nil }

// Poll calls cond until it returns true or timeout passes.
func Poll(timeout time.Duration, cond func() bool) bool {
	deadline := time.Now().Add(timeout)
	for {
		if cond() {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(25 * time.Millisecond)
	}
}

// TrimScreen removes trailing blanks on every line and trailing empty lines.
func TrimScreen(s string) string {
	lines := strings.Split(s, "\n")
	for i := range lines {
		lines[i] = strings.TrimRight(lines[i], " ")
	}
	for len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return strings.Join(lines, "\n")
}
