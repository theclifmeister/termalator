package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/theclifmeister/termilator/internal/caller"
	"github.com/theclifmeister/termilator/internal/tasks"
)

// Exit codes (docs/SPEC.md §6.3), shared by every command.
const (
	ExitOK      = 0 // done, or already true
	ExitRefused = 1 // refused: some items failed, valid ones were saved
	ExitUsage   = 2 // usage error; nothing saved
	ExitIO      = 3 // I/O or server error; check with list and retry
)

// Env is everything a command touches outside the files, so tests can run
// commands in-process.
type Env struct {
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
	Getenv func(string) string
	Cwd    string
	Caller caller.Caller
	// Identify asks the server who the caller is (§11.1); ok is false
	// when no server answers. Nil skips the check.
	Identify func() (c caller.Caller, ok bool)
}

// OSEnv is the process's real environment.
func OSEnv() *Env {
	cwd, _ := os.Getwd()
	return &Env{
		Stdin: os.Stdin, Stdout: os.Stdout, Stderr: os.Stderr,
		Getenv: os.Getenv, Cwd: cwd, Caller: caller.FromEnv(),
		Identify: identifyByServer,
	}
}

// Run runs a tm subcommand from this package with the process's
// environment. handled is false if args name no command of this package,
// so cmd/tm can try others.
//
// An agent's project command runs inside the server (forward), which
// tells the caller from the process tree and writes the project folder
// that a sandboxed thread can't. Without a server it runs here.
func Run(args []string) (code int, handled bool) {
	e := OSEnv()
	if len(args) > 0 && forwarded[args[0]] && e.Getenv(caller.EnvSession) != "" {
		if code, ok := e.forward(args); ok {
			return code, true
		}
	}
	return e.Run(args)
}

type command func(e *Env, args []string) error

var commands = map[string]command{
	"project": runProject,
	"task":    runTask,
	"context": runContext,
	"inbox":   runInbox,
	"skill":   runSkill,
	"thread":  runThread,
	"report":  runReport,
	"status":  runStatus,
	"done":    runDone,
}

// checked are the commands whose rights depend on the caller: for them
// the server's view of the caller is combined with the environment's.
var checked = map[string]bool{"project": true, "task": true, "inbox": true}

// Run dispatches args (without the program name).
func (e *Env) Run(args []string) (code int, handled bool) {
	if len(args) == 0 || len(args) == 1 && args[0] == "--own" {
		return e.dashboardCmd(len(args) == 1), true
	}
	cmd, ok := commands[args[0]]
	if !ok {
		return 0, false
	}
	if checked[args[0]] && e.Identify != nil {
		if c, ok := e.Identify(); ok {
			e.Caller = caller.Narrower(e.Caller, c)
		}
	}
	return e.report("tm "+args[0], cmd(e, args[1:])), true
}

// usageError is exit 2: the command line was wrong and nothing was saved.
type usageError struct{ msg string }

func (u *usageError) Error() string { return u.msg }

func usagef(format string, a ...any) error { return &usageError{fmt.Sprintf(format, a...)} }

// exitError carries a code for an outcome that was already printed, such
// as a bulk add with some failed items.
type exitError struct{ code int }

func (x *exitError) Error() string { return fmt.Sprintf("exit %d", x.code) }

// report prints err and maps it to an exit code.
func (e *Env) report(prefix string, err error) int {
	var u *usageError
	var r *tasks.Error
	var x *exitError
	switch {
	case err == nil:
		return ExitOK
	case errors.As(err, &x):
		return x.code
	case errors.As(err, &u):
		fmt.Fprintf(e.Stderr, "%s: %s\n", prefix, u.msg)
		return ExitUsage
	case errors.As(err, &r):
		fmt.Fprintf(e.Stderr, "%s: %s: %s\n", prefix, r.Code, r.Msg)
		return ExitRefused
	default:
		fmt.Fprintf(e.Stderr, "%s: %v\n", prefix, err)
		return ExitIO
	}
}

func (e *Env) printJSON(v any) error {
	enc := json.NewEncoder(e.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// flagSet is a small parser that, unlike package flag, accepts flags
// after positional arguments ("tm task status T12 blocked --note x").
type flagSet struct {
	bools map[string]*bool
	strs  map[string]*string
	lists map[string]*[]string
	set   map[string]bool
}

func newFlags() *flagSet {
	return &flagSet{bools: map[string]*bool{}, strs: map[string]*string{}, lists: map[string]*[]string{}, set: map[string]bool{}}
}

func (f *flagSet) Bool(name string) *bool     { v := new(bool); f.bools[name] = v; return v }
func (f *flagSet) String(name string) *string { v := new(string); f.strs[name] = v; return v }
func (f *flagSet) List(name string) *[]string { v := new([]string); f.lists[name] = v; return v }
func (f *flagSet) IsSet(name string) bool     { return f.set[name] }
func (f *flagSet) anySet(names ...string) bool {
	for _, n := range names {
		if f.set[n] {
			return true
		}
	}
	return false
}

// Parse returns the positional arguments. "--" ends flag parsing.
func (f *flagSet) Parse(args []string) ([]string, error) {
	var pos []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			return append(pos, args[i+1:]...), nil
		}
		if !strings.HasPrefix(a, "-") || a == "-" {
			pos = append(pos, a)
			continue
		}
		name, val, hasVal := strings.Cut(strings.TrimLeft(a, "-"), "=")
		if b, ok := f.bools[name]; ok {
			if hasVal {
				return nil, usagef("--%s takes no value", name)
			}
			*b, f.set[name] = true, true
			continue
		}
		s, isStr := f.strs[name]
		l, isList := f.lists[name]
		if !isStr && !isList {
			return nil, usagef("unknown flag %s", a)
		}
		if !hasVal {
			if i+1 >= len(args) {
				return nil, usagef("--%s needs a value", name)
			}
			i++
			val = args[i]
		}
		f.set[name] = true
		if isStr {
			*s = val
		} else {
			*l = append(*l, val)
		}
	}
	return pos, nil
}

// readArg reads a file argument; "-" is stdin.
func (e *Env) readArg(path string) (string, error) {
	var data []byte
	var err error
	if path == "-" {
		data, err = io.ReadAll(e.Stdin)
	} else {
		data, err = os.ReadFile(e.abs(path))
	}
	if err != nil {
		return "", usagef("%v", err)
	}
	return string(data), nil
}

// abs resolves a path argument against the caller's cwd, which inside the
// server is not the process's own.
func (e *Env) abs(path string) string {
	if filepath.IsAbs(path) || e.Cwd == "" {
		return path
	}
	return filepath.Join(e.Cwd, path)
}
