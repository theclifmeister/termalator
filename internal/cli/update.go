package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/theclifmeister/termilator/internal/proto"
	"github.com/theclifmeister/termilator/internal/update"
	"github.com/theclifmeister/termilator/internal/version"
)

const updateUsage = `usage: tm update [--check] [--yes] [--restart] [--json]`

func init() {
	commands["update"] = func(e *Env, args []string) error { return codeErr(updateCmd(e, args, defaultUpdater(e))) }
}

// updater is everything `tm update` touches outside its arguments, so
// tests can replace it.
type updater struct {
	Exe, Channel, Version, Team string
	GOOS, GOARCH                string
	Client                      *update.Client
	// Verify checks a downloaded binary (macOS: codesign); nil skips.
	Verify update.Verifier
	// TTY says whether stdin is a terminal, for the questions.
	TTY bool
	// RunVersion runs `<bin> version` and returns its output.
	RunVersion func(bin string) (string, error)
	// Brew runs brew with args, on the user's terminal.
	Brew func(args ...string) error
	// Server describes the running server; ok is false when none runs.
	// Sessions is -1 for a server of an older protocol, which only says
	// hello.
	Server func() (st proto.ServerStatus, ok bool)
	// Restart runs `<bin> server restart [--yes]` on the user's terminal.
	Restart func(bin string, yes bool) int
}

func defaultUpdater(e *Env) *updater {
	exe, _ := os.Executable()
	interactive := func(name string, args ...string) *exec.Cmd {
		cmd := exec.Command(name, args...)
		cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, e.Stdout, e.Stderr
		return cmd
	}
	return &updater{
		Exe: exe, Channel: version.Channel, Version: version.Version, Team: version.TeamID,
		GOOS: runtime.GOOS, GOARCH: runtime.GOARCH,
		Client: update.NewClient(e.Getenv),
		Verify: update.DefaultVerifier(),
		TTY:    isTTY(os.Stdin),
		RunVersion: func(bin string) (string, error) {
			out, err := exec.Command(bin, "version").CombinedOutput()
			return strings.TrimSpace(string(out)), err
		},
		Brew: func(args ...string) error { return interactive("brew", args...).Run() },
		Server: func() (proto.ServerStatus, bool) {
			c, _, err := connect(false)
			var verr *proto.MismatchError
			if errors.As(err, &verr) {
				// An older server: the hello says enough to offer the
				// restart, which works whatever its protocol.
				h := verr.Server
				return proto.ServerStatus{PID: h.PID, Version: h.Version, Build: h.Build, Protocol: h.Protocol, Sessions: -1}, true
			}
			if err != nil {
				return proto.ServerStatus{}, false
			}
			defer c.Close()
			var st proto.ServerStatus
			if c.Call(proto.MethodServerStatus, nil, &st) != nil {
				return proto.ServerStatus{}, false
			}
			return st, true
		},
		Restart: func(bin string, yes bool) int {
			args := []string{"server", "restart"}
			if yes {
				args = append(args, "--yes")
			}
			err := interactive(bin, args...).Run()
			var x *exec.ExitError
			if errors.As(err, &x) {
				return x.ExitCode()
			}
			if err != nil {
				fmt.Fprintf(e.Stderr, "tm update: %v\n", err)
				return ExitIO
			}
			return ExitOK
		},
	}
}

// updateCheck is `tm update --check --json`.
type updateCheck struct {
	update.Install
	Current string `json:"current"`
	Latest  string `json:"latest,omitempty"`
	Newer   bool   `json:"newer"`
	Error   string `json:"error,omitempty"`
}

