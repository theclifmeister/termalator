package cli

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/theclifmeister/termalator/internal/caller"
	"github.com/theclifmeister/termalator/internal/home"
	"github.com/theclifmeister/termalator/internal/project"
	"github.com/theclifmeister/termalator/internal/tui"
)

// defaultAgent runs coordinators and the dashboard's c key.
const defaultAgent = "claude"

// dashboardCmd is `tm` with no arguments: the dashboard, and the attach
// view in between (docs/SPEC.md §4). The prefix (Ctrl+\) then d in a
// session comes back here.
func (e *Env) dashboardCmd() int {
	if !isTTY(os.Stdin) || !isTTY(os.Stdout) {
		fmt.Fprintln(e.Stderr, "tm: the dashboard needs a terminal; see tm session list")
		return ExitUsage
	}
	if rawMode(os.Stdin) {
		fmt.Fprintln(e.Stderr, "tm: this terminal looks like it was left in raw mode (a client killed with SIGKILL?); run `reset` if it misbehaves")
	}
	c, p, err := connect(true)
	if err != nil {
		return e.srvFail("dashboard", err)
	}
	c.Close()
	who := e.Caller
	if e.Identify != nil {
		if c, ok := e.Identify(); ok {
			who = caller.Narrower(who, c)
		}
	}
	src := &tui.ServerSource{Paths: p, Agent: defaultAgent, Caller: who, Run: e.quietRun(who)}
	defer src.Close()
	var uiFile string // ui.json: the dashboard's layout (docs/SPEC.md §5.1)
	if d, err := home.Dir(); err == nil {
		uiFile = filepath.Join(d, "ui.json")
	}
	var st tui.DashState
	for {
		res, err := tui.Dashboard(tui.DashOptions{Source: src, In: os.Stdin, Out: os.Stdout,
			Cwd: e.Cwd, AgentName: defaultAgent, State: st, UIFile: uiFile, Prefix: tui.ConfigPrefix()})
		if err != nil {
			return e.srvFail("dashboard", err)
		}
		if res.Attach == "" {
			return ExitOK
		}
		st = res.State
		ares, code := e.attach(p, res.Attach, true, []string{})
		if code != ExitOK {
			return code
		}
		st.Message = res.Attach + ": " + ares.Reason
		st.Then = ares.Then // prefix then p, ], [, … in the session
	}
}

// openCmd is `tm project open <slug>`: start or attach the project's
// coordinator. Without a terminal it prints the session id.
func (e *Env) openCmd(slug, agentName string) error {
	if _, err := project.Open(slug); err != nil {
		return err
	}
	c, p, err := connect(true)
	if err != nil {
		return err
	}
	cols, rows, ok := termSize()
	if !ok {
		cols, rows = 80, 25
	}
	id, err := tui.OpenCoordinator(c.Call, slug, agentName, int(cols), int(max(rows, 2)-1))
	c.Close()
	if err != nil {
		return err
	}
	if !isTTY(os.Stdin) || !isTTY(os.Stdout) {
		fmt.Fprintln(e.Stdout, id)
		return nil
	}
	res, code := e.attach(p, id, true, []string{"project", "open", slug, "--agent", agentName})
	if code != ExitOK {
		return &exitError{code}
	}
	fmt.Fprintf(e.Stdout, "[%s: %s]\n", id, res.Reason)
	return nil
}

// quietRun runs tm commands for the dashboard as who, with their output
// captured: a failure becomes an error with the command's message.
func (e *Env) quietRun(who caller.Caller) func(args ...string) error {
	return func(args ...string) error {
		var out, errb bytes.Buffer
		sub := *e
		sub.Stdin, sub.Stdout, sub.Stderr, sub.Caller = strings.NewReader(""), &out, &errb, who
		if code, _ := sub.Run(args); code != ExitOK {
			msg := strings.TrimSpace(errb.String())
			if msg == "" {
				msg = fmt.Sprintf("tm %s: exit %d", args[0], code)
			}
			return errors.New(msg)
		}
		return nil
	}
}
