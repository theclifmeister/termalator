package codex

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/theclifmeister/terminatr/internal/agent"
)

// The coordinator's sandbox (docs/SPEC.md §8.6, Codex) rests on Codex's
// experimental network proxy and its permission profile syntax, either of
// which a newer Codex may rename or change. Codex ignores a -c key it
// doesn't know (unless --strict-config), so a renamed key would leave
// the coordinator either unsandboxed or with the network wide open, and a
// changed one may stop it from starting. So before a coordinator starts,
// its profile flags run under `codex sandbox` with a probe inside: tm's
// socket must connect and a TCP connection to a loopback listener must
// be refused (limited mode refuses every address but the proxy's). When
// that doesn't hold, the profile flags are dropped (the coordinator runs
// as before them: --approve-for-me, workspace-write, network off) and
// the launch carries a warning naming the Codex version.

// ProbeCommand is the tm command the probe runs inside the sandbox (main
// dispatches it): `tm codex-sandbox-probe <unix socket> <tcp address>`.
const ProbeCommand = "codex-sandbox-probe"

// probeWant is what the probe prints when the sandbox holds.
const probeWant = "unix=ok tcp=refused"

// threadWant is what a thread's probe prints when its sandbox holds:
// tm's socket connects and a file outside the workspace can't be made.
const threadWant = "unix=ok write=denied"

// ProbeMain is the probe: it dials the unix socket and the TCP address
// and prints how each went. With a third argument, a path to create, it
// prints whether that write was denied instead of the TCP result (a
// thread's network is open, so only its filesystem tells a sandbox).
func ProbeMain(args []string) int {
	if len(args) != 2 && len(args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: tm "+ProbeCommand+" SOCKET HOST:PORT [WRITE-PATH]")
		return 2
	}
	dial := func(network, addr string) string {
		c, err := net.DialTimeout(network, addr, 3*time.Second)
		if err != nil {
			return "refused"
		}
		c.Close()
		return "ok"
	}
	if len(args) == 3 {
		w := "denied"
		if f, err := os.OpenFile(args[2], os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o600); err == nil {
			f.Close()
			os.Remove(args[2])
			w = "ok"
		}
		fmt.Printf("unix=%s write=%s\n", dial("unix", args[0]), w)
		return 0
	}
	fmt.Printf("unix=%s tcp=%s\n", dial("unix", args[0]), dial("tcp", args[1]))
	return 0
}

// profileFlags finds the profile's -c flags in argv: the indexes of each
// flag and its value, the profile's name from default_permissions, and
// the other pairs, for `codex sandbox`, where -P names the profile.
func profileFlags(argv []string) (idx []int, name string, flags []string) {
	for i := 0; i+1 < len(argv); i++ {
		if argv[i] != "-c" {
			continue
		}
		v := argv[i+1]
		switch {
		case strings.HasPrefix(v, "default_permissions="):
			name = strings.Trim(strings.TrimPrefix(v, "default_permissions="), `"`)
		case strings.HasPrefix(v, "features.network_proxy="), strings.HasPrefix(v, "permissions="):
			flags = append(flags, "-c", v)
		default:
			continue
		}
		idx = append(idx, i, i+1)
		i++
	}
	return idx, name, flags
}

// probeFunc checks that the profile flags hold under the codex binary
// and returns its version ("" when it can't tell). A variable for tests.
var probeFunc = probeProfile

// probeCache keeps each verdict for a binary (path, size, mtime) and
// flags, so a coordinator's restart doesn't probe again.
var probeCache sync.Map // string -> probeVerdict

type probeVerdict struct {
	version string
	err     error
}

