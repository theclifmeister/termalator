// Package doctor implements `tm doctor` (docs/SPEC.md §15 M8): checks of
// the toolchain, the server and its run dir (and, on macOS, whether its
// sessions can reach the keychain), the agents against their
// manifests' tested_versions, enabled Claude plugins known to be unsafe in
// tm's sessions, the sandbox prerequisites, and leftovers of
// threads (worktrees and merged branches nobody uses any more), and
// settings in config.toml that tm no longer has.
//
// Checks only look. Each problem that can be repaired carries a Fix,
// which `tm doctor --fix` applies after the human confirms.
package doctor

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/theclifmeister/terminatr/internal/codehost"
	"github.com/theclifmeister/terminatr/internal/keychain"
	"github.com/theclifmeister/terminatr/internal/project"
	"github.com/theclifmeister/terminatr/internal/proto"
	"github.com/theclifmeister/terminatr/internal/server"
	"github.com/theclifmeister/terminatr/internal/service"
	"github.com/theclifmeister/terminatr/internal/update"
)

// Status of one check.
type Status string

const (
	OK   Status = "ok"
	Warn Status = "warn"
	Fail Status = "fail"
)

// Check is one result line.
type Check struct {
	Group  string `json:"group"`
	Name   string `json:"name"`
	Status Status `json:"status"`
	Detail string `json:"detail,omitempty"`
	// Source says where a code-host check ran: "server" (in the running
	// server's context, as its sessions see it) or "local" (in this
	// process, no server answered). Empty for every other check.
	Source string `json:"source,omitempty"`
	// Fix repairs the problem; nil when there is nothing tm may do.
	Fix *Fix `json:"fix,omitempty"`
}

// Fix is one repair `tm doctor --fix` may do: a removal, or restarting a
// server this tm can't talk to.
type Fix struct {
	Desc  string       `json:"desc"`
	Apply func() error `json:"-"`
}

// Deps is everything the checks touch outside the files under Paths, so
// tests can replace it.
type Deps struct {
	Paths server.Paths
	GOOS  string
	// LookPath finds a program on PATH.
	LookPath func(string) (string, error)
	// Run runs a program in dir and returns its combined, trimmed output.
	Run func(dir, name string, args ...string) (string, error)
	// Version and Build are this tm's, to compare with the server's.
	Version, Build string
	// Selftest checks that libghostty-vt is linked and works; nil skips.
	Selftest func() error
	// Install is how this tm was installed; nil skips the install checks.
	Install *update.Install
	// Latest returns the latest release's tag; nil skips that check.
	Latest func() (string, error)
	// Restart restarts the server as `tm server restart --yes` does;
	// nil offers no restart.
	Restart func() error
	// Keychain probes whether this process can reach the macOS login
	// keychain (keychain.Probe); nil offers no restart for a server that
	// can't.
	Keychain func() proto.KeychainStatus
	// Launchd reports whether a restart from here starts the server
	// through launchd's GUI domain (macOS), and so gives its sessions
	// the keychain wherever doctor runs; nil means it doesn't.
	Launchd func() bool
	// Strays lists loaded launchd jobs named like the server's that no
	// server needs, and Bootout unloads one by label (macOS); nil skips.
	Strays  func() ([]service.Stray, error)
	Bootout func(label string) error
	// Hosts lists the project's repos in use with the code host each
	// one's PRs live on (codehost.Detect); empty means GitHub, as for a
	// fresh install.
	Hosts func() []RepoHost
	// Getenv reads the environment (AZURE_DEVOPS_EXT_PAT); nil reads none.
	Getenv func(string) string
	// PATGet asks Azure DevOps a URL with a PAT; nil is codehost.PATGet.
	PATGet func(url, pat string) ([]byte, error)
}

// RepoHost is one repo in use and where its PRs live.
type RepoHost = codehost.RepoHost

