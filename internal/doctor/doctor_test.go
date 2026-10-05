package doctor

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"

	"github.com/theclifmeister/termilator/internal/project"
	"github.com/theclifmeister/termilator/internal/proto"
	"github.com/theclifmeister/termilator/internal/server"
	"github.com/theclifmeister/termilator/internal/thread"
	"github.com/theclifmeister/termilator/internal/update"
	"github.com/theclifmeister/termilator/internal/worktree"
)

// testDeps is an isolated home with a short run dir, no server, and the
// real git.
func testDeps(t *testing.T) Deps {
	t.Helper()
	h := t.TempDir()
	t.Setenv("TERMILATOR_HOME", h)
	run, err := os.MkdirTemp("/tmp", "tmdoc-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(run) })
	os.Chmod(run, 0o700)
	p := server.Paths{Home: h, RunDir: run, Socket: filepath.Join(run, "tm.sock"), Lock: filepath.Join(run, "server.lock"),
		PID: filepath.Join(run, "server.pid"), Log: filepath.Join(h, "logs", "server.log"), Sessions: filepath.Join(h, "state", "sessions.json")}
	d := DefaultDeps(p, "v0", "b0")
	d.GOOS = "linux"
	return d
}

func find(cs []Check, name string) []Check {
	var out []Check
	for _, c := range cs {
		if c.Name == name {
			out = append(out, c)
		}
	}
	return out
}

func TestServerStaleFiles(t *testing.T) {
	d := testDeps(t)
	os.WriteFile(d.Paths.Socket, nil, 0o600)
	os.WriteFile(d.Paths.PID, []byte("4242\n"), 0o600)
	os.MkdirAll(filepath.Join(d.Paths.RunDir, "s", "s-1"), 0o700)
	cs, live := Server(d)
	if live.Running {
		t.Fatal("no server runs")
	}
	if c := find(cs, "server"); len(c) != 1 || c[0].Status != OK {
		t.Fatalf("server: %+v", cs)
	}
	for _, n := range []string{"stale socket", "stale pid file", "runtime dir"} {
		c := find(cs, n)
		if len(c) != 1 || c[0].Status != Warn || c[0].Fix == nil {
			t.Fatalf("%s: %+v", n, cs)
		}
	}
	// Without --fix nothing is removed.
	if _, err := os.Stat(d.Paths.Socket); err != nil {
		t.Fatal("check removed the socket")
	}
	for _, f := range Fixes(cs) {
		if err := f.Apply(); err != nil {
			t.Fatal(err)
		}
	}
	for _, p := range []string{d.Paths.Socket, d.Paths.PID, filepath.Join(d.Paths.RunDir, "s", "s-1")} {
		if _, err := os.Stat(p); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s not removed", p)
		}
	}
}

