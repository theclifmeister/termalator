// Package doctor implements `tm doctor` (docs/SPEC.md §15 M8): checks of
// the toolchain, the server and its run dir (and, on macOS, whether its
// sessions can reach the keychain), the agents against their
// manifests' tested_versions, the sandbox prerequisites, and leftovers of
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

	"github.com/theclifmeister/termilator/internal/keychain"
	"github.com/theclifmeister/termilator/internal/proto"
	"github.com/theclifmeister/termilator/internal/server"
	"github.com/theclifmeister/termilator/internal/update"
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
}

// DefaultDeps uses the real system.
func DefaultDeps(p server.Paths, version, build string) Deps {
	return Deps{Paths: p, GOOS: runtime.GOOS, LookPath: exec.LookPath, Run: run, Version: version, Build: build,
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
	out = append(out, Toolchain(d)...)
	out = append(out, Install(d)...)
	srv, live := Server(d)
	out = append(out, srv...)
	out = append(out, Agents(d)...)
	out = append(out, Sandbox(d)...)
	out = append(out, Leftovers(d, live)...)
	out = append(out, Settings(d)...)
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

// Toolchain checks this binary and the programs threads rely on.
func Toolchain(d Deps) []Check {
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
	if _, err := d.LookPath("gh"); err != nil {
		out = append(out, Check{Group: g, Name: "gh", Status: Warn, Detail: "not found; tm thread resolve can't tell whether a PR was merged"})
	} else {
		out = append(out, Check{Group: g, Name: "gh", Status: OK, Detail: "found"})
	}
	return out
}

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
