package cli

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/theclifmeister/terminatr/internal/agent"
	"github.com/theclifmeister/terminatr/internal/caller"
	"github.com/theclifmeister/terminatr/internal/proto"
	"github.com/theclifmeister/terminatr/internal/server"
)

const watchUsage = `usage: tm watch [--session ID] [--json]   (default: this session, $TERMINATR_SESSION)
       tm watch --project <slug> [--json]`

func init() {
	commands["watch"] = func(e *Env, args []string) error { return codeErr(watchCmd(e, args)) }
}

// watchCmd implements `tm watch` (docs/SPEC.md §10): a session's state,
// then a line on each change, until the session ends. With --json each
// line is a proto.Watch (NDJSON), for mods. It reads through the control
// socket only, so it works from a sandboxed thread.
func watchCmd(e *Env, args []string) int {
	fs := flag.NewFlagSet("watch", flag.ContinueOnError)
	fs.SetOutput(e.Stderr)
	id := fs.String("session", "", "session id (default: $TERMINATR_SESSION)")
	slug := fs.String("project", "", "watch a project instead: what its coordinator's /tm pane shows")
	asJSON := fs.Bool("json", false, "print one JSON object per line")
	if err := fs.Parse(args); err != nil {
		return ExitUsage
	}
	if fs.NArg() != 0 || (*slug != "" && *id != "") {
		return e.srvUsage("watch", watchUsage)
	}
	if *slug != "" {
		return watchProject(e, *slug, *asJSON)
	}
	if *id == "" {
		*id = e.Getenv(caller.EnvSession)
	}
	if *id == "" {
		return e.srvUsage("watch", "no session: pass --session ID\n"+watchUsage)
	}
	p, err := server.ResolvePaths()
	if err != nil {
		return e.srvFail("watch", err)
	}
	st, w, err := server.WatchSession(p, *id)
	var perr *proto.Error
	if errors.As(err, &perr) && perr.Code == proto.ErrUnknownMethod {
		err = errors.New("the running server can't watch sessions; restart it (tm server restart)")
	}
	if err != nil {
		return e.srvFail("watch", err)
	}
	defer st.Close()
	for {
		if err := writeWatch(e.Stdout, w, *asJSON); err != nil {
			return ExitOK // the reader went away
		}
		if w.Session.State == string(agent.StateExited) {
			return ExitOK
		}
		if w, err = st.Next(); err != nil {
			if errors.Is(err, io.EOF) {
				err = errors.New("server hung up")
			}
			return e.srvFail("watch", err)
		}
	}
}

func writeWatch(out io.Writer, w proto.Watch, asJSON bool) error {
	if asJSON {
		b, err := json.Marshal(w)
		if err != nil {
			return err
		}
		_, err = out.Write(append(b, '\n'))
		return err
	}
	_, err := fmt.Fprintln(out, watchLine(w))
	return err
}

// watchLine is a Watch in one line for people: "s-3 thread working ·
// T12 started 2/4 · #12 open, checks pass · 1 needs you · 3 in inbox".
func watchLine(w proto.Watch) string {
	s := w.Session
	head := s.ID + " " + s.Role
	if s.State != "" {
		head += " " + s.State
		if s.Reason != "" {
			head += "/" + s.Reason
		}
	}
	parts := []string{head}
	if t := w.Task; t != nil {
		task := fmt.Sprintf("%s %s %d/%d", t.ID, t.Status, t.StepsDone, t.StepsTotal)
		if t.Current != "" {
			task += ": " + t.Current
		}
		parts = append(parts, task)
	}
	if w.PR != "" {
		parts = append(parts, w.PR)
	}
	if s.NeedsYou != "" {
		parts = append(parts, "waiting: "+s.NeedsYou)
	}
	if s.Project != "" {
		parts = append(parts, fmt.Sprintf("%d needs you", w.NeedsYou), fmt.Sprintf("%d in inbox", w.Inbox))
	}
	if w.Queued > 0 {
		parts = append(parts, fmt.Sprintf("%d queued", w.Queued))
	}
	return strings.Join(parts, " · ")
}

// watchProject is `tm watch --project`: the project's state, then a line
// on each change, until the reader or the server goes. With asJSON each
// line is a proto.ProjectWatch.
func watchProject(e *Env, slug string, asJSON bool) int {
	p, err := server.ResolvePaths()
	if err != nil {
		return e.srvFail("watch", err)
	}
	st, w, err := server.WatchProject(p, slug)
	var perr *proto.Error
	if errors.As(err, &perr) && perr.Code == proto.ErrUnknownMethod {
		err = errors.New("the running server can't watch projects; restart it (tm server restart)")
	}
	if err != nil {
		return e.srvFail("watch", err)
	}
	defer st.Close()
	for {
		var werr error
		if asJSON {
			b, _ := json.Marshal(w)
			_, werr = e.Stdout.Write(append(b, '\n'))
		} else {
			_, werr = fmt.Fprintln(e.Stdout, projectWatchLine(w))
		}
		if werr != nil {
			return ExitOK // the reader went away
		}
		if w, err = st.Next(); err != nil {
			if errors.Is(err, io.EOF) {
				err = errors.New("server hung up")
			}
			return e.srvFail("watch", err)
		}
	}
}

// projectWatchLine is a ProjectWatch in one line for people: "demo · 2
// need you · 1 in inbox · 4 threads · 3 on deck".
func projectWatchLine(w proto.ProjectWatch) string {
	return fmt.Sprintf("%s · %d need you · %d in inbox · %d threads · %d on deck",
		w.Project, len(w.NeedsYou), len(w.Inbox), len(w.Threads), len(w.Ready))
}
