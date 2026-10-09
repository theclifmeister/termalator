package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/theclifmeister/terminatr/internal/caller"
	"github.com/theclifmeister/terminatr/internal/home"
	"github.com/theclifmeister/terminatr/internal/project"
	"github.com/theclifmeister/terminatr/internal/proto"
	"github.com/theclifmeister/terminatr/internal/tui"
	"github.com/theclifmeister/terminatr/internal/view"
)

// dashboardCmd is `tm` with no arguments, or `tm --own`: a console of
// the server-owned view (docs/SPEC.md §3.3, §4), view main unless own.
// It shows the view's screen, the dashboard or the attached layout, and
// follows it when this console or another changes it.
func (e *Env) dashboardCmd(own bool) int {
	return e.fullConsole(own, nil)
}

// uiFile is ui.json, the console's layout (docs/SPEC.md §5.1); "" when
// there is no home.
func uiFile() string {
	if d, err := home.Dir(); err == nil {
		return filepath.Join(d, "ui.json")
	}
	return ""
}

// fullConsole runs a full console: the dashboard and the attached
// layout of view main (own: a view of its own). goTo, from a sidebar
// click in tm attach or tm project open, is opened first: that bare view
// hands its console over to this one (docs/SPEC.md §3.3).
func (e *Env) fullConsole(own bool, goTo *tui.Target) int {
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
	src := &tui.ServerSource{Paths: p, Caller: who}
	defer src.Close()
	uiFile := uiFile()
	cols, rows, ok := termSize()
	if !ok {
		cols, rows = 80, 24
	}
	lay := tui.LoadLayout(uiFile)
	side, info := lay.Sidebar, lay.Info
	vc, err := tui.JoinView(p, proto.ViewSubscribeParams{Own: own, Cols: cols, Rows: rows, Sidebar: &side, Info: &info})
	if err != nil {
		return e.srvFail("dashboard", err)
	}
	defer vc.Close()
	args := []string{}
	if own {
		args = []string{"--own"}
	}
	var st tui.DashState
	if goTo != nil {
		if err := tui.OpenTarget(p, vc, *goTo); err != nil {
			st.Message = err.Error()
		}
	}
	attach := vc.View().Mode == view.ModeLayout
	flash := ""        // the popup's last message, for the session's status bar
	command := ""      // a prefix command typed over the popup, for the session
	sideFocus := false // the sidebar has the keyboard, across screens
	for {
		var over *tui.Over
		if attach {
			ares, code := e.attach(p, vc, &tui.SidebarOptions{UIFile: uiFile, Focus: sideFocus}, flash, command, args)
			if code != ExitOK {
				return code
			}
			if ares.Quit {
				return ExitOK
			}
			st.Message = strings.TrimPrefix(ares.Session+": "+ares.Reason, ": ")
			st.Then = ares.Then // prefix then p, ] or [ in the session
			st.SideFocus = ares.SideFocus
			if ares.Then != "" || ares.GoTo != nil || ares.Over != nil {
				// Left to run a key, open a row or a popup: no news.
				st.Message = ""
			}
			// Prefix then a, i, t, , or ?: the popup over the session,
			// which attaches again once it closes.
			over = ares.Over
		}
		// Start at the window's size now: waiting for the program's own
		// size leaves the dashboard at 80×24 until it arrives (Windows).
		wc, wr, _ := termSize()
		res, err := tui.Dashboard(tui.DashOptions{Source: src, In: os.Stdin, Out: os.Stdout, Width: int(wc), Height: int(wr),
			Cwd: e.Cwd, State: st, UIFile: uiFile, Prefix: tui.ConfigPrefix(), View: vc, Over: over})
		if err != nil {
			return e.srvFail("dashboard", err)
		}
		if res.Attach == "" {
			return ExitOK
		}
		attach, st, flash, command, sideFocus = true, tui.DashState{}, res.Message, res.Command, res.SideFocus
	}
}

// openCmd is `tm project open <slug>`: start or attach the project's
// coordinator, in a bare view of this console's own. Without a terminal
// it prints the session id. A click on the sidebar hands the console
// over to a full one of view main.
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
	// The pane gets the window less the sidebar and the status bar.
	uiFile := uiFile()
	lay := tui.LoadLayout(uiFile)
	side, info := lay.Sidebar, lay.Info
	id, err := tui.OpenCoordinator(c.Call, slug, agentName, max(int(cols)-side.Cols(int(cols)), 1), int(max(rows, 2)-1))
	c.Close()
	if err != nil {
		return err
	}
	if !isTTY(os.Stdin) || !isTTY(os.Stdout) {
		fmt.Fprintln(e.Stdout, id)
		return nil
	}
	vc, err := tui.JoinView(p, proto.ViewSubscribeParams{Own: true, Bare: true, StatusBar: true, Session: id,
		Cols: cols, Rows: rows, Sidebar: &side, Info: &info})
	if err != nil {
		return err
	}
	args := []string{"project", "open", slug}
	if agentName != "" {
		args = append(args, "--agent", agentName)
	}
	res, code := e.attach(p, vc, &tui.SidebarOptions{UIFile: uiFile}, "", "", args)
	vc.Close()
	if code != ExitOK {
		return &exitError{code}
	}
	if res.GoTo != nil {
		return codeErr(e.fullConsole(false, res.GoTo))
	}
	fmt.Fprintf(e.Stdout, "[%s: %s]\n", id, res.Reason)
	return nil
}
