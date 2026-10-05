package legacy

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func TestEnv(t *testing.T) {
	t.Setenv("TERMALATOR_HOME", "/old/home")
	t.Setenv("TERMALATOR_SESSION", "s-1")
	t.Setenv("TERMILATOR_SESSION", "s-2")
	t.Setenv("TERMALATOR", "1")
	os.Unsetenv("TERMILATOR_HOME")
	os.Unsetenv("TERMILATOR")
	Env()
	for k, want := range map[string]string{"TERMILATOR_HOME": "/old/home", "TERMILATOR_SESSION": "s-2", "TERMILATOR": "1"} {
		if got := os.Getenv(k); got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
	os.Unsetenv("TERMILATOR_HOME")
	os.Unsetenv("TERMILATOR")
}

// fakeHome makes $HOME a temp dir with a v0.1.0 state directory.
func fakeHome(t *testing.T) (home, old, nu string) {
	t.Helper()
	home = t.TempDir()
	if r, err := filepath.EvalSymlinks(home); err == nil {
		home = r
	}
	t.Setenv("HOME", home)
	t.Setenv("TERMILATOR_HOME", "")
	t.Setenv("XDG_RUNTIME_DIR", "")
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	old, nu = filepath.Join(home, ".termalator"), filepath.Join(home, ".termilator")
	if err := os.MkdirAll(filepath.Join(old, "state"), 0o700); err != nil {
		t.Fatal(err)
	}
	return home, old, nu
}

func write(t *testing.T, path, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestPending(t *testing.T) {
	_, old, nu := fakeHome(t)
	if o, n, ok := Pending(); !ok || o != old || n != nu {
		t.Fatalf("Pending = %q %q %v", o, n, ok)
	}
	t.Setenv("TERMILATOR_HOME", "/somewhere")
	if _, _, ok := Pending(); ok {
		t.Fatal("pending with TERMILATOR_HOME set")
	}
	t.Setenv("TERMILATOR_HOME", "")
	os.Mkdir(nu, 0o700)
	if _, _, ok := Pending(); ok {
		t.Fatal("pending although ~/.termilator exists")
	}
}

func TestOldServer(t *testing.T) {
	_, old, _ := fakeHome(t)
	p := OldPaths(old)
	if _, running := OldServer(p); running {
		t.Fatal("running without a lock file")
	}
	write(t, p.Lock, "")
	write(t, p.PID, "4242\n")
	if _, running := OldServer(p); running {
		t.Fatal("running with a free lock")
	}
	f, err := os.Open(p.Lock)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	if pid, running := OldServer(p); !running || pid != 4242 {
		t.Fatalf("OldServer = %d %v, want 4242 true", pid, running)
	}
}

func TestMigrate(t *testing.T) {
	home, old, nu := fakeHome(t)
	write(t, filepath.Join(old, "state", "sessions.json"),
		`{"cwd": "`+old+`/projects/demo", "other": "`+old+`-spike/x", "end": "`+old+`"}`)
	write(t, filepath.Join(old, "projects", "demo", "threads", "t-0001", "thread.toml"),
		`worktree = "`+old+`/worktrees/demo/t-0001-x"`+"\n")
	write(t, filepath.Join(old, "run", "s", "s-1", "claude-plugin", ".claude-plugin", "plugin.json"),
		`{"name": "termalator", "description": "termalator session hooks", "version": "0.1.0"}`)
	write(t, filepath.Join(old, "run", "s", "s-1", "claude-settings.json"),
		`{"allow": ["Read(/`+old+`/projects/termalator/**)"], "env": "TERMALATOR_BIN"}`)
	write(t, filepath.Join(old, "logs", "server.log"), "started in "+old+"\n")

	// A thread worktree of a real repository.
	repo := filepath.Join(home, "repo")
	git := func(dir string, args ...string) string {
		t.Helper()
		out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	os.MkdirAll(repo, 0o755)
	git(repo, "init", "-q")
	git(repo, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "--allow-empty", "-m", "x")
	wt := filepath.Join(old, "worktrees", "demo", "t-0001-x")
	git(repo, "worktree", "add", "-q", "-b", "tm/demo/t-0001-x", wt)

	// Claude's transcript folders, one of them for another directory.
	claude := filepath.Join(home, ".claude", "projects")
	for _, d := range []string{claudeKey(old) + "-projects-demo", claudeKey(old) + "-worktrees-demo-t-0001-x", claudeKey(old) + "-spike-repos-demo"} {
		write(t, filepath.Join(claude, d, "a.jsonl"), "{}\n")
	}

	var warn bytes.Buffer
	if err := Migrate(old, nu, &warn); err != nil {
		t.Fatal(err)
	}
	if warn.Len() > 0 {
		t.Errorf("warnings: %s", warn.String())
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Fatal("old home still there")
	}
	got := read(t, filepath.Join(nu, "state", "sessions.json"))
	want := `{"cwd": "` + nu + `/projects/demo", "other": "` + old + `-spike/x", "end": "` + nu + `"}`
	if got != want {
		t.Errorf("sessions.json:\n got %s\nwant %s", got, want)
	}
	if got := read(t, filepath.Join(nu, "projects", "demo", "threads", "t-0001", "thread.toml")); !strings.Contains(got, nu+"/worktrees/demo/t-0001-x") {
		t.Errorf("thread.toml: %s", got)
	}
	if got := read(t, filepath.Join(nu, "run", "s", "s-1", "claude-plugin", ".claude-plugin", "plugin.json")); !strings.Contains(got, `"name": "termilator", "description": "termilator session hooks"`) {
		t.Errorf("plugin.json: %s", got)
	}
	// The project slug "termalator" is the user's and stays.
	if got := read(t, filepath.Join(nu, "run", "s", "s-1", "claude-settings.json")); got != `{"allow": ["Read(/`+nu+`/projects/termalator/**)"], "env": "TERMILATOR_BIN"}` {
		t.Errorf("claude-settings.json: %s", got)
	}
	if got := read(t, filepath.Join(nu, "logs", "server.log")); !strings.Contains(got, old) {
		t.Errorf("the log was rewritten: %s", got)
	}
	if s := Stale(filepath.Join(nu, "run", "s"), old); len(s) > 0 {
		t.Errorf("stale after migrate: %v", s)
	}

	nwt := filepath.Join(nu, "worktrees", "demo", "t-0001-x")
	if out := git(repo, "worktree", "list", "--porcelain"); !strings.Contains(out, "worktree "+nwt) {
		t.Errorf("worktree not repaired:\n%s", out)
	}
	if got := git(nwt, "rev-parse", "--abbrev-ref", "HEAD"); got != "tm/demo/t-0001-x" {
		t.Errorf("worktree HEAD = %q", got)
	}

	for _, d := range []string{claudeKey(nu) + "-projects-demo", claudeKey(nu) + "-worktrees-demo-t-0001-x", claudeKey(old) + "-spike-repos-demo"} {
		if _, err := os.Stat(filepath.Join(claude, d, "a.jsonl")); err != nil {
			t.Errorf("claude folder %s: %v", d, err)
		}
	}
}

func TestStaleAndFix(t *testing.T) {
	_, old, nu := fakeHome(t)
	os.Remove(filepath.Join(old, "state"))
	os.Remove(old)
	dir := filepath.Join(nu, "run", "s", "s-3")
	hooks := filepath.Join(dir, "claude-plugin", "hooks", "hooks.json")
	write(t, hooks, `{"command": "\"`+old+`/server-bin/tm-abc\" hook --agent claude"}`)
	write(t, filepath.Join(dir, "ok.json"), `{"cwd": "`+nu+`/projects/termalator"}`)
	stale := Stale(filepath.Join(nu, "run", "s"), old)
	if len(stale) != 1 || stale[0] != hooks {
		t.Fatalf("Stale = %v, want [%s]", stale, hooks)
	}
	if err := Fix(stale); err != nil {
		t.Fatal(err)
	}
	if got := read(t, hooks); got != `{"command": "\"`+nu+`/server-bin/tm-abc\" hook --agent claude"}` {
		t.Errorf("hooks.json: %s", got)
	}
	if s := Stale(filepath.Join(nu, "run", "s"), old); len(s) > 0 {
		t.Errorf("still stale: %v", s)
	}
}

func TestOldService(t *testing.T) {
	home := t.TempDir()
	if _, ok := OldServiceFile("darwin", home); ok {
		t.Fatal("found a service that isn't there")
	}
	plist := filepath.Join(home, "Library", "LaunchAgents", OldLabel+".plist")
	write(t, plist, "<plist/>")
	p, ok := OldServiceFile("darwin", home)
	if !ok || p != plist {
		t.Fatalf("OldServiceFile = %q %v", p, ok)
	}
	var ran []string
	run := func(name string, args ...string) error {
		ran = append(ran, name+" "+strings.Join(args, " "))
		return nil
	}
	if err := RemoveOldService("darwin", p, run); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(plist); !os.IsNotExist(err) {
		t.Error("plist still there")
	}
	if len(ran) != 1 || !strings.HasPrefix(ran[0], "launchctl bootout gui/") || !strings.HasSuffix(ran[0], "/"+OldLabel) {
		t.Errorf("ran %q", ran)
	}
}