// DefaultDeps uses the real system.
func DefaultDeps(p server.Paths, version, build string) Deps {
	return Deps{Paths: p, GOOS: runtime.GOOS, LookPath: exec.LookPath, Run: run, Version: version, Build: build, Getenv: os.Getenv, Hosts: usedHosts,
		Keychain: func() proto.KeychainStatus { return keychain.Probe(runtime.GOOS, os.Getenv, keychain.Run) }}
}

// cmdTimeout bounds every program doctor runs (agent --version, git).
const cmdTimeout = 10 * time.Second

func run(dir, name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), cmdTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	s := strings.TrimSpace(out.String())
	if err != nil {
		if s == "" {
			s = err.Error()
		}
		return s, fmt.Errorf("%s %s: %s", name, strings.Join(args, " "), firstLine(s))
	}
	return s, nil
}

func firstLine(s string) string {
	s, _, _ = strings.Cut(strings.TrimSpace(s), "\n")
	return s
}

// Run runs every check, in a stable order.
func Run(d Deps) []Check {
	var out []Check
	srv, live := Server(d)
	out = append(out, toolchain(d)...)
	out = append(out, codeHosts(d, live)...)
	out = append(out, Install(d)...)
	out = append(out, srv...)
	out = append(out, Agents(d)...)
	out = append(out, Plugins(d)...)
	out = append(out, Sandbox(d)...)
	out = append(out, Leftovers(d, live)...)
	out = append(out, Launchd(d)...)
	out = append(out, Settings(d)...)
	out = append(out, Upkeep(d)...)
	return out
}

// Worst is the worst status among checks.
func Worst(cs []Check) Status {
	w := OK
	for _, c := range cs {
		switch {
		case c.Status == Fail:
			return Fail
		case c.Status == Warn:
			w = Warn
		}
	}
	return w
}

// Fixes lists the fixes among checks.
func Fixes(cs []Check) []*Fix {
	var out []*Fix
	for _, c := range cs {
		if c.Fix != nil {
			out = append(out, c.Fix)
		}
	}
	return out
}

// Toolchain checks this binary and the programs threads rely on, the
// code host's CLIs in this process's context (no server asked).
func Toolchain(d Deps) []Check {
	return append(toolchain(d), codeHosts(d, Live{})...)
}

func toolchain(d Deps) []Check {
	const g = "toolchain"
	out := []Check{{Group: g, Name: "tm", Status: OK, Detail: d.Version + " build " + d.Build}}
	if d.Selftest != nil {
		if err := d.Selftest(); err != nil {
			out = append(out, Check{Group: g, Name: "libghostty-vt", Status: Fail, Detail: err.Error()})
		} else {
			out = append(out, Check{Group: g, Name: "libghostty-vt", Status: OK, Detail: "linked"})
		}
	}
	if p, err := d.LookPath("git"); err != nil {
		out = append(out, Check{Group: g, Name: "git", Status: Warn, Detail: "not found; threads need git for their worktrees"})
	} else {
		v, _ := d.Run("", p, "--version")
		out = append(out, Check{Group: g, Name: "git", Status: OK, Detail: strings.TrimPrefix(firstLine(v), "git version ")})
	}
	return out
}