func TestServerHung(t *testing.T) {
	d := testDeps(t)
	f, err := os.OpenFile(d.Paths.Lock, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(d.Paths.PID, []byte("4242\n"), 0o600)
	cs, live := Server(d)
	c := find(cs, "server")
	if !live.Running || len(c) != 1 || c[0].Status != Fail || !strings.Contains(c[0].Detail, "pid 4242") {
		t.Fatalf("hung: %+v", cs)
	}
	if len(Fixes(cs)) != 0 {
		t.Fatal("a held lock must not offer to remove anything")
	}
	// A stale-file fix refuses while a server holds the lock.
	if err := removeIfNoServer(d.Paths, d.Paths.PID); err == nil {
		t.Fatal("removed the pid file of a live server")
	}
}

func TestServerRunDirMode(t *testing.T) {
	d := testDeps(t)
	os.Chmod(d.Paths.RunDir, 0o755)
	if c := find(serverChecks(d), "run dir"); len(c) != 1 || c[0].Status != Fail {
		t.Fatalf("run dir: %+v", c)
	}
}

func serverChecks(d Deps) []Check { cs, _ := Server(d); return cs }

func TestAgentVersions(t *testing.T) {
	cases := []struct {
		version string
		missing bool
		want    Status
		detail  string
	}{
		{version: "2.1.289 (Claude Code)", want: OK, detail: "2.1.289 (tested)"},
		{version: "3.0.1 (Claude Code)", want: Warn, detail: "not in tested_versions"},
		{version: "garbage", want: Warn, detail: "no version"},
		{missing: true, want: Warn, detail: "not found on PATH"},
	}
	for _, tc := range cases {
		d := testDeps(t)
		d.LookPath = func(n string) (string, error) {
			if tc.missing {
				return "", exec.ErrNotFound
			}
			return "/bin/" + n, nil
		}
		d.Run = func(dir, name string, args ...string) (string, error) { return tc.version, nil }
		c := find(Agents(d), "claude")
		if len(c) != 1 || c[0].Status != tc.want || !strings.Contains(c[0].Detail, tc.detail) {
			t.Errorf("%q: %+v", tc.version, c)
		}
	}
}

func TestGHAuth(t *testing.T) {
	d := testDeps(t)
	d.LookPath = func(n string) (string, error) { return "/bin/" + n, nil }
	authed := true
	d.Run = func(dir, name string, args ...string) (string, error) {
		if name == "/bin/gh" && strings.Join(args, " ") == "auth status" && !authed {
			return "You are not logged into any GitHub hosts.", errors.New("gh auth status: exit status 1")
		}
		return "ok", nil
	}
	if c := find(Toolchain(d), "gh auth"); len(c) != 1 || c[0].Status != OK {
		t.Fatalf("logged in: %+v", c)
	}
	authed = false
	if c := find(Toolchain(d), "gh auth"); len(c) != 1 || c[0].Status != Warn || !strings.Contains(c[0].Detail, "gh auth login") {
		t.Fatalf("logged out: %+v", c)
	}
	d.LookPath = func(n string) (string, error) { return "", exec.ErrNotFound }
	if c := find(Toolchain(d), "gh auth"); len(c) != 0 {
		t.Fatalf("no gh: %+v", c)
	}
}

func TestBrokenUserManifest(t *testing.T) {
	d := testDeps(t)
	os.MkdirAll(d.Paths.AgentsDir(), 0o700)
	os.WriteFile(filepath.Join(d.Paths.AgentsDir(), "bad.toml"), []byte("nonsense = ["), 0o600)
	if c := find(Agents(d), "manifest"); len(c) != 1 || c[0].Status != Warn {
		t.Fatalf("broken manifest: %+v", c)
	}
}

func TestSandbox(t *testing.T) {
	d := testDeps(t)
	d.LookPath = func(n string) (string, error) {
		if n == "bwrap" {
			return "/usr/bin/bwrap", nil
		}
		return "", exec.ErrNotFound
	}
	cs := Sandbox(d)
	if find(cs, "bwrap")[0].Status != OK || find(cs, "socat")[0].Status != Warn {
		t.Fatalf("linux: %+v", cs)
	}
	d.GOOS = "darwin"
	if cs := Sandbox(d); len(cs) != 1 || cs[0].Name != "sandbox-exec" || cs[0].Status != Warn {
		t.Fatalf("darwin: %+v", cs)
	}
}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@t", "-c", "init.defaultBranch=main"}, args...)...)
	cmd.Dir = dir
	b, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, b)
	}
	return strings.TrimSpace(string(b))
}

