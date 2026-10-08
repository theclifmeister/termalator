package server

// The mod's tools (docs/SPEC.md §8.6, Mods): in a thread session the mod
// gives the model typed tools for what a thread otherwise does through
// the shell (tm report, tm status, tm task steps, tm done), and serves
// each call with POST /v1/tools/{name} on the session's mod socket. The
// socket says which session calls, so the call runs as that session's
// thread, with no permission rule and no pid walk; the input is checked
// here and turned into the command it stands for, which runs in this
// server as `tm` would through cli.run. A mods-off session, and any other
// agent, keeps the shell commands.

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/theclifmeister/terminatr/internal/caller"
	"github.com/theclifmeister/terminatr/internal/codehost"
	"github.com/theclifmeister/terminatr/internal/proto"
	"github.com/theclifmeister/terminatr/internal/thread"
)

// maxModTool bounds a tool call's body: a report and its framing.
const maxModTool = thread.MaxReport + 16<<10

// ModReportTool is the input of the report tool: the report's sections,
// which the server writes out in the format tm report takes.
type ModReportTool struct {
	PR       string   `json:"pr,omitempty"`
	Report   string   `json:"report"`
	Next     []string `json:"next"`
	Check    []string `json:"check,omitempty"`
	Remember []string `json:"remember,omitempty"`
	Attach   []string `json:"attach,omitempty"`
}

// ModStatusTool is the input of the status tool: tm status's flags.
type ModStatusTool struct {
	Percent  *int   `json:"percent,omitempty"`
	Activity string `json:"activity,omitempty"`
	NeedsYou string `json:"needs_you,omitempty"`
}

// ModStepsTool is the input of the steps tool: steps of the thread's
// task to check, uncheck and add, done in that order.
type ModStepsTool struct {
	Check   []int    `json:"check,omitempty"`
	Uncheck []int    `json:"uncheck,omitempty"`
	Add     []string `json:"add,omitempty"`
}

// ModDoneTool is the input of the done tool.
type ModDoneTool struct {
	Summary string `json:"summary,omitempty"`
}

// ModToolResult is what POST /v1/tools/{name} answers: the command's
// output, for the model; with a non-2xx status, why it was refused.
type ModToolResult struct {
	Text  string `json:"text,omitempty"`
	Error string `json:"error,omitempty"`
}

// errModTool is a tool input that isn't one: the model fixes it.
var errModTool = errors.New("bad tool input")

func toolErr(format string, a ...any) error {
	return fmt.Errorf("%w: %s", errModTool, fmt.Sprintf(format, a...))
}

// modTool serves POST /v1/tools/{name} for session id.
func (s *Server) modTool(w http.ResponseWriter, r *http.Request, id string) {
	b, err := io.ReadAll(io.LimitReader(r.Body, maxModTool+1))
	if err == nil && len(b) > maxModTool {
		err = toolErr("input is over %d bytes", maxModTool)
	}
	if err != nil {
		toolReply(w, http.StatusBadRequest, ModToolResult{Error: err.Error()})
		return
	}
	s.mu.Lock()
	rec, ok := s.records[id]
	_, live := s.sessions[id]
	s.mu.Unlock()
	if !ok || !live {
		toolReply(w, http.StatusGone, ModToolResult{Error: "session " + id + " is gone"})
		return
	}
	code, res := s.runTool(rec, r.PathValue("name"), b)
	toolReply(w, code, res)
}

// runTool runs tool name with input b as the thread of session record
// rec, and answers with the HTTP status the mod socket sends.
func (s *Server) runTool(rec SessionRecord, name string, b []byte) (int, ModToolResult) {
	if rec.Role != proto.RoleThread || !thread.ValidID(rec.Thread) {
		return http.StatusForbidden, ModToolResult{Error: "these tools are a thread's"}
	}
	if s.opts.RunCLI == nil {
		return http.StatusServiceUnavailable, ModToolResult{Error: "this server runs no project commands"}
	}
	runs, err := toolRuns(name, b, func() string { return s.taskRef(rec.Project, rec.Thread) })
	if err != nil {
		code := http.StatusBadRequest
		if !errors.Is(err, errModTool) {
			code = http.StatusNotFound
		}
		return code, ModToolResult{Error: err.Error()}
	}
	c := caller.Caller{Kind: caller.Thread, Project: rec.Project, Thread: rec.Thread}
	var out strings.Builder
	defer s.kick()
	for _, p := range runs {
		p.Cwd = rec.Cwd
		res := s.opts.RunCLI(p, c)
		out.WriteString(res.Stdout)
		if res.Code != 0 {
			msg := strings.TrimSpace(res.Stderr)
			if msg == "" {
				msg = fmt.Sprintf("tm %s exited %d", p.Args[0], res.Code)
			}
			if done := strings.TrimSpace(out.String()); done != "" {
				msg = done + "\n" + msg
			}
			return http.StatusUnprocessableEntity, ModToolResult{Error: msg}
		}
	}
	return http.StatusOK, ModToolResult{Text: strings.TrimSpace(out.String())}
}

// toolRunRPC serves tool.run: the thread is the caller's, found from the
// peer pid, and its live session gives the cwd.
func (s *Server) toolRunRPC(p proto.ToolRunParams, peerPID int) (any, *proto.Error) {
	c := s.callerOf(peerPID)
	if c.Kind != caller.Thread {
		return nil, proto.Errorf(proto.ErrRefused, "these tools are a thread's")
	}
	s.mu.Lock()
	var rec SessionRecord
	found := false
	for id, r := range s.records {
		if _, live := s.sessions[id]; live && r.Role == proto.RoleThread && r.Project == c.Project && r.Thread == c.Thread {
			rec, found = r, true
			break
		}
	}
	s.mu.Unlock()
	if !found {
		return nil, proto.Errorf(proto.ErrRefused, "thread %s has no live session", c.Thread)
	}
	_, res := s.runTool(rec, p.Name, p.Input)
	return proto.ToolRunResult{Text: firstNonEmpty(res.Error, res.Text), IsError: res.Error != ""}, nil
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func toolReply(w http.ResponseWriter, code int, r ModToolResult) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(r)
}