// codeHosts shows the CLI, login and access of each code host kind among
// the repos in use, whose PR polls fail without them (a gh-failing inbox
// item, §7.5). With a server running they are its results (server.codehost):
// its context, not this shell's, is what the sessions' gh, az and git
// have; over SSH on a Mac this shell has no login keychain and would
// warn about a gh that works fine there. Where this shell fails what the
// server passes, a "local shell" note says so instead of a warning. With
// no server (or an older one) the checks run here, labelled local.
func codeHosts(d Deps, live Live) []Check {
	const g = "toolchain"
	var hosts []RepoHost
	if d.Hosts != nil {
		hosts = d.Hosts()
	}
	dd := codehost.DoctorDeps{LookPath: d.LookPath, Run: d.Run, Getenv: d.Getenv, PATGet: d.PATGet}
	convert := func(cs []codehost.Check, source string) []Check {
		var out []Check
		for _, c := range cs {
			st := Warn
			if c.OK {
				st = OK
			}
			out = append(out, Check{Group: g, Name: c.Name, Status: st, Detail: c.Detail, Source: source})
		}
		return out
	}
	local := convert(codehost.Checks(dd, hosts), "local")
	if live.CodeHost == nil {
		return local
	}
	var out []Check
	passed := map[string]bool{}
	for _, c := range live.CodeHost.Checks {
		st := Warn
		if c.OK {
			st = OK
			passed[c.Name] = true
		}
		out = append(out, Check{Group: g, Name: c.Name, Status: st, Detail: c.Detail, Source: "server"})
	}
	var differs []string
	for _, c := range local {
		if c.Status != OK && passed[c.Name] {
			differs = append(differs, c.Name)
		}
	}
	if len(differs) == 0 {
		return out
	}
	why := "this shell fails " + strings.Join(differs, ", ") + " (its environment differs from the server's)"
	if d.Keychain != nil && d.GOOS == "darwin" {
		if ks := d.Keychain(); ks.Checked && !ks.OK {
			why = "this shell can't reach the keychain"
			if ks.OverSSH {
				why += " (SSH)"
			}
			why += ": " + strings.Join(differs, ", ") + " fail here"
		}
	}
	return append(out, Check{Group: g, Name: "local shell", Status: OK, Detail: why + "; the server's sessions pass", Source: "local"})
}

// usedHosts is every repo of every project, with its code host.
func usedHosts() []RepoHost { return project.CodeHosts() }

// Install reports how this tm was installed and whether a newer release
// exists (`tm update`).
func Install(d Deps) []Check {
	const g = "install"
	if d.Install == nil {
		return nil
	}
	in := *d.Install
	out := []Check{{Group: g, Name: "method", Status: OK, Detail: string(in.Method) + ", " + in.Path}}
	if d.Latest == nil {
		return out
	}
	latest, err := d.Latest()
	switch {
	case errors.Is(err, update.ErrOff):
	case err != nil:
		out = append(out, Check{Group: g, Name: "release", Status: Warn, Detail: "couldn't check: " + firstLine(err.Error())})
	default:
		newer, ok := update.Newer(latest, d.Version)
		switch {
		case !ok:
			out = append(out, Check{Group: g, Name: "release", Status: OK, Detail: "latest is " + latest})
		case newer:
			hint := "tm update"
			switch in.Method {
			case update.Homebrew:
				hint = in.Upgrade
			case update.Dev:
				hint = "git pull && make"
			}
			out = append(out, Check{Group: g, Name: "release", Status: Warn, Detail: latest + " is available: " + hint})
		default:
			out = append(out, Check{Group: g, Name: "release", Status: OK, Detail: "up to date (" + latest + ")"})
		}
	}
	return out
}

// Launchd warns about loaded dev.terminatr.server.* jobs no server needs
// (a test or dev home's, left behind); the fix boots them out.
func Launchd(d Deps) []Check {
	const g = "launchd"
	if d.Strays == nil {
		return nil
	}
	strays, err := d.Strays()
	if err != nil {
		return []Check{{Group: g, Name: "jobs", Status: Warn, Detail: "can't list launchd jobs: " + err.Error()}}
	}
	if len(strays) == 0 {
		return []Check{{Group: g, Name: "jobs", Status: OK, Detail: "no stray dev.terminatr.server.* jobs"}}
	}
	var out []Check
	for _, s := range strays {
		c := Check{Group: g, Name: "stray job", Status: Warn, Detail: s.Label + ": " + s.Reason}
		if d.Bootout != nil {
			label := s.Label
			c.Fix = &Fix{Desc: "boot out launchd job " + label, Apply: func() error { return d.Bootout(label) }}
		}
		out = append(out, c)
	}
	return out
}
