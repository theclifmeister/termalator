package cli

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/theclifmeister/terminatr/internal/proto"
	"github.com/theclifmeister/terminatr/internal/server"
	"github.com/theclifmeister/terminatr/internal/service"
	"github.com/theclifmeister/terminatr/internal/version"
)

const serverUsage = `usage: tm server run [--detached] | start [--no-launchd] | stop [--yes] [--force] | restart [--yes] [--no-launchd] | status [--json]
       tm server service install|uninstall [--print]`

// serverCmd implements `tm server …` (docs/SPEC.md §3.1).
func serverCmd(e *Env, args []string) int {
	if len(args) == 0 {
		return e.srvUsage("server", serverUsage)
	}
	switch args[0] {
	case "run":
		return serverRun(e, args[1:])
	case "start":
		return serverStart(e, args[1:])
	case "stop":
		code := serverStop(e, args[1:])
		if code == ExitOK {
			unloadJob()
		}
		return code
	case "restart":
		var stop, start []string
		for _, a := range args[1:] {
			if a == "--no-launchd" || a == "-no-launchd" {
				start = append(start, a)
			} else {
				stop = append(stop, a)
			}
		}
		if code := serverStop(e, stop); code != ExitOK {
			return code
		}
		return serverStart(e, start)
	case "status":
		return serverStatus(e, args[1:])
	case "service":
		return serverService(e, args[1:])
	}
	return e.srvUsage("server", serverUsage)
}

func serverRun(e *Env, args []string) int {
	fs := flag.NewFlagSet("server run", flag.ContinueOnError)
	fs.SetOutput(e.Stderr)
	detached := fs.Bool("detached", false, "detach from the terminal and log to the server log")
	launchd := fs.Bool("launchd", false, "started by launchd (macOS): log to the server log")
	launchFile := fs.String("launch-file", "", "with --launchd: the file holding the environment to run with")
	if err := fs.Parse(args); err != nil {
		return ExitUsage
	}
	var source string
	if *launchFile != "" {
		// Before the paths: the starting tm's environment says where
		// they are.
		var err error
		if source, err = service.ApplyLaunchFile(*launchFile); err != nil {
			return e.srvFail("server run", err)
		}
	}
	p, err := server.ResolvePaths()
	if err != nil {
		return e.srvFail("server run", err)
	}
	logger := log.New(e.Stderr, "", log.LstdFlags|log.Lmicroseconds)
	sigs := []os.Signal{syscall.SIGTERM, syscall.SIGINT}
	if *launchd {
		// launchd owns the process: its stdout and stderr go to
		// logs/service.log, the server's log to the server log.
		lf, err := server.OpenLog(p)
		if err != nil {
			return e.srvFail("server run", err)
		}
		defer lf.Close()
		logger.SetOutput(lf)
		hup := make(chan os.Signal, 1)
		signal.Notify(hup, syscall.SIGHUP)
		go func() {
			for range hup {
			}
		}()
	} else if !*detached || !server.IsSessionLeader() {
		// Started by hand; a detached child of StartDetached logs it.
		warnSSH(e.Stderr, runtime.GOOS, e.Getenv)
	}
	if *detached {
		if !server.IsSessionLeader() {
			// Started by hand from a shell: get a fresh session first.
			if err := server.Respawn(); err != nil {
				return e.srvFail("server run", err)
			}
			return ExitOK
		}
		lf, err := server.OpenLog(p)
		if err != nil {
			return e.srvFail("server run", err)
		}
		defer lf.Close()
		logger.SetOutput(lf)
		if err := server.Detach(); err != nil {
			logger.Printf("detach: %v", err)
			return ExitIO
		}
		sigs = []os.Signal{syscall.SIGTERM}
	}
	ctx, stop := signal.NotifyContext(context.Background(), sigs...)
	defer stop()
	bin, _ := os.Executable()
	opts := server.Options{Paths: p, Log: logger, Bin: bin, Source: source, RunCLI: RunInServer}
	if len(os.Args) > 2 && os.Args[1] == "server" && os.Args[2] == "run" {
		// This process is `tm server run` (not a test calling in): the
		// server runs from its pin (docs/SPEC.md §3.6).
		opts.Exec, opts.Args = syscall.Exec, os.Args[1:]
	}
	err = server.Run(ctx, opts)
	var running *server.AlreadyRunningError
	if errors.As(err, &running) {
		logger.Printf("%v", err)
		if !*detached {
			fmt.Fprintf(e.Stderr, "tm server run: %v\n", err)
		}
		return ExitRefused
	}
	if err != nil {
		logger.Printf("server: %v", err)
		return ExitIO
	}
	return ExitOK
}

func serverStart(e *Env, args []string) int {
	fs := flag.NewFlagSet("server start", flag.ContinueOnError)
	fs.SetOutput(e.Stderr)
	direct := fs.Bool("no-launchd", false, "macOS: start the server from this session, not launchd's (no keychain over SSH)")
	if err := fs.Parse(args); err != nil || fs.NArg() > 0 {
		return e.srvUsage("server start", serverUsage)
	}
	c, p, err := connect(false)
	if err == nil {
		fmt.Fprintf(e.Stdout, "already running (pid %d)\n", c.Server.PID)
		c.Close()
		return ExitOK
	}
	if !errors.Is(err, server.ErrNotRunning) {
		return e.srvFail("server start", err)
	}
	start := server.StartDetached
	if *direct {
		start = server.StartChild
	}
	if err := start(p); err != nil {
		return e.srvFail("server start", err)
	}
	c, err = server.Dial(p, proto.KindControl)
	if err != nil {
		return e.srvFail("server start", err)
	}
	defer c.Close()
	fmt.Fprintf(e.Stdout, "started (pid %d)\n", c.Server.PID)
	if *direct || !service.Wanted(runtime.GOOS, e.Getenv) {
		warnSSH(e.Stderr, runtime.GOOS, e.Getenv)
	}
	return ExitOK
}