func TestLeftovers(t *testing.T) {
	d := testDeps(t)
	repo := filepath.Join(t.TempDir(), "repo")
	os.MkdirAll(repo, 0o755)
	git(t, repo, "init", "-q")
	git(t, repo, "commit", "-q", "--allow-empty", "-m", "init")
	p, err := project.New(project.Options{Name: "Demo", Repos: []string{repo}})
	if err != nil {
		t.Fatal(err)
	}
	mk := func(title, state string) *thread.Record {
		t.Helper()
		r, err := thread.Create(p, thread.Record{Title: title, Agent: "claude", Repo: repo, State: state})
		if err != nil {
			t.Fatal(err)
		}
		wt, _ := thread.WorktreeDir(p.Slug, r.ID, title)
		br := thread.BranchName(p.Slug, r.ID, title)
		if _, err := worktree.Create(repo, wt, br, "HEAD"); err != nil {
			t.Fatal(err)
		}
		r, err = thread.Update(p, r.ID, func(x *thread.Record) error { x.Worktree, x.Branch = wt, br; return nil })
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	running := mk("Open one", thread.Running)
	clean := mk("Done clean", thread.Resolved)
	dirty := mk("Done dirty", thread.Resolved)
	os.WriteFile(filepath.Join(dirty.Worktree, "wip.txt"), []byte("x"), 0o644)
	// A branch with work on it and no thread: never offered for removal.
	git(t, repo, "branch", "tm/demo/t-0009-gone")
	git(t, repo, "checkout", "-q", "tm/demo/t-0009-gone")
	git(t, repo, "commit", "-q", "--allow-empty", "-m", "work")
	git(t, repo, "checkout", "-q", "main")
	// A resolved thread's second branch (a second PR's), merged: offered.
	git(t, repo, "branch", "tm/demo/t-0002-second")
	// An empty folder with no thread at all.
	stray := filepath.Join(d.Paths.Home, "worktrees", "demo", "t-0042-stray")
	os.MkdirAll(stray, 0o755)

	cs := Leftovers(d, Live{})
	text := ""
	for _, c := range cs {
		text += string(c.Status) + " " + c.Detail + "\n"
	}
	for _, c := range cs {
		if strings.Contains(c.Detail, running.Worktree) || strings.Contains(c.Detail, running.Branch) {
			t.Errorf("open thread flagged: %s", c.Detail)
		}
	}
	want := map[string]bool{ // detail substring → has a fix
		clean.Worktree + ": thread t-0002 is resolved":                                true,
		dirty.Worktree + ": thread t-0003 is resolved; has uncommitted changes, kept": false,
		stray + ": thread t-0042 has no record":                                       true,
		clean.Branch + " in " + repo:                                                  true,
		"tm/demo/t-0009-gone in " + repo:                                              false,
		"tm/demo/t-0002-second in " + repo + ": thread t-0002 is resolved":            true,
	}
	for sub, fix := range want {
		found := false
		for _, c := range cs {
			if strings.Contains(c.Detail, sub) {
				found = true
				if (c.Fix != nil) != fix {
					t.Errorf("%s: fix %v, want %v", sub, c.Fix != nil, fix)
				}
			}
		}
		if !found {
			t.Errorf("no check for %q in:\n%s", sub, text)
		}
	}
	for _, f := range Fixes(cs) {
		if err := f.Apply(); err != nil {
			t.Errorf("%s: %v", f.Desc, err)
		}
	}
	for _, gone := range []string{clean.Worktree, stray} {
		if _, err := os.Stat(gone); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s not removed", gone)
		}
	}
	for _, kept := range []string{running.Worktree, dirty.Worktree} {
		if _, err := os.Stat(kept); err != nil {
			t.Errorf("%s removed", kept)
		}
	}
	branches := git(t, repo, "branch", "--list", "tm/*")
	if strings.Contains(branches, clean.Branch) || strings.Contains(branches, "t-0002-second") || !strings.Contains(branches, "t-0009-gone") || !strings.Contains(branches, running.Branch) {
		t.Errorf("branches after fix:\n%s", branches)
	}
	journal, _ := os.ReadFile(p.Path("JOURNAL.md"))
	if w := "human branch.delete t-0002 tm/demo/t-0002-second (merged into main, tm doctor --fix)"; !strings.Contains(string(journal), w) {
		t.Errorf("journal lacks %q:\n%s", w, journal)
	}
	// Second run: only the kept ones remain.
	if n := len(Fixes(Leftovers(d, Live{}))); n != 0 {
		t.Errorf("%d fixes left after --fix", n)
	}
}

