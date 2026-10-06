package cli

import (
	"errors"
	"flag"
	"fmt"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/theclifmeister/terminatr/internal/proto"
)

const sessionUsage = `usage: tm session list [--json]
       tm session start [--cwd DIR] [--cols N --rows N] [-- COMMAND ARGS…]
       tm session start --agent NAME [--cwd DIR] [--brief FILE] [--kickoff TEXT] [--model M] [--yolo]
       tm session stop ID
       tm session read ID [--scrollback] [--json]   (the screen as plain text)
       tm session keys ID [--enter] [TEXT…]   (types TEXT literally; --enter presses Enter)
       tm session prompt ID TEXT   (pasted once the agent is idle)
       tm session wait ID [--state S[,S…]] [--timeout 30s]`

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
	case "prompt":
		return sessionPrompt(e, args[1:])
	case "wait":
		return sessionWait(e, args[1:])
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
		what := strings.Join(s.Argv, " ")
		if s.Agent != "" {
			what = s.Agent + " " + stateLine(s)
		}
		// A thread's session names its task, the thread id in brackets.
		role := s.Role
		switch {
		case s.Task != "":
			role += " " + s.Task + " (" + s.Thread + ")"
		case s.Thread != "":
			role += " " + s.Thread
		}
		fmt.Fprintf(tw, "%s\t%s\tpid %d\t%d×%d\t%s\t%s\t%s\n", s.ID, role, s.PID, s.Cols, s.Rows,
			time.Since(s.Created).Round(time.Second), what, s.Cwd)
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
	agentName := fs.String("agent", "", "agent to run, from tm agent list (instead of a command)")
	brief := fs.String("brief", "", "file given to the agent as its brief (system prompt)")
	kickoff := fs.String("kickoff", "", "the agent's first prompt")
	model := fs.String("model", "", "the agent's model")
	yolo := fs.Bool("yolo", false, "skip the agent's own permission prompts")
	role := fs.String("role", "", "coordinator, thread or shell (default)")
	project := fs.String("project", "", "project slug, for coordinator and thread roles")
	thread := fs.String("thread", "", "thread id, for the thread role")
	if err := fs.Parse(args); err != nil {
		return ExitUsage
	}
	if *agentName != "" && fs.NArg() > 0 {
		return e.srvUsage("session start", "pass --agent or a command, not both")
	}
	if *agentName == "" && (*brief != "" || *kickoff != "" || *model != "" || *yolo || *role != "") {
		return e.srvUsage("session start", "--brief, --kickoff, --model, --yolo and --role need --agent")
	}
	if *yolo && e.Caller.IsAgent() {
		// Turning permission prompts off is the human's call (§11.2).
		fmt.Fprintln(e.Stderr, "tm session start: coordinator-only: --yolo is a human call")
		return ExitRefused
	}
	if *cols > 0xffff || *rows > 0xffff {
		return e.srvUsage("session start", "size out of range")
	}
	p := proto.SessionStartParams{Argv: fs.Args(), Cols: uint16(*cols), Rows: uint16(*rows),
		Agent: *agentName, Kickoff: *kickoff, Model: *model, Yolo: *yolo,
		Role: *role, Project: *project, Thread: *thread}
	if *brief != "" {
		abs, err := filepath.Abs(*brief)
		if err != nil {
			return e.srvUsage("session start", err.Error())
		}
		p.Brief = abs
	}
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

// stateLine is an agent session's state for listings: state/reason,
// todo progress and the current item.
func stateLine(s proto.SessionInfo) string {
	st := s.State
	if st == "" {
		st = "unknown"
	}
	if s.Reason != "" {
		st += "/" + s.Reason
	}
	if s.StateSources != "" {
		st += " (" + s.StateSources + ")"
	}
	if s.TodosTotal > 0 {
		st += fmt.Sprintf(" %d/%d todos", s.TodosDone, s.TodosTotal)
	}
	if s.Current != "" {
		st += " ▸ " + s.Current
	}
	if s.Queued > 0 {
		st += fmt.Sprintf(" · %d queued", s.Queued)
		if n := s.QueueNote(time.Now()); n != "" {
			st += " (" + n + ")"
		}
	}
	return st
}

func sessionPrompt(e *Env, args []string) int {
	if len(args) < 2 {
		return e.srvUsage("session prompt", "usage: tm session prompt ID TEXT")
	}
	id, text := args[0], strings.Join(args[1:], " ")
	if text == "-" {
		b, err := e.readArg("-")
		if err != nil {
			return e.srvUsage("session prompt", err.Error())
		}
		text = strings.TrimRight(b, "\n")
	}
	c, _, err := connect(false)
	if err != nil {
		return e.srvFail("session prompt", err)
	}
	defer c.Close()
	var res proto.SessionPromptResult
	if err := c.Call(proto.MethodSessionPrompt, proto.SessionPromptParams{ID: id, Text: text}, &res); err != nil {
		return e.srvFail("session prompt", err)
	}
	switch res.Via {
	case "channel":
		fmt.Fprintf(e.Stdout, "%s: sent\n", id)
	default:
		fmt.Fprintf(e.Stdout, "%s: queued (pasted once the agent is idle)\n", id)
	}
	return ExitOK
}

func sessionWait(e *Env, args []string) int {
	fs := flag.NewFlagSet("session wait", flag.ContinueOnError)
	fs.SetOutput(e.Stderr)
	states := fs.String("state", "", "comma-separated states to wait for (default: any change)")
	timeout := fs.Duration("timeout", 30*time.Second, "give up after this long (exit 1)")
	id, rest := splitID(args)
	if err := fs.Parse(rest); err != nil {
		return ExitUsage
	}
	if id == "" || fs.NArg() > 0 {
		return e.srvUsage("session wait", "usage: tm session wait ID [--state S[,S…]] [--timeout 30s]")
	}
	p := proto.SessionWaitParams{ID: id, TimeoutMS: int(timeout.Milliseconds())}
	if *states != "" {
		p.States = strings.Split(*states, ",")
	}
	c, _, err := connect(false)
	if err != nil {
		return e.srvFail("session wait", err)
	}
	defer c.Close()
	var res proto.SessionWaitResult
	if err := c.Call(proto.MethodSessionWait, p, &res); err != nil {
		return e.srvFail("session wait", err)
	}
	st := res.State
	if res.Reason != "" {
		st += "/" + res.Reason
	}
	fmt.Fprintln(e.Stdout, st)
	if res.TimedOut {
		fmt.Fprintf(e.Stderr, "tm session wait: timed out after %v\n", *timeout)
		return ExitRefused
	}
	return ExitOK
}
