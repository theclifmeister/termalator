package cli

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"syscall"

	"golang.org/x/sys/unix"

	"github.com/theclifmeister/termalator/internal/project"
	"github.com/theclifmeister/termalator/internal/proto"
	"github.com/theclifmeister/termalator/internal/server"
	"github.com/theclifmeister/termalator/internal/tui"
)

const attachUsage = `usage: tm attach [SESSION]   (the newest session when none is named; Ctrl+\ detaches)`

// reexecEnv marks a tm that was re-executed as the server's binary, so a
// build mismatch that survives the re-exec fails instead of looping.
const reexecEnv = "TERMALATOR_REEXEC"

// attachLogEnv names a file for the attach client's diagnostics (digest
// checks, key encodings). Tests read it.
const attachLogEnv = "TERMALATOR_ATTACH_LOG"

func init() { commands["attach"] = runAttach }

func runAttach(e *Env, args []string) error { return codeErr(attachCmd(e, args)) }

// attachCmd implements `tm attach [SESSION]` (docs/SPEC.md §3.3).
func attachCmd(e *Env, args []string) int {
	fs := flag.NewFlagSet("attach", flag.ContinueOnError)
	fs.SetOutput(e.Stderr)
	fs.Usage = func() { fmt.Fprintln(e.Stderr, attachUsage) }
	if err := fs.Parse(args); err != nil {
		return ExitUsage
	}
	if fs.NArg() > 1 {
		return e.srvUsage("attach", attachUsage)
	}
	id := fs.Arg(0)
	if !isTTY(os.Stdin) {
		return e.srvUsage("attach", "stdin is not a terminal")
	}
	if rawMode(os.Stdin) {
		fmt.Fprintln(e.Stderr, "tm attach: this terminal looks like it was left in raw mode (a client killed with SIGKILL?); run `reset` if it misbehaves")
	}
	c, p, err := connect(false)
	if errors.Is(err, server.ErrNotRunning) {
		fmt.Fprintln(e.Stderr, "tm attach: tm server is not running; start a session with tm session start")
		return ExitRefused
	}
	if err != nil {
		return e.srvFail("attach", err)
	}
	if id == "" {
		var res proto.SessionListResult
		err := c.Call(proto.MethodSessionList, nil, &res)
		if err != nil {
			c.Close()
			return e.srvFail("attach", err)
		}
		if id = newest(res.Sessions); id == "" {
			c.Close()
			fmt.Fprintln(e.Stderr, "tm attach: no sessions; start one with tm session start")
			return ExitRefused
		}
	}
	c.Close()

	res, code := e.attach(p, id, false, []string{"attach", id})
	if code != ExitOK {
		return code
	}
	if res.Detached {
		fmt.Fprintf(e.Stdout, "[%s from %s]\n", res.Reason, id)
		return ExitOK
	}
	fmt.Fprintf(e.Stdout, "[%s: %s]\n", id, res.Reason)
	return ExitOK
}

// tookOver tells a thread's coordinator that the user took over the
// thread's pane (docs/SPEC.md §4): an inbox item and a journal line.
func (e *Env) tookOver(s proto.SessionInfo) error {
	p, err := project.Open(s.Project)
	if err != nil {
		return err
	}
	return p.TookOver(e.Caller, s.Thread)
}

// attach runs the attach view on this terminal until the user detaches
// or the session ends. On a build mismatch it re-execs the server's
// binary with args (docs/SPEC.md §3.3) and doesn't return.
func (e *Env) attach(p server.Paths, id string, statusBar bool, args []string) (tui.Result, int) {
	logger := log.New(io.Discard, "", 0)
	if path := e.Getenv(attachLogEnv); path != "" {
		if f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600); err == nil {
			defer f.Close()
			logger = log.New(f, fmt.Sprintf("attach %s pid %d: ", id, os.Getpid()), log.Lmicroseconds)
		}
	}
	res, err := tui.Attach(tui.Options{Paths: p, Session: id, In: os.Stdin, Out: os.Stdout, Log: logger, StatusBar: statusBar,
		Takeover: e.tookOver})
	var verr *proto.MismatchError
	if errors.As(err, &verr) && verr.ReExec && e.Getenv(reexecEnv) == "" {
		// The snapshot format is only stable within one build: become the
		// server's binary and attach again.
		logger.Printf("re-exec %s: %v", verr.Bin, err)
		argv := append([]string{verr.Bin}, args...)
		err = syscall.Exec(verr.Bin, argv, append(os.Environ(), reexecEnv+"=1"))
		return res, e.srvFail("attach", fmt.Errorf("re-exec %s: %w", verr.Bin, err))
	}
	if err != nil {
		return res, e.srvFail("attach", err)
	}
	return res, ExitOK
}

// newest returns the id of the most recently created session.
func newest(list []proto.SessionInfo) string {
	var best *proto.SessionInfo
	for i := range list {
		if best == nil || list[i].Created.After(best.Created) {
			best = &list[i]
		}
	}
	if best == nil {
		return ""
	}
	return best.ID
}

// rawMode reports whether f is a terminal with echo and line editing off.
func rawMode(f *os.File) bool {
	t, err := unix.IoctlGetTermios(int(f.Fd()), ioctlGetTermios)
	return err == nil && t.Lflag&(unix.ICANON|unix.ECHO) == 0
}
