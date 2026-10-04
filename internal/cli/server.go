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
	"strings"
	"syscall"
	"time"

	"github.com/theclifmeister/termalator/internal/proto"
	"github.com/theclifmeister/termalator/internal/server"
	"github.com/theclifmeister/termalator/internal/version"
)

const serverUsage = `usage: tm server run [--detached] | start | stop [--yes] [--force] | restart [--yes] | status [--json]`

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
		return serverStop(e, args[1:])
	case "restart":
		if code := serverStop(e, args[1:]); code != ExitOK {
			return code
		}
		return serverStart(e, nil)
	case "status":
		return serverStatus(e, args[1:])
	}
	return e.srvUsage("server", serverUsage)
}

func serverRun(e *Env, args []string) int {
	fs := flag.NewFlagSet("server run", flag.ContinueOnError)
	fs.SetOutput(e.Stderr)
	detached := fs.Bool("detached", false, "detach from the terminal and log to the server log")
	if err := fs.Parse(args); err != nil {
		return ExitUsage
	}
	p, err := server.ResolvePaths()
	if err != nil {
		return e.srvFail("server run", err)
	}
	logger := log.New(e.Stderr, "", log.LstdFlags|log.Lmicroseconds)
	sigs := []os.Signal{syscall.SIGTERM, syscall.SIGINT}
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
	err = server.Run(ctx, server.Options{Paths: p, Log: logger, Bin: bin, RunCLI: RunInServer})
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
	if len(args) > 0 {
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
	if err := server.StartDetached(p); err != nil {
		return e.srvFail("server start", err)
	}
	c, err = server.Dial(p, proto.KindControl)
	if err != nil {
		return e.srvFail("server start", err)
	}
	defer c.Close()
	fmt.Fprintf(e.Stdout, "started (pid %d)\n", c.Server.PID)
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
	c, p, err := connect(false)
	if errors.Is(err, server.ErrNotRunning) {
		fmt.Fprintln(e.Stdout, "not running")
		return ExitOK
	}
	if err != nil {
		return e.srvFail("server stop", err)
	}
	defer c.Close()
	pid := c.Server.PID
	err = c.Call(proto.MethodServerStop, proto.ServerStopParams{Yes: *yes}, nil)
	var perr *proto.Error
	if errors.As(err, &perr) && perr.Code == proto.ErrRefused && !*yes && isTTY(os.Stdin) {
		fmt.Fprintf(e.Stderr, "%s. Stop anyway? [y/N] ", perr.Message)
		line, _ := bufio.NewReader(e.Stdin).ReadString('\n')
		if a := strings.TrimSpace(strings.ToLower(line)); a != "y" && a != "yes" {
			return ExitRefused
		}
		err = c.Call(proto.MethodServerStop, proto.ServerStopParams{Yes: true}, nil)
	}
	if err != nil {
		return e.srvFail("server stop", err)
	}
	// The lock goes first; the process exits a moment later.
	if !server.WaitStopped(p, server.StopGrace+5*time.Second) || !server.WaitExited(pid, 3*time.Second) {
		fmt.Fprintf(e.Stderr, "tm server stop: pid %d still running; see %s\n", pid, p.Log)
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
