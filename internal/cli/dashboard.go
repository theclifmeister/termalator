package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/theclifmeister/termalator/internal/caller"
	"github.com/theclifmeister/termalator/internal/home"
	"github.com/theclifmeister/termalator/internal/project"
	"github.com/theclifmeister/termalator/internal/proto"
	"github.com/theclifmeister/termalator/internal/tui"
	"github.com/theclifmeister/termalator/internal/view"
)

// defaultAgent runs coordinators and the dashboard's c key.
const defaultAgent = "claude"

// dashboardCmd is `tm` with no arguments, or `tm --own`: a console of
// the server-owned view (docs/SPEC.md §3.3, §4), view main unless own.
// It shows the view's screen, the dashboard or the attached layout, and
// follows it when this console or another changes it.
func (e *Env) dashboardCmd(own bool) int {
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
	src := &tui.ServerSource{Paths: p, Agent: defaultAgent, Caller: who}
	defer src.Close()
	var uiFile string // ui.json: the dashboard's layout (docs/SPEC.md §5.1)
	if d, err := home.Dir(); err == nil {
		uiFile = filepath.Join(d, "ui.json")
	}
	cols, rows, ok := termSize()
	if !ok {
		cols, rows = 80, 24
	}
	side := tui.LoadLayout(uiFile).Sidebar
	vc, err := tui.JoinView(p, proto.ViewSubscribeParams{Own: own, Cols: cols, Rows: rows, Sidebar: &side})
	if err != nil {
		return e.srvFail("dashboard", err)
	}
	defer vc.Close()
	args := []string{}
	if own {
		args = []string{"--own"}
	}
	var st tui.DashState
	attach := vc.View().Mode == view.ModeLayout
	for {
		if attach {
			ares, code := e.attach(p, vc, &tui.SidebarOptions{UIFile: uiFile}, args)
			if code != ExitOK {
				return code
			}
			if ares.Quit {
				return ExitOK
			}
			st.Message = strings.TrimPrefix(ares.Session+": "+ares.Reason, ": ")
			st.Then = ares.Then // prefix then p, ], [, … in the session
		}
		res, err := tui.Dashboard(tui.DashOptions{Source: src, In: os.Stdin, Out: os.Stdout,
			Cwd: e.Cwd, State: st, UIFile: uiFile, Prefix: tui.ConfigPrefix(), View: vc})
		if err != nil {
			return e.srvFail("dashboard", err)
		}
		if res.Attach == "" {
			return ExitOK
		}
		attach, st = true, tui.DashState{}
	}
}

// openCmd is `tm project open <slug>`: start or attach the project's
// coordinator, in a bare view of this console's own. Without a terminal
// it prints the session id.
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
	vc, err := tui.JoinView(p, proto.ViewSubscribeParams{Own: true, Bare: true, StatusBar: true, Session: id, Cols: cols, Rows: rows})
	if err != nil {
		return err
	}
	defer vc.Close()
	res, code := e.attach(p, vc, nil, []string{"project", "open", slug, "--agent", agentName})
	if code != ExitOK {
		return &exitError{code}
	}
	fmt.Fprintf(e.Stdout, "[%s: %s]\n", id, res.Reason)
	return nil
}
