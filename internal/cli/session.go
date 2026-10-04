package cli

import (
	"errors"
	"flag"
	"fmt"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/theclifmeister/termalator/internal/proto"
)

const sessionUsage = `usage: tm session list [--json]
       tm session start [--cwd DIR] [--cols N --rows N] [-- COMMAND ARGS…]
       tm session stop ID
       tm session read ID [--scrollback] [--json]   (the screen as plain text)
       tm session keys ID [--enter] [TEXT…]   (types TEXT literally; --enter presses Enter)`

// sessionCmd implements `tm session …`: plain sessions outside projects.
func sessionCmd(e *Env, args []string) int {
	if len(args) == 0 {
		return e.srvUsage("session", sessionUsage)
	}
	switch args[0] {
	case "list", "ls":
		return sessionList(e, args[1:])
	case "start":
		return sessionStart(e, args[1:])
	case "stop":
		return sessionStop(e, args[1:])
	case "read":
		return sessionRead(e, args[1:])
	case "keys":
		return sessionKeys(e, args[1:])
	}
	return e.srvUsage("session", sessionUsage)
}

func sessionList(e *Env, args []string) int {
	fs := flag.NewFlagSet("session list", flag.ContinueOnError)
	fs.SetOutput(e.Stderr)
	asJSON := fs.Bool("json", false, "print JSON")
	if err := fs.Parse(args); err != nil {
		return ExitUsage
	}
	c, _, err := connect(true)
	if err != nil {
		return e.srvFail("session list", err)
	}
	defer c.Close()
	var res proto.SessionListResult
	if err := c.Call(proto.MethodSessionList, nil, &res); err != nil {
		return e.srvFail("session list", err)
	}
	if *asJSON {
		return e.srvJSON(res.Sessions)
	}
	tw := tabwriter.NewWriter(e.Stdout, 0, 0, 2, ' ', 0)
	for _, s := range res.Sessions {
		fmt.Fprintf(tw, "%s\t%s\tpid %d\t%d×%d\t%s\t%s\t%s\n", s.ID, s.Role, s.PID, s.Cols, s.Rows,
			time.Since(s.Created).Round(time.Second), strings.Join(s.Argv, " "), s.Cwd)
	}
	tw.Flush()
	return ExitOK
}

func sessionStart(e *Env, args []string) int {
	fs := flag.NewFlagSet("session start", flag.ContinueOnError)
	fs.SetOutput(e.Stderr)
	cwd := fs.String("cwd", "", "working directory (default: the current one)")
	cols := fs.Uint("cols", 0, "columns (default: this terminal's, or 80)")
	rows := fs.Uint("rows", 0, "rows (default: this terminal's, or 24)")
	agent := fs.String("agent", "", "agent to run (arrives with the agent layer)")
	if err := fs.Parse(args); err != nil {
		return ExitUsage
	}
	if *agent != "" {
		return e.srvUsage("session start", "--agent is not supported yet; start a shell or pass a command after --")
	}
	if *cols > 0xffff || *rows > 0xffff {
		return e.srvUsage("session start", "size out of range")
	}
	p := proto.SessionStartParams{Argv: fs.Args(), Cols: uint16(*cols), Rows: uint16(*rows)}
	if p.Cols == 0 || p.Rows == 0 {
		if c, r, ok := termSize(); ok {
			p.Cols, p.Rows = c, r
		}
	}
	dir := *cwd
	if dir == "" {
		dir = e.Cwd
	}
	if dir != "" {
		abs, err := filepath.Abs(dir)
		if err != nil {
			return e.srvUsage("session start", err.Error())
		}
		p.Cwd = abs
	}
	c, _, err := connect(true)
	if err != nil {
		return e.srvFail("session start", err)
	}
	defer c.Close()
	var res proto.SessionStartResult
	if err := c.Call(proto.MethodSessionStart, p, &res); err != nil {
		return e.srvFail("session start", err)
	}
	fmt.Fprintln(e.Stdout, res.Session.ID)
	return ExitOK
}

func sessionStop(e *Env, args []string) int {
	if len(args) != 1 {
		return e.srvUsage("session stop", "usage: tm session stop ID")
	}
	c, _, err := connect(true)
	if err != nil {
		return e.srvFail("session stop", err)
	}
	defer c.Close()
	err = c.Call(proto.MethodSessionStop, proto.SessionIDParams{ID: args[0]}, nil)
	var perr *proto.Error
	if errors.As(err, &perr) && perr.Code == proto.ErrUnknownSession {
		// Idempotent: a session that is gone is stopped.
		fmt.Fprintf(e.Stdout, "%s not running\n", args[0])
		return ExitOK
	}
	if err != nil {
		return e.srvFail("session stop", err)
	}
	fmt.Fprintf(e.Stdout, "%s stopped\n", args[0])
	return ExitOK
}

func sessionRead(e *Env, args []string) int {
	fs := flag.NewFlagSet("session read", flag.ContinueOnError)
	fs.SetOutput(e.Stderr)
	scrollback := fs.Bool("scrollback", false, "include the scrollback")
	asJSON := fs.Bool("json", false, "print JSON")
	id, rest := splitID(args)
	if err := fs.Parse(rest); err != nil {
		return ExitUsage
	}
	if id == "" && fs.NArg() == 1 {
		id = fs.Arg(0)
	} else if id == "" || fs.NArg() > 0 {
		return e.srvUsage("session read", "usage: tm session read ID [--scrollback] [--json]")
	}
	c, _, err := connect(true)
	if err != nil {
		return e.srvFail("session read", err)
	}
	defer c.Close()
	var res proto.SessionReadResult
	if err := c.Call(proto.MethodSessionRead, proto.SessionReadParams{ID: id, Scrollback: *scrollback}, &res); err != nil {
		return e.srvFail("session read", err)
	}
	if *asJSON {
		return e.srvJSON(res)
	}
	fmt.Fprintln(e.Stdout, strings.TrimRight(res.Text, "\n"))
	return ExitOK
}

func sessionKeys(e *Env, args []string) int {
	fs := flag.NewFlagSet("session keys", flag.ContinueOnError)
	fs.SetOutput(e.Stderr)
	enter := fs.Bool("enter", false, "press Enter after the text (key names are not parsed: 'enter' types e-n-t-e-r)")
	id, rest := splitID(args)
	if err := fs.Parse(rest); err != nil {
		return ExitUsage
	}
	words := fs.Args()
	if id == "" && len(words) > 0 {
		id, words = words[0], words[1:]
	}
	if id == "" || (len(words) == 0 && !*enter) {
		return e.srvUsage("session keys", "usage: tm session keys ID [--enter] [TEXT…]   (TEXT is typed literally; --enter presses Enter)")
	}
	data := strings.Join(words, " ")
	if *enter {
		data += "\r"
	}
	c, _, err := connect(true)
	if err != nil {
		return e.srvFail("session keys", err)
	}
	defer c.Close()
	if err := c.Call(proto.MethodSessionKeys, proto.SessionKeysParams{ID: id, Data: data}, nil); err != nil {
		return e.srvFail("session keys", err)
	}
	return ExitOK
}

// splitID takes a leading positional id so flags may follow it
// (`tm session read s-1 --json`).
func splitID(args []string) (string, []string) {
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		return args[0], args[1:]
	}
	return "", args
}