// sandboxed returns l with the profile kept when it holds, or dropped,
// with a warning, when it doesn't.
func sandboxed(l agent.Launch, spec agent.LaunchSpec) agent.Launch {
	idx, name, flags := profileFlags(l.Argv)
	if len(idx) == 0 {
		return l
	}
	version, err := probeFunc(l.Argv[0], spec.TMBin, spec.Cwd, spec.Socket, name, flags)
	if err == nil {
		return l
	}
	drop := map[int]bool{}
	for _, i := range idx {
		drop[i] = true
	}
	argv := make([]string, 0, len(l.Argv)-len(idx))
	for i, a := range l.Argv {
		if !drop[i] {
			argv = append(argv, a)
		}
	}
	l.Argv = argv
	if version == "" {
		version = "(version unknown)"
	}
	l.Warnings = append(l.Warnings, fmt.Sprintf(
		"codex %s: the coordinator's sandbox profile doesn't hold (%v); running the coordinator without it (--approve-for-me: workspace-write, network off, tm calls go to auto-review)",
		version, err))
	return l
}

// probeProfile runs `codex sandbox -P <name> -C cwd <flags> -- tm
// codex-sandbox-probe <sock> <loopback>` and reports why the sandbox
// doesn't hold, if it doesn't.
func probeProfile(codex, tmBin, cwd, sock, name string, flags []string) (string, error) {
	path, err := exec.LookPath(codex)
	if err != nil {
		return "", err
	}
	version := ""
	if out, err := run(path, nil, "--version"); err == nil {
		version = agent.ParseVersion(out)
	}
	key := path + "\x00" + tmBin + "\x00" + cwd + "\x00" + sock + "\x00" + name + "\x00" + strings.Join(flags, "\x00")
	if fi, err := os.Stat(path); err == nil {
		key += fmt.Sprintf("\x00%d\x00%d", fi.Size(), fi.ModTime().UnixNano())
	}
	if v, ok := probeCache.Load(key); ok {
		p := v.(probeVerdict)
		return p.version, p.err
	}
	err = probeOnce(path, tmBin, cwd, sock, name, flags)
	probeCache.Store(key, probeVerdict{version, err})
	return version, err
}

func probeOnce(codex, tmBin, cwd, sock, name string, flags []string) error {
	if tmBin == "" || cwd == "" || sock == "" || name == "" {
		return errors.New("nothing to probe with: no tm binary, cwd, socket or profile name")
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return fmt.Errorf("probe listener: %v", err)
	}
	defer ln.Close()
	args := append([]string{"sandbox", "-P", name, "-C", cwd}, flags...)
	args = append(args, "--", tmBin, ProbeCommand, sock, ln.Addr().String())
	out, err := run(codex, flagsEnv(), args...)
	last := lastLine(out)
	switch {
	case err != nil:
		return fmt.Errorf("codex sandbox refused it: %s", firstLine(out, err))
	case last == probeWant:
		return nil
	case strings.HasPrefix(last, "unix=refused"):
		return errors.New("the profile wasn't applied: tm's socket is unreachable in it")
	case strings.HasSuffix(last, "tcp=ok"):
		return errors.New("the network isn't limited to tm's socket: features.network_proxy or network.mode was ignored")
	}
	return fmt.Errorf("unexpected probe output %q", firstLine(out, nil))
}

// probeTimeout bounds each codex run of the probe.
const probeTimeout = 15 * time.Second

func run(path string, env []string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Env = env
	var b bytes.Buffer
	cmd.Stdout, cmd.Stderr = &b, &b
	err := cmd.Run()
	if ctx.Err() != nil {
		err = fmt.Errorf("timed out after %s", probeTimeout)
	}
	return b.String(), err
}

// flagsEnv is the environment for the probe's codex: the server's,
// without what a Codex session gives the commands it runs (unset_env).
func flagsEnv() []string {
	var env []string
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "CODEX_THREAD_ID=") && !strings.HasPrefix(kv, "CODEX_SANDBOX") {
			env = append(env, kv)
		}
	}
	return env
}

// lastLine is out's last non-empty line, skipping Codex's warnings.
func lastLine(out string) string {
	lines := strings.Split(strings.TrimSpace(out), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if l := strings.TrimSpace(lines[i]); l != "" && !strings.HasPrefix(l, "WARNING") {
			return l
		}
	}
	return ""
}