func serverStop(e *Env, args []string) int {
	fs := flag.NewFlagSet("server stop", flag.ContinueOnError)
	fs.SetOutput(e.Stderr)
	yes := fs.Bool("yes", false, "stop even while agent sessions run")
	force := fs.Bool("force", false, "SIGKILL a hung server (the pid that holds the lock)")
	if err := fs.Parse(args); err != nil {
		return ExitUsage
	}
	if *force {
		p, err := server.ResolvePaths()
		if err != nil {
			return e.srvFail("server stop", err)
		}
		pid, err := server.ForceKill(p)
		if errors.Is(err, server.ErrNotRunning) {
			fmt.Fprintln(e.Stdout, "not running")
			return ExitOK
		}
		if err != nil {
			return e.srvFail("server stop", err)
		}
		server.WaitStopped(p, 5*time.Second)
		os.Remove(p.Socket)
		os.Remove(p.PID)
		fmt.Fprintf(e.Stdout, "killed (pid %d)\n", pid)
		return ExitOK
	}
	p, err := server.ResolvePaths()
	if err != nil {
		return e.srvFail("server stop", err)
	}
	// Stop works whatever protocol the server speaks: restart is how a
	// server of another version is replaced, so it must never refuse on
	// version (docs/SPEC.md §3.3).
	st, err := server.Stop(p, *yes)
	var perr *proto.Error
	if errors.As(err, &perr) && perr.Code == proto.ErrRefused && !*yes && isTTY(os.Stdin) {
		fmt.Fprintf(e.Stderr, "%s. Stop anyway? [y/N] ", perr.Message)
		line, _ := bufio.NewReader(e.Stdin).ReadString('\n')
		if a := strings.TrimSpace(strings.ToLower(line)); a != "y" && a != "yes" {
			return ExitRefused
		}
		st, err = server.Stop(p, true)
	}
	if errors.Is(err, server.ErrNotRunning) {
		fmt.Fprintln(e.Stdout, "not running")
		return ExitOK
	}
	if err != nil {
		return e.srvFail("server stop", err)
	}
	pid := st.PID
	switch {
	case st.Signalled:
		fmt.Fprintf(e.Stderr, "tm server stop: the server (pid %d) could not be asked; sent it SIGTERM\n", pid)
	case st.Protocol != proto.Protocol:
		fmt.Fprintf(e.Stderr, "tm server stop: the server speaks protocol %d, this tm %d; stopped it all the same\n", st.Protocol, proto.Protocol)
	}
	// The lock goes first; the process exits a moment later.
	if !server.WaitStopped(p, server.StopGrace+5*time.Second) || !server.WaitExited(pid, 3*time.Second) {
		fmt.Fprintf(e.Stderr, "tm server stop: pid %d still running; see %s, or tm server stop --force kills it\n", pid, p.Log)
		return ExitIO
	}
	fmt.Fprintf(e.Stdout, "stopped (pid %d)\n", pid)
	return ExitOK
}

func serverStatus(e *Env, args []string) int {
	fs := flag.NewFlagSet("server status", flag.ContinueOnError)
	fs.SetOutput(e.Stderr)
	asJSON := fs.Bool("json", false, "print JSON")
	if err := fs.Parse(args); err != nil {
		return ExitUsage
	}
	c, _, err := connect(false)
	if errors.Is(err, server.ErrNotRunning) {
		if *asJSON {
			return e.srvJSON(map[string]bool{"running": false})
		}
		fmt.Fprintln(e.Stdout, "not running")
		return ExitRefused
	}
	if err != nil {
		return e.srvFail("server status", err)
	}
	defer c.Close()
	var st proto.ServerStatus
	if err := c.Call(proto.MethodServerStatus, nil, &st); err != nil {
		return e.srvFail("server status", err)
	}
	if *asJSON {
		return e.srvJSON(st)
	}
	fmt.Fprintf(e.Stdout, "pid       %d\n", st.PID)
	fmt.Fprintf(e.Stdout, "uptime    %s\n", time.Since(st.Started).Round(time.Second))
	fmt.Fprintf(e.Stdout, "version   %s (build %s)\n", st.Version, st.Build)
	fmt.Fprintf(e.Stdout, "protocol  %d\n", st.Protocol)
	fmt.Fprintf(e.Stdout, "sessions  %d\n", st.Sessions)
	fmt.Fprintf(e.Stdout, "socket    %s\n", st.Socket)
	fmt.Fprintf(e.Stdout, "home      %s\n", st.Home)
	if st.PreviousShutdown == "crash" {
		fmt.Fprintf(e.Stdout, "note      the previous server crashed; lost sessions: %s\n", strings.Join(st.Lost, " "))
	}
	if st.Build != version.BuildID() {
		fmt.Fprintf(e.Stderr, "note: the server runs another build than this tm (%s); 'tm server restart' switches it to this one\n", version.BuildID())
	}
	return ExitOK
}

// unloadJob boots out a dev or test home's on-demand launchd job after
// its server stopped, so it isn't left loaded (T71). Best effort.
func unloadJob() {
	p, err := server.ResolvePaths()
	if err != nil {
		return
	}
	if c, err := server.LaunchdConfig(p); err == nil {
		c.Unload()
	}
}