func TestThreadID(t *testing.T) {
	for in, want := range map[string]string{"t-0003-fix-it": "t-0003", "t-0003": "t-0003", "t-12": "", "notes": "", "t-00x1-a": ""} {
		if got := threadID(in); got != want {
			t.Errorf("threadID(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestInstall(t *testing.T) {
	d := Deps{Version: "v0.1.0"}
	if got := Install(d); got != nil {
		t.Fatalf("no install info: %+v", got)
	}
	d.Install = &update.Install{Method: update.Homebrew, Path: "/opt/homebrew/Cellar/termilator/0.1.0/bin/tm", Upgrade: "brew upgrade termilator"}
	d.Latest = func() (string, error) { return "v0.2.0", nil }
	got := Install(d)
	if len(got) != 2 || got[0].Detail != "homebrew, /opt/homebrew/Cellar/termilator/0.1.0/bin/tm" ||
		got[1].Status != Warn || got[1].Detail != "v0.2.0 is available: brew upgrade termilator" {
		t.Fatalf("newer release: %+v", got)
	}
	d.Latest = func() (string, error) { return "v0.1.0", nil }
	if got := Install(d); got[1].Status != OK || got[1].Detail != "up to date (v0.1.0)" {
		t.Fatalf("current: %+v", got)
	}
	d.Latest = func() (string, error) { return "", update.ErrOff }
	if got := Install(d); len(got) != 1 {
		t.Fatalf("checks off: %+v", got)
	}
	d.Latest = func() (string, error) { return "", errors.New("dial tcp: no route") }
	if got := Install(d); got[1].Status != Warn || !strings.Contains(got[1].Detail, "couldn't check") {
		t.Fatalf("offline: %+v", got)
	}
}

func TestKeychain(t *testing.T) {
	d := testDeps(t)
	restarted := false
	d.Restart = func() error { restarted = true; return nil }
	local := proto.KeychainStatus{Checked: true, OK: true, Session: "Aqua"}
	d.Keychain = func() proto.KeychainStatus { return local }

	if cs := keychainCheck(d, proto.KeychainStatus{Checked: true, OK: true}, nil); len(cs) != 1 || cs[0].Status != OK {
		t.Fatalf("reachable: %+v", cs)
	}
	if cs := keychainCheck(d, proto.KeychainStatus{}, nil); len(cs) != 0 {
		t.Fatalf("not checked (Linux): %+v", cs)
	}
	if cs := keychainCheck(d, proto.KeychainStatus{}, proto.Errorf(proto.ErrUnknownMethod, "unknown method")); len(cs) != 0 {
		t.Fatalf("older server: %+v", cs)
	}

	bad := proto.KeychainStatus{Checked: true, OverSSH: true, Session: "Background",
		Detail: "the server runs in a Background session, not the desktop's (Aqua)"}
	cs := keychainCheck(d, bad, nil)
	if len(cs) != 1 || cs[0].Status != Warn || !strings.Contains(cs[0].Detail, "Background") ||
		!strings.Contains(cs[0].Detail, "not over SSH") || cs[0].Fix == nil {
		t.Fatalf("unreachable, doctor on the Mac: %+v", cs)
	}
	if cs[0].Fix.Apply(); !restarted {
		t.Fatal("the fix didn't restart the server")
	}
	// From an SSH login, or a session of the same server, a restart
	// would start the server where it was: only say what to do.
	for _, l := range []proto.KeychainStatus{
		{Checked: true, OK: true, OverSSH: true, Session: "Aqua"},
		{Checked: true, Session: "Background"},
	} {
		local = l
		if cs := keychainCheck(d, bad, nil); len(cs) != 1 || cs[0].Fix != nil {
			t.Fatalf("doctor in %+v: %+v", l, cs)
		}
	}
}

// TestSettingsRemovedComplete: a project still set to the removed
// complete_tasks "released" gets one warning, with no fix (the user
// picks again in Settings); none without it.
func TestSettingsRemovedComplete(t *testing.T) {
	d := testDeps(t)
	if cs := Settings(d); len(cs) != 0 {
		t.Fatalf("no config: %+v", cs)
	}
	os.WriteFile(filepath.Join(d.Paths.Home, "config.toml"), []byte("[projects.demo]\ncomplete_tasks = \"released\"\n\n[projects.ok]\ncomplete_tasks = \"merged\"\n"), 0o600)
	cs := Settings(d)
	if len(cs) != 1 || cs[0].Status != Warn || cs[0].Fix != nil || !strings.Contains(cs[0].Detail, "when released\" was removed, so tasks complete by you in demo: pick again in Settings") {
		t.Fatalf("%+v", cs)
	}
}

// TestSettingsUnknownKeys: a key tm doesn't know under [keys] or [ui]
// is a warning, not an error that stops tm.
func TestSettingsUnknownKeys(t *testing.T) {
	d := testDeps(t)
	os.WriteFile(filepath.Join(d.Paths.Home, "config.toml"), []byte("[ui]\nicon = \"nerd\"\n"), 0o600)
	cs := Settings(d)
	if len(cs) != 1 || cs[0].Status != Warn || cs[0].Detail != "unknown setting ui.icon, ignored" {
		t.Fatalf("%+v", cs)
	}
}