// firstLine is the first line of out worth quoting (Codex's error, not
// its warnings), else err, bounded.
func firstLine(out string, err error) string {
	for _, l := range strings.Split(out, "\n") {
		if l = strings.TrimSpace(l); l != "" && !strings.HasPrefix(l, "WARNING") {
			if len(l) > 200 {
				l = l[:200] + "…"
			}
			return l
		}
	}
	if err != nil {
		return err.Error()
	}
	return "no output"
}

// ProbeSandbox checks the installed Codex as a coordinator's launch
// would, against a scratch folder and socket (agent.SandboxProber, for
// tm doctor), uncached: the Codex version and why it doesn't hold.
func (a *Agent) ProbeSandbox(tmBin string) (string, error) {
	dir, err := os.MkdirTemp("", "tm-probe-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(dir)
	if real, err := filepath.EvalSymlinks(dir); err == nil {
		dir = real
	}
	sock := filepath.Join(dir, "s.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		return "", err
	}
	defer ln.Close()
	l, err := a.Agent.Launch(agent.LaunchSpec{
		Role: "coordinator", Cwd: dir, RuntimeDir: dir, TMBin: tmBin, Socket: sock,
		Access: agent.Access{Read: []string{dir}},
	})
	if err != nil {
		return "", err
	}
	idx, name, flags := profileFlags(l.Argv)
	if len(idx) == 0 {
		return "", errors.New("the manifest gives the coordinator no profile")
	}
	path, err := exec.LookPath(l.Argv[0])
	if err != nil {
		return "", err
	}
	version := ""
	if out, err := run(path, nil, "--version"); err == nil {
		version = agent.ParseVersion(out)
	}
	return version, probeOnce(path, tmBin, dir, sock, name, flags)
}

// ProbeThreadSandbox checks a thread's profile under the installed Codex
// (agent.ThreadSandboxProber, for tm doctor on Windows): Codex's Windows
// sandbox needs a one-time setup, and without it every command of a
// thread asks for approval (docs/CODEX.md). The probe runs inside the
// profile, so it holds only when the sandbox applies and tm's socket
// (AF_UNIX on Windows) is reachable from it.
func (a *Agent) ProbeThreadSandbox(tmBin string) (string, error) {
	base, err := os.MkdirTemp("", "tm-probe-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(base)
	if real, err := filepath.EvalSymlinks(base); err == nil {
		base = real
	}
	work, outside := filepath.Join(base, "work"), filepath.Join(base, "outside")
	for _, d := range []string{work, outside} {
		if err := os.Mkdir(d, 0o700); err != nil {
			return "", err
		}
	}
	sock := filepath.Join(base, "s.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		return "", err
	}
	defer ln.Close()
	l, err := a.Agent.Launch(agent.LaunchSpec{
		Role: "thread", Cwd: work, RuntimeDir: base, TMBin: tmBin, Socket: sock,
		Access: agent.Access{Write: []string{work}, Read: []string{base}},
	})
	if err != nil {
		return "", err
	}
	idx, name, flags := profileFlags(l.Argv)
	if len(idx) == 0 {
		return "", errors.New("the manifest gives a thread no profile")
	}
	// A thread's profile is the one -c permissions=… flag; profileFlags
	// takes it as it takes the coordinator's.
	path, err := exec.LookPath(l.Argv[0])
	if err != nil {
		return "", err
	}
	version := ""
	if out, err := run(path, nil, "--version"); err == nil {
		version = agent.ParseVersion(out)
	}
	args := append([]string{"sandbox", "-P", name, "-C", work}, flags...)
	args = append(args, "--", tmBin, ProbeCommand, sock, "127.0.0.1:1", filepath.Join(outside, "probe"))
	out, err := run(path, flagsEnv(), args...)
	switch last := lastLine(out); {
	case err != nil:
		return version, fmt.Errorf("codex sandbox refused it: %s", firstLine(out, err))
	case last == threadWant:
		return version, nil
	case strings.HasPrefix(last, "unix=refused"):
		return version, errors.New("tm's socket is unreachable from the sandbox")
	case strings.HasSuffix(last, "write=ok"):
		return version, errors.New("the sandbox doesn't apply: a file outside the workspace could be written")
	}
	return version, fmt.Errorf("unexpected probe output %q", firstLine(out, nil))
}