// toolRuns checks tool name's input and returns the commands it stands
// for; task gives the thread's task ref, for steps. An unknown name is
// a plain error, a bad input errModTool.
func toolRuns(name string, body []byte, task func() string) ([]proto.CLIRunParams, error) {
	dec := json.NewDecoder(strings.NewReader(string(body)))
	dec.DisallowUnknownFields()
	decode := func(v any) error {
		if err := dec.Decode(v); err != nil {
			return toolErr("%v", err)
		}
		return nil
	}
	args := func(a ...string) proto.CLIRunParams { return proto.CLIRunParams{Args: a} }
	switch name {
	case "report":
		var in ModReportTool
		if err := decode(&in); err != nil {
			return nil, err
		}
		text, err := reportText(in)
		if err != nil {
			return nil, err
		}
		p := args("report")
		for _, a := range in.Attach {
			if strings.TrimSpace(a) == "" {
				return nil, toolErr("attach: an empty path")
			}
			p.Args = append(p.Args, "--attach="+a)
		}
		p.Stdin = []byte(text)
		return []proto.CLIRunParams{p}, nil
	case "status":
		var in ModStatusTool
		if err := decode(&in); err != nil {
			return nil, err
		}
		p := args("status")
		if in.Percent != nil {
			if *in.Percent < 0 || *in.Percent > 100 {
				return nil, toolErr("percent takes 0-100")
			}
			p.Args = append(p.Args, "--percent="+strconv.Itoa(*in.Percent))
		}
		if in.Activity != "" {
			p.Args = append(p.Args, "--activity="+in.Activity)
		}
		if in.NeedsYou != "" {
			p.Args = append(p.Args, "--needs-you="+in.NeedsYou)
		}
		if len(p.Args) == 1 {
			return nil, toolErr("give percent, activity or needs_you")
		}
		return []proto.CLIRunParams{p}, nil
	case "steps":
		var in ModStepsTool
		if err := decode(&in); err != nil {
			return nil, err
		}
		if len(in.Check)+len(in.Uncheck)+len(in.Add) == 0 {
			return nil, toolErr("give check, uncheck or add")
		}
		ref := task()
		if ref == "" {
			return nil, toolErr("this thread has no task")
		}
		var runs []proto.CLIRunParams
		for _, verb := range []struct {
			name string
			ns   []int
		}{{"check", in.Check}, {"uncheck", in.Uncheck}} {
			for _, n := range verb.ns {
				if n < 1 {
					return nil, toolErr("%s: %d is not a step number", verb.name, n)
				}
				runs = append(runs, args("task", "steps", ref, verb.name, strconv.Itoa(n)))
			}
		}
		for _, t := range in.Add {
			if strings.TrimSpace(t) == "" || strings.ContainsAny(t, "\r\n") {
				return nil, toolErr("add: a step is one non-empty line")
			}
			runs = append(runs, args("task", "steps", ref, "add", "--", t))
		}
		return runs, nil
	case "done":
		var in ModDoneTool
		if err := decode(&in); err != nil {
			return nil, err
		}
		p := args("done")
		if in.Summary != "" {
			p.Args = append(p.Args, "--", in.Summary)
		}
		return []proto.CLIRunParams{p}, nil
	}
	return nil, fmt.Errorf("no tool %q", name)
}

// reportText writes the report tool's sections out as tm report takes
// them; thread.Validate checks the rest once it runs.
func reportText(in ModReportTool) (string, error) {
	if strings.TrimSpace(in.Report) == "" {
		return "", toolErr("report: empty")
	}
	if in.PR != "" {
		if _, _, ok := codehost.ParsePRURL(in.PR); !ok {
			return "", toolErr("pr: want https://github.com/<owner>/<repo>/pull/<n> or https://dev.azure.com/<org>/<project>/_git/<repo>/pullrequest/<n>")
		}
	}
	for _, l := range strings.Split(in.Report, "\n") {
		if strings.HasPrefix(strings.TrimSpace(l), "## ") {
			return "", toolErr("report: a line starts with ## (the sections are the tool's other fields; use ### inside the report)")
		}
	}
	var b strings.Builder
	if in.PR != "" {
		fmt.Fprintf(&b, "PR: %s\n\n", strings.TrimSpace(in.PR))
	}
	b.WriteString("## Report\n\n" + strings.TrimSpace(in.Report) + "\n")
	for _, sec := range []struct {
		name  string
		lines []string
		need  bool
	}{{"Next", in.Next, true}, {"Check", in.Check, false}, {"Remember", in.Remember, false}} {
		if len(sec.lines) == 0 && !sec.need {
			continue
		}
		fmt.Fprintf(&b, "\n## %s\n\n", sec.name)
		for _, l := range sec.lines {
			l = strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(strings.TrimSpace(l), "- "), "* "))
			if l == "" || strings.ContainsAny(l, "\r\n") || strings.HasPrefix(l, "## ") {
				return "", toolErr("%s: each item is one non-empty line", strings.ToLower(sec.name))
			}
			b.WriteString("- " + l + "\n")
		}
	}
	return b.String(), nil
}
