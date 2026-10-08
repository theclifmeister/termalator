package doctor

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	_ "github.com/theclifmeister/terminatr/internal/agent/claude" // registers the Go agent (Doctor)
	"github.com/theclifmeister/terminatr/internal/codehost"
	"github.com/theclifmeister/terminatr/internal/project"
	"github.com/theclifmeister/terminatr/internal/proto"
	"github.com/theclifmeister/terminatr/internal/server"
	"github.com/theclifmeister/terminatr/internal/service"
	"github.com/theclifmeister/terminatr/internal/thread"
	"github.com/theclifmeister/terminatr/internal/update"
	"github.com/theclifmeister/terminatr/internal/worktree"
)

// testDeps is an isolated home with a short run dir, no server, and the
// real git.
func testDeps(t *testing.T) Deps {
	t.Helper()
	h := t.TempDir()
	t.Setenv("TERMINATR_HOME", h)
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

func TestCodeHosts(t *testing.T) {
	d := testDeps(t)
	d.LookPath = func(n string) (string, error) { return "/bin/" + n, nil }
	var ran []string
	var mu sync.Mutex // the checks run at once
	fail := ""
	d.Run = func(dir, name string, args ...string) (string, error) {
		line := name + " " + strings.Join(args, " ")
		mu.Lock()
		ran = append(ran, dir+"|"+line)
		mu.Unlock()
		if fail != "" && strings.Contains(line, fail) {
			return "boom", errors.New("exit 1")
		}
		return "ok", nil
	}
	shop := codehost.Target{Kind: codehost.AzureKind, OrgURL: "https://dev.azure.com/acme", Project: "Shop", Repo: "web"}
	d.Hosts = func() []RepoHost { return []RepoHost{{Repo: "/r/web", Target: shop}, {Repo: "/r/web2", Target: shop}} }
	var env map[string]string
	d.Getenv = func(k string) string { return env[k] }
	var patAsked []string
	d.PATGet = func(u, pat string) ([]byte, error) {
		mu.Lock()
		defer mu.Unlock()
		patAsked = append(patAsked, u)
		return nil, &codehost.CLIError{CLI: "az", Problem: "Azure DevOps refused AZURE_DEVOPS_EXT_PAT (401/403)", Err: errors.New("HTTP 401")}
	}

	cs := Toolchain(d)
	if len(find(cs, "gh")) != 0 || len(find(cs, "gh auth")) != 0 {
		t.Fatalf("gh checked with only an Azure repo: %+v", cs)
	}
	for _, n := range []string{"az", "az login", "az repo Shop/web", "git origin Shop/web"} {
		if c := find(cs, n); len(c) != 1 || c[0].Status != OK || c[0].Group != "code host" {
			t.Errorf("%s: %+v", n, c)
		}
	}
	if len(find(cs, "az azure-devops")) != 0 {
		t.Errorf("checked the azure-devops extension: %+v", cs)
	}
	got := strings.Join(ran, "\n")
	if strings.Contains(got, "extension") {
		t.Errorf("asked about the azure-devops extension:\n%s", got)
	}
	for _, want := range []string{"/bin/az rest --method get --resource 499b84ac-1321-427f-aa17-267ca6975798 --url https://dev.azure.com/acme/Shop/_apis/git/repositories/web?api-version=7.1 ", "/r/web|git ls-remote origin HEAD"} {
		if !strings.Contains(got, want) {
			t.Errorf("didn't run %q:\n%s", want, got)
		}
	}

	fail = "account show"
	if c := find(Toolchain(d), "az login"); c[0].Status != Warn || !strings.Contains(c[0].Detail, "az login") {
		t.Errorf("logged out: %+v", c)
	}
	env = map[string]string{"AZURE_DEVOPS_EXT_PAT": "s3cret"}
	c := find(Toolchain(d), "az login")
	if c[0].Status != OK || strings.Contains(c[0].Detail, "s3cret") || !strings.Contains(c[0].Detail, "AZURE_DEVOPS_EXT_PAT") {
		t.Errorf("PAT: %+v", c)
	}
	fail = "rest --method get"
	if c := find(Toolchain(d), "az repo Shop/web"); c[0].Status != Warn || !strings.Contains(c[0].Detail, "https://dev.azure.com/acme") {
		t.Errorf("no access: %+v", c)
	}
	fail = "ls-remote"
	if c := find(Toolchain(d), "git origin Shop/web"); c[0].Status != Warn || !strings.Contains(c[0].Detail, "credential helper") {
		t.Errorf("git credentials: %+v", c)
	}
	d.LookPath = func(n string) (string, error) { return "", exec.ErrNotFound }
	// no az, the PAT set: tm asks with it
	if cs := Toolchain(d); len(find(cs, "az")) != 1 || find(cs, "az")[0].Status != OK || len(find(cs, "az repo Shop/web")) != 1 ||
		find(cs, "az repo Shop/web")[0].Status != Warn || len(patAsked) == 0 || patAsked[0] != "https://dev.azure.com/acme/Shop/_apis/git/repositories/web?api-version=7.1" {
		t.Errorf("no az, PAT: %+v %q", cs, patAsked)
	}
	env = nil
	if cs := Toolchain(d); len(find(cs, "az")) != 1 || find(cs, "az")[0].Status != Warn || find(cs, "az repo Shop/web")[0].Status != Warn {
		t.Errorf("no az: %+v", cs)
	}

	// Both kinds in use: both CLIs are checked, once each.
	d.LookPath = func(n string) (string, error) { return "/bin/" + n, nil }
	fail = ""
	d.Hosts = func() []RepoHost {
		return []RepoHost{{Repo: "/r/a", Target: codehost.Target{Kind: codehost.GitHubKind}}, {Repo: "/r/b", Target: shop}}
	}
	cs = Toolchain(d)
	if len(find(cs, "gh auth")) != 1 || len(find(cs, "az login")) != 1 {
		t.Errorf("both kinds: %+v", cs)
	}
	// No repos: GitHub, as on a fresh install.
	d.Hosts = func() []RepoHost { return nil }
	cs = Toolchain(d)
	if len(find(cs, "gh auth")) != 1 {
		t.Errorf("no repos: %+v", cs)
	}
	// az installed, no Azure repo: its lines show, marked unused, never a warning.
	for n, want := range map[string]string{"az": "found", "az login": "logged in"} {
		if c := find(cs, n); len(c) != 1 || c[0].Status != OK || !strings.HasPrefix(c[0].Detail, want) || !strings.Contains(c[0].Detail, "no project uses Azure DevOps") {
			t.Errorf("unused %s: %+v", n, c)
		}
	}
	if len(find(cs, "az azure-devops")) != 0 || len(find(cs, "az repo Shop/web")) != 0 {
		t.Errorf("Azure-only lines without an Azure repo: %+v", cs)
	}
	fail = "account show"
	if c := find(Toolchain(d), "az login"); len(c) != 1 || c[0].Status != OK || !strings.HasPrefix(c[0].Detail, "not logged in") {
		t.Errorf("unused, logged out: %+v", c)
	}
	// az missing and no Azure repo: no az lines at all.
	d.LookPath = func(n string) (string, error) {
		if n == "az" {
			return "", exec.ErrNotFound
		}
		return "/bin/" + n, nil
	}
	if cs := Toolchain(d); len(find(cs, "az")) != 0 || len(find(cs, "az login")) != 0 {
		t.Errorf("no az, no Azure repo: %+v", cs)
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
	p, err := project.New(project.Options{Slug: "demo", Repos: []string{repo}})
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
	d.Install = &update.Install{Method: update.Homebrew, Path: "/opt/homebrew/Cellar/terminatr/0.1.0/bin/tm", Upgrade: "brew upgrade terminatr"}
	d.Latest = func() (string, error) { return "v0.2.0", nil }
	got := Install(d)
	if len(got) != 2 || got[0].Detail != "homebrew, /opt/homebrew/Cellar/terminatr/0.1.0/bin/tm" ||
		got[1].Status != Warn || got[1].Detail != "v0.2.0 is available: brew upgrade terminatr" {
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
		!strings.Contains(cs[0].Detail, "tm server restart") || cs[0].Fix == nil {
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
	// Unless the restart goes through launchd's GUI domain (macOS),
	// which starts it in the desktop's session from anywhere.
	d.Launchd = func() bool { return true }
	if cs := keychainCheck(d, bad, nil); len(cs) != 1 || cs[0].Fix == nil || !strings.Contains(cs[0].Fix.Desc, "launchd") {
		t.Fatalf("doctor over SSH, launchd restart: %+v", cs)
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

func TestUpkeep(t *testing.T) {
	d := testDeps(t)
	if got := Upkeep(d); len(got) != 0 {
		t.Fatalf("no projects: %+v", got)
	}
	p, err := project.New(project.Options{Slug: "demo"})
	if err != nil {
		t.Fatal(err)
	}
	if got := Upkeep(d); len(got) != 1 || got[0].Status != OK {
		t.Fatalf("within budget: %+v", got)
	}
	os.WriteFile(p.Path("memory", "big.md"), make([]byte, project.BudgetMemory+1), 0o644)
	got := Upkeep(d)
	if len(got) != 1 || got[0].Status != Warn || got[0].Name != "demo" || !strings.Contains(got[0].Detail, "memory/big.md is 6.0 KB, over its 6 KB budget") || got[0].Fix != nil {
		t.Fatalf("over budget: %+v", got)
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

// TestQueueChecks: a prompt held while its agent is idle shows once it
// has been held for proto.QueueNotice (T43); a fresh hold, or a queue
// waiting on a working agent, doesn't.
func TestQueueChecks(t *testing.T) {
	now := time.Now()
	sessions := []proto.SessionInfo{
		{ID: "s-28", Role: proto.RoleCoordinator, Project: "terminatr", Queued: 1, QueueHeld: "prompt box not empty", QueueHeldSince: now.Add(-9 * time.Hour)},
		{ID: "s-30", Role: proto.RoleCoordinator, Project: "todo", Queued: 1, QueueHeld: "prompt box not empty", QueueHeldSince: now.Add(-10 * time.Second)},
		{ID: "s-31", Role: proto.RoleThread, Project: "todo", Thread: "t-0001", Queued: 2},
	}
	got := queueChecks(sessions, now)
	if len(got) != 1 || got[0].Status != Warn || !strings.Contains(got[0].Detail, "s-28 (terminatr coordinator): 1 queued prompt(s), held 9h0m0s: prompt box not empty") {
		t.Fatalf("checks %+v", got)
	}
}

func TestPlugins(t *testing.T) {
	d := testDeps(t)
	d.LookPath = func(n string) (string, error) { return "/bin/" + n, nil }
	list := `[{"id":"vercel@claude-plugins-official","enabled":true},{"id":"worktrees@supermods","enabled":false}]`
	var listErr error
	d.Run = func(dir, name string, args ...string) (string, error) {
		if name == "/bin/claude" && strings.Join(args, " ") == "plugin list --json" {
			return list, listErr
		}
		return "", errors.New("unexpected " + name)
	}
	if cs := Plugins(d); len(cs) != 1 || cs[0].Status != OK || !strings.Contains(cs[0].Detail, "worktrees@supermods") {
		t.Fatalf("disabled: %+v", cs)
	}
	for _, l := range []string{
		`[{"id":"worktrees@supermods","enabled":true}]`,
		`[{"id":"worktrees@supermods","enabled":false,"projectEnabled":true}]`,
	} {
		list = l
		cs := Plugins(d)
		if len(cs) != 1 || cs[0].Status != Warn || cs[0].Name != "worktrees@supermods" || cs[0].Fix != nil ||
			!strings.Contains(cs[0].Detail, "no commits ahead") || !strings.Contains(cs[0].Detail, "claude plugin disable worktrees@supermods") {
			t.Fatalf("enabled %s: %+v", l, cs)
		}
	}
	list = `[{"id":"worktrees@other","enabled":true}]`
	if cs := Plugins(d); len(cs) != 1 || cs[0].Status != OK {
		t.Fatalf("other marketplace: %+v", cs)
	}
	list = "not json"
	if cs := Plugins(d); len(cs) != 1 || cs[0].Status != Warn || !strings.Contains(cs[0].Detail, "couldn't read") {
		t.Fatalf("bad json: %+v", cs)
	}
	list, listErr = "boom", errors.New("claude plugin list --json: boom")
	if cs := Plugins(d); len(cs) != 1 || cs[0].Status != Warn || !strings.Contains(cs[0].Detail, "couldn't list") {
		t.Fatalf("list fails: %+v", cs)
	}
	d.LookPath = func(n string) (string, error) { return "", exec.ErrNotFound }
	if cs := Plugins(d); len(cs) != 0 {
		t.Fatalf("no claude: %+v", cs)
	}
}

// TestLaunchdStrays: stray jobs are a warning with a fix that boots each
// one out; none, or no launchd, is quiet.
func TestLaunchdStrays(t *testing.T) {
	d := testDeps(t)
	if cs := Launchd(d); len(cs) != 0 {
		t.Fatalf("no launchd: %+v", cs)
	}
	var booted []string
	d.Strays = func() ([]service.Stray, error) { return nil, nil }
	d.Bootout = func(l string) error { booted = append(booted, l); return nil }
	if cs := Launchd(d); len(cs) != 1 || cs[0].Status != OK || cs[0].Fix != nil {
		t.Fatalf("none: %+v", cs)
	}
	d.Strays = func() ([]service.Stray, error) {
		return []service.Stray{{Label: "dev.terminatr.server.aaaa", Reason: "its plist is gone"}, {Label: "dev.terminatr.server.bbbb", Reason: "x"}}, nil
	}
	cs := Launchd(d)
	if len(cs) != 2 || cs[0].Status != Warn || !strings.Contains(cs[0].Detail, "dev.terminatr.server.aaaa") || cs[0].Fix == nil {
		t.Fatalf("strays: %+v", cs)
	}
	for _, f := range Fixes(cs) {
		if err := f.Apply(); err != nil {
			t.Fatal(err)
		}
	}
	if strings.Join(booted, " ") != "dev.terminatr.server.aaaa dev.terminatr.server.bbbb" {
		t.Fatalf("booted: %v", booted)
	}
}

func TestCodeHostsServerContext(t *testing.T) {
	d := testDeps(t)
	d.LookPath = func(n string) (string, error) { return "/bin/" + n, nil }
	authed := false // this shell's gh
	d.Run = func(dir, name string, args ...string) (string, error) {
		if strings.Contains(strings.Join(args, " "), "auth status") && !authed {
			return "", errors.New("not logged in")
		}
		return "ok", nil
	}
	d.Hosts = func() []RepoHost { return nil }
	srv := func(ok bool) Live {
		return Live{CodeHost: &proto.CodeHostStatus{Checks: []proto.CodeHostCheck{
			{Name: "gh", OK: true, Detail: "found"}, {Name: "gh auth", OK: ok, Detail: "server says"}}}}
	}

	// The server's gh works and this shell's doesn't: the server's lines,
	// and a note instead of a warning.
	cs := codeHosts(d, srv(true))
	if c := find(cs, "gh auth"); len(c) != 1 || c[0].Status != OK || c[0].Source != "server" || c[0].Detail != "server says" {
		t.Fatalf("gh auth: %+v", c)
	}
	if c := find(cs, "local shell"); len(c) != 1 || c[0].Status != OK || !strings.Contains(c[0].Detail, "gh auth") || !strings.Contains(c[0].Detail, "the server's sessions pass") {
		t.Fatalf("note: %+v", c)
	}
	if Worst(cs) != OK {
		t.Errorf("worst %s: %+v", Worst(cs), cs)
	}
	// On a Mac over SSH the note names the keychain.
	d.GOOS = "darwin"
	d.Keychain = func() proto.KeychainStatus { return proto.KeychainStatus{Checked: true, OverSSH: true} }
	if c := find(codeHosts(d, srv(true)), "local shell"); len(c) != 1 || !strings.Contains(c[0].Detail, "can't reach the keychain (SSH)") {
		t.Errorf("keychain note: %+v", c)
	}
	// Both fail: the server's warning stands, no note.
	cs = codeHosts(d, srv(false))
	if c := find(cs, "gh auth"); c[0].Status != Warn || len(find(cs, "local shell")) != 0 {
		t.Errorf("both fail: %+v", cs)
	}
	// This shell works too: no note.
	authed = true
	if cs := codeHosts(d, srv(true)); len(find(cs, "local shell")) != 0 {
		t.Errorf("both pass: %+v", cs)
	}
	// No server: this shell's checks, labelled local.
	authed = false
	if c := find(codeHosts(d, Live{}), "gh auth"); len(c) != 1 || c[0].Status != Warn || c[0].Source != "local" {
		t.Errorf("no server: %+v", c)
	}
}

// TestEachStreams: the groups come out one by one in order while the
// code hosts are still being checked; those come last, after waiting
// names their group.
func TestEachStreams(t *testing.T) {
	d := testDeps(t)
	d.LookPath = func(n string) (string, error) { return "/bin/" + n, nil }
	release := make(chan struct{})
	d.Run = func(dir, name string, args ...string) (string, error) {
		if name == "/bin/gh" {
			<-release // a slow gh
		}
		return "ok", nil
	}
	d.Hosts = func() []RepoHost { return nil }
	var groups []string
	waited := false
	Each(d, func(cs []Check) {
		for _, c := range cs {
			if len(groups) == 0 || groups[len(groups)-1] != c.Group {
				groups = append(groups, c.Group)
			}
		}
	}, func(g string) {
		if g != "code host" || slices.Contains(groups, "code host") {
			t.Errorf("waiting %q after %q", g, groups)
		}
		waited = true
		close(release)
	})
	if !waited || len(groups) < 3 || groups[0] != "toolchain" || groups[len(groups)-1] != "code host" || slices.Index(groups, "code host") != len(groups)-1 {
		t.Fatalf("waited %v, groups %q", waited, groups)
	}
}

// TestServerHangs: a server that holds the lock and answers the
// handshake but never a call: doctor's calls give up (callTimeout,
// codeHostTimeout) instead of waiting for ever.
func TestServerHangs(t *testing.T) {
	d := testDeps(t)
	defer func(a, b time.Duration) { callTimeout, codeHostTimeout = a, b }(callTimeout, codeHostTimeout)
	callTimeout, codeHostTimeout = 200*time.Millisecond, 200*time.Millisecond
	lk, err := os.OpenFile(d.Paths.Lock, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer lk.Close()
	if err := unix.Flock(int(lk.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("unix", d.Paths.Socket)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				br := bufio.NewReader(c)
				br.ReadBytes('\n') // the client's hello
				b, _ := json.Marshal(proto.Hello{Protocol: proto.Protocol, Version: "v0", Build: "b0"})
				c.Write(append(b, '\n'))
				io.Copy(io.Discard, br) // and never an answer
			}()
		}
	}()
	start := time.Now()
	cs, _ := Server(d)
	if c := find(cs, "server"); len(c) != 1 || c[0].Status != Fail || !strings.Contains(c[0].Detail, "server.status") {
		t.Errorf("server: %+v", cs)
	}
	if ch := serverCodeHost(d.Paths); ch != nil {
		t.Errorf("server.codehost: %+v", ch)
	}
	if l := sessionsNow(d.Paths); l != nil {
		t.Errorf("session.list: %v", l)
	}
	if took := time.Since(start); took > 5*time.Second {
		t.Errorf("took %s", took)
	}
}

// TestModels: the models line names each agent's catalog and where it
// comes from; a stale default, models for an unknown agent and allowed
// models no catalog lists are warnings.
func TestModels(t *testing.T) {
	d := testDeps(t)
	cs := Models(d)
	if len(cs) != 1 || cs[0].Status != OK || cs[0].Detail != "claude 3 (as released), codex 2 (as released)" {
		t.Fatalf("no config: %+v", cs)
	}
	body := "[agents.claude]\nmodels = [{ name = \"opus-6\", about = \"newest\" }]\n\n[agents.codex]\ndefault_model = \"gpt-1\"\n\n[agents.nope]\ndefault_model = \"\"\n\n" +
		"[defaults]\nmodels = [\"opus\", \"opus-6\"]\n\n[projects.demo]\nmodels = [\"opus\", \"haiku\"]\n"
	os.WriteFile(filepath.Join(d.Paths.Home, "config.toml"), []byte(body), 0o600)
	cs = Models(d)
	var got []string
	for _, c := range cs {
		got = append(got, string(c.Status)+" "+c.Detail)
	}
	want := []string{
		"ok claude 1 (your list), codex 2 (as released)",
		"warn codex's default model gpt-1 is stale: its models don't list it, so threads run the agent's own default; pick another in Settings > Models",
		"warn config.toml has models for agent nope, which tm doesn't know (tm agent list)",
		"warn allowed model opus is stale: no agent lists it (all projects, demo); Settings > Thread models shows it, enter leaves it out",
		"warn allowed model haiku is stale: no agent lists it (demo); Settings > Thread models shows it, enter leaves it out",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("got:\n%s", strings.Join(got, "\n"))
	}
}
