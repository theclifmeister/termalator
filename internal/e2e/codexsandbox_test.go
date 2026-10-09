package e2e

// The thread sandbox against the installed codex (T189): `codex sandbox`
// runs a shell under permission profile "tm" exactly as codex.toml
// renders it for a thread, with no login and no model. Seatbelt on
// macOS, bubblewrap or Landlock on Linux. It skips when codex isn't on
// PATH; CI installs the tested version on Linux and sets
// E2E_CODEX_SANDBOX=1, which makes a skip a failure.

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/theclifmeister/terminatr/internal/agent"
)

// sandboxArgs picks the profile's -c flags out of a thread's launch.
func sandboxArgs(t *testing.T, spec agent.LaunchSpec) []string {
	t.Helper()
	m, _ := agent.Builtin("codex")
	pm, err := agent.ParseManifest(m)
	if err != nil {
		t.Fatal(err)
	}
	l, err := agent.FromManifest(pm).Launch(spec)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for i := 0; i+1 < len(l.Argv); i++ {
		if l.Argv[i] == "-c" && (strings.HasPrefix(l.Argv[i+1], "permissions=") || strings.HasPrefix(l.Argv[i+1], "default_permissions=")) {
			out = append(out, "-c", l.Argv[i+1])
		}
	}
	if len(out) != 4 {
		t.Fatalf("no thread profile in %q", l.Argv)
	}
	return out
}

func TestSmokeCodexSandbox(t *testing.T) {
	skip := t.Skip
	if os.Getenv("E2E_CODEX_SANDBOX") == "1" {
		skip = t.Fatal
	}
	bin, err := exec.LookPath("codex")
	if err != nil {
		skip("codex is not on PATH")
	}
	// `codex sandbox -P` and the ":workspace" profile the thread
	// profile extends came in 0.128.
	if help, _ := exec.Command(bin, "sandbox", "--help").CombinedOutput(); !strings.Contains(string(help), "--permission-profile") {
		skip("codex sandbox has no --permission-profile (Codex before 0.128): the thread profile needs 0.128 or later")
	}
	if b, _ := os.ReadFile("/proc/sys/kernel/apparmor_restrict_unprivileged_userns"); strings.TrimSpace(string(b)) == "1" {
		skip("bwrap can't map a user namespace here: sysctl kernel.apparmor_restrict_unprivileged_userns=0 to run it")
	}
	env := New(t)
	// Everything outside /tmp and $TMPDIR, which the profile makes
	// writable: as in a real install, below the user's home. The socket
	// path must stay short.
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	root, err := os.MkdirTemp(home, ".tmcxs")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(root) })
	root, _ = filepath.EvalSymlinks(root)
	repo, wt := filepath.Join(root, "repo"), filepath.Join(root, "wt")
	proj, outside, run := filepath.Join(root, "proj"), filepath.Join(root, "outside"), filepath.Join(root, "run")
	for _, d := range []string{repo, proj, outside, run} {
		os.MkdirAll(d, 0o700)
	}
	os.WriteFile(filepath.Join(proj, "TASKS.md"), []byte("tasks\n"), 0o600)
	git := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@t"}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git(repo, "init", "-q", "-b", "main")
	git(repo, "commit", "-q", "--allow-empty", "-m", "first")
	git(repo, "worktree", "add", "-q", "-b", "wt", wt)

	// The server on a socket the profile doesn't make writable.
	env.Socket = filepath.Join(run, "tm.sock")
	env.Vars = append(env.Vars, "TERMINATR_SOCKET="+env.Socket)
	env.MustCLI("server", "start")
	// Before root goes, socket and all.
	t.Cleanup(func() { env.CLI("server", "stop") })

	// The thread's access policy (internal/server accessFor).
	access := agent.Access{Read: []string{proj}, NoWrite: []string{proj},
		Write: []string{filepath.Join(repo, ".git"), filepath.Join(repo, ".git", "worktrees", "wt")}}
	args := append([]string{"sandbox", "-P", "tm", "-C", wt}, sandboxArgs(t, agent.LaunchSpec{
		Role: agent.RoleThread, SessionID: "s-1", Cwd: wt, RepoRoot: repo,
		GitDir: filepath.Join(repo, ".git", "worktrees", "wt"), RuntimeDir: run,
		TMBin: env.Bin, Socket: env.Socket, Access: access,
	})...)

	// Each probe prints its name and ok or no.
	probe := func(name, cmd string) string {
		return cmd + " >/dev/null 2>&1 && echo " + name + "=ok || echo " + name + "=no"
	}
	tmpdir := os.Getenv("TMPDIR")
	if tmpdir == "" {
		tmpdir = "/tmp"
	}
	script := strings.Join([]string{
		probe("worktree", "echo x > "+filepath.Join(wt, "f")),
		probe("commit", "git -c user.name=t -c user.email=t@t -C "+wt+" add f && git -c user.name=t -c user.email=t@t -C "+wt+" commit -qm f"),
		probe("tmp", "echo x > /tmp/tmcxs-$$ && rm /tmp/tmcxs-$$"),
		probe("tmpdir", "echo x > "+filepath.Join(tmpdir, "tmcxs-$$")+" && rm "+filepath.Join(tmpdir, "tmcxs-$$")),
		probe("outside", "echo x > "+filepath.Join(outside, "f")),
		probe("home", "echo x > "+filepath.Join(root, "f")),
		probe("project-read", "cat "+filepath.Join(proj, "TASKS.md")),
		probe("project-write", "echo x >> "+filepath.Join(proj, "TASKS.md")),
		probe("socket", env.Bin+" server status"),
	}, "\n")
	cmd := exec.Command(bin, append(args, "--", "sh", "-c", script)...)
	cmd.Dir = wt
	// The npm codex is a node script: the user's PATH after tm's.
	cmd.Env = append(env.Vars, "PATH="+filepath.Dir(env.Bin)+":"+os.Getenv("PATH"))
	out, err := cmd.CombinedOutput()
	t.Logf("codex %s\n%s", strings.Join(args, " "), out)
	if err != nil {
		t.Fatalf("codex sandbox: %v", err)
	}
	want := map[string]string{
		"worktree": "ok", "commit": "ok", "tmp": "ok", "tmpdir": "ok",
		"outside": "no", "home": "no", "project-read": "ok", "project-write": "no",
		"socket": "ok",
	}
	got := map[string]string{}
	for _, line := range strings.Split(string(out), "\n") {
		if k, v, ok := strings.Cut(strings.TrimSpace(line), "="); ok {
			got[k] = v
		}
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s: got %q, want %q", k, got[k], v)
		}
	}
	for _, f := range []string{filepath.Join(outside, "f"), filepath.Join(root, "f")} {
		if _, err := os.Stat(f); err == nil {
			t.Errorf("%s was written", f)
		}
	}
}