// updateCmd implements `tm update` (docs/SPEC.md §10.1). It never stops
// the server on its own: the old server keeps running its binary, and
// restarting it is a question on a terminal or an explicit --restart.
func updateCmd(e *Env, args []string, u *updater) int {
	fs := flag.NewFlagSet("update", flag.ContinueOnError)
	fs.SetOutput(e.Stderr)
	check := fs.Bool("check", false, "only report whether a newer release exists")
	yes := fs.Bool("yes", false, "install without asking")
	restart := fs.Bool("restart", false, "restart the server afterwards (agents are resumed)")
	asJSON := fs.Bool("json", false, "with --check: print JSON")
	if err := fs.Parse(args); err != nil || fs.NArg() > 0 || *asJSON && !*check {
		return e.srvUsage("update", updateUsage)
	}
	in := update.Detect(u.Exe, u.Channel)
	res := updateCheck{Install: in, Current: u.Version}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	var rel update.Release
	if in.Method != update.Dev || *check {
		var err error
		if rel, err = u.Client.Latest(ctx); err != nil {
			res.Error = err.Error()
		} else {
			res.Latest = rel.Tag
			res.Newer, _ = update.Newer(rel.Tag, u.Version)
		}
	}
	if *check {
		if *asJSON {
			return e.srvJSON(res)
		}
		fmt.Fprintf(e.Stdout, "installed  %s (%s, %s)\n", u.Version, in.Method, in.Path)
		switch {
		case res.Error != "":
			fmt.Fprintf(e.Stdout, "latest     unknown: %s\n", res.Error)
			return ExitIO
		case res.Newer:
			fmt.Fprintf(e.Stdout, "latest     %s: %s\n", rel.Tag, updateHint(in))
		default:
			fmt.Fprintf(e.Stdout, "latest     %s: up to date\n", rel.Tag)
		}
		return ExitOK
	}

	switch in.Method {
	case update.Dev:
		fmt.Fprintf(e.Stderr, "tm update: this tm (%s) was built from source; update it with git pull && make\n", u.Version)
		return ExitRefused
	case update.Homebrew:
		if res.Error == "" && !res.Newer {
			fmt.Fprintf(e.Stdout, "tm %s is the latest release\n", u.Version)
			return ExitOK
		}
		// Homebrew owns its files: never replace them, let brew do it.
		fmt.Fprintf(e.Stdout, "tm was installed with Homebrew (%s); it updates with:\n  %s\n", in.Path, in.Upgrade)
		if !*yes && !(u.TTY && ask(e, "Run it now? [y/N] ")) {
			return ExitOK
		}
		if err := u.Brew("upgrade", update.Formula); err != nil {
			fmt.Fprintf(e.Stderr, "tm update: brew upgrade: %v\n", err)
			return ExitIO
		}
		return u.afterUpdate(e, brewBin(in.Path), res.Latest, *restart)
	}

	if res.Error != "" {
		fmt.Fprintf(e.Stderr, "tm update: %s\n", res.Error)
		return ExitIO
	}
	if !res.Newer {
		fmt.Fprintf(e.Stdout, "tm %s is the latest release\n", u.Version)
		return ExitOK
	}
	fmt.Fprintf(e.Stdout, "tm %s is available (installed: %s at %s)\n", rel.Tag, u.Version, in.Path)
	if !*yes {
		if !u.TTY {
			fmt.Fprintln(e.Stderr, "tm update: not a terminal; pass --yes to install it")
			return ExitRefused
		}
		if !ask(e, "Install it? [y/N] ") {
			fmt.Fprintln(e.Stdout, "nothing changed")
			return ExitRefused
		}
	}
	tmp, err := u.Client.Download(ctx, rel, u.GOOS, u.GOARCH, filepath.Dir(in.Path))
	if err != nil {
		if errors.Is(err, os.ErrPermission) {
			err = fmt.Errorf("%w: %s is not writable; reinstall where you can write, or update as its owner", err, filepath.Dir(in.Path))
		}
		fmt.Fprintf(e.Stderr, "tm update: %v\n", err)
		return ExitIO
	}
	defer os.Remove(tmp) // gone after the rename; cleans up on failure
	fmt.Fprintf(e.Stdout, "checksum   ok (%s)\n", update.ArchiveName(u.GOOS, u.GOARCH))
	if u.Verify != nil {
		team, err := u.Verify(tmp, u.Team)
		if err != nil {
			fmt.Fprintf(e.Stderr, "tm update: the downloaded tm fails the signature check: %v; nothing changed\n", err)
			return ExitRefused
		}
		fmt.Fprintf(e.Stdout, "signature  ok (Developer ID, team %s)\n", team)
	}
	if out, err := u.RunVersion(tmp); err != nil || !strings.Contains(out, rel.Tag) {
		fmt.Fprintf(e.Stderr, "tm update: the downloaded tm doesn't run as %s (%v): %s; nothing changed\n", rel.Tag, err, out)
		return ExitRefused
	}
	if err := update.Replace(tmp, in.Path); err != nil {
		fmt.Fprintf(e.Stderr, "tm update: %v\n", err)
		return ExitIO
	}
	fmt.Fprintf(e.Stdout, "installed  %s at %s\n", rel.Tag, in.Path)
	return u.afterUpdate(e, in.Path, rel.Tag, *restart)
}

// brewBin is the tm Homebrew links into its prefix's bin, which names
// the newest keg after an upgrade: .../Cellar/termilator/0.2.0/bin/tm
// becomes .../bin/tm.
func brewBin(keg string) string {
	if prefix, _, ok := strings.Cut(keg, "/Cellar/"); ok {
		return filepath.Join(prefix, "bin", "tm")
	}
	return keg
}

// afterUpdate deals with a server that still runs the old binary: it
// says so, and restarts it only on --restart or a y on a terminal,
// because a restart ends the agents' running turns.
func (u *updater) afterUpdate(e *Env, bin, newVersion string, restart bool) int {
	st, ok := u.Server()
	if !ok || newVersion != "" && st.Version == newVersion {
		return ExitOK
	}
	sessions := plural(st.Sessions, "session")
	if st.Sessions < 0 {
		sessions = "its sessions"
	}
	fmt.Fprintf(e.Stdout, "\nThe server (pid %d) still runs %s with %s; it keeps the old binary until it restarts.\n",
		st.PID, st.Version, sessions)
	fmt.Fprintln(e.Stdout, "Restarting stops every session: agents are resumed but lose the turn they are in, shells are lost.")
	switch {
	case restart:
		return u.Restart(bin, true)
	case u.TTY && ask(e, "Restart the server now? [y/N] "):
		// No --yes: the restart itself still asks when agents are working.
		return u.Restart(bin, false)
	}
	fmt.Fprintln(e.Stdout, "Restart it when convenient: tm server restart")
	return ExitOK
}

func updateHint(in update.Install) string {
	switch in.Method {
	case update.Homebrew:
		return in.Upgrade
	case update.Dev:
		return "git pull && make"
	}
	return "tm update"
}

// ask prints a question on stderr and reads a y/yes. It reads byte by
// byte, so a second question still finds its own line on stdin.
func ask(e *Env, q string) bool {
	fmt.Fprint(e.Stderr, q)
	var line []byte
	b := make([]byte, 1)
	for {
		n, err := e.Stdin.Read(b)
		if n == 1 && b[0] != '\n' {
			line = append(line, b[0])
		}
		if err != nil || n == 1 && b[0] == '\n' {
			break
		}
	}
	a := strings.ToLower(strings.TrimSpace(string(line)))
	return a == "y" || a == "yes"
}
