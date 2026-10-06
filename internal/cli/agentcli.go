package cli

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/theclifmeister/terminatr/internal/agent"
	"github.com/theclifmeister/terminatr/internal/detect"
	"github.com/theclifmeister/terminatr/internal/proto"
	"github.com/theclifmeister/terminatr/internal/server"
)

const agentUsage = `usage: tm agent list [--json]
       tm agent check FILE
       tm agent reload
       tm agent explain SESSION [--json]`

func init() {
	commands["agent"] = func(e *Env, args []string) error { return codeErr(agentCmd(e, args)) }
}

// agentCmd implements `tm agent …` (docs/SPEC.md §8.2, §8.7).
func agentCmd(e *Env, args []string) int {
	if len(args) == 0 {
		return e.srvUsage("agent", agentUsage)
	}
	switch args[0] {
	case "list", "ls":
		return agentList(e, args[1:])
	case "check":
		return agentCheck(e, args[1:])
	case "reload":
		return agentReload(e, args[1:])
	case "explain":
		return agentExplain(e, args[1:])
	}
	return e.srvUsage("agent", agentUsage)
}

// localAgents lists the manifests as this binary sees them, for when no
// server runs.
func localAgents() (proto.AgentListResult, error) {
	p, err := server.ResolvePaths()
	if err != nil {
		return proto.AgentListResult{}, err
	}
	reg, lerr := agent.Load(p.AgentsDir())
	res := proto.AgentListResult{Agents: []proto.AgentInfo{}}
	if lerr != nil {
		res.Errors = strings.Split(lerr.Error(), "\n")
	}
	if reg == nil {
		return res, lerr
	}
	for _, n := range reg.Names() {
		a, _ := reg.Get(n)
		info := proto.AgentInfo{Name: n, Source: reg.Source[n], Injector: string(a.Injector())}
		if m := agent.ManifestOf(a); m != nil {
			info.Display, info.Command, info.Tested = m.Display, m.Launch.Command, m.TestedVersions
			info.Unenforced = !m.RendersAccess()
		}
		res.Agents = append(res.Agents, info)
	}
	return res, nil
}

func agentList(e *Env, args []string) int {
	fs := flag.NewFlagSet("agent list", flag.ContinueOnError)
	fs.SetOutput(e.Stderr)
	asJSON := fs.Bool("json", false, "print JSON")
	if err := fs.Parse(args); err != nil {
		return ExitUsage
	}
	var res proto.AgentListResult
	c, _, err := connect(false)
	if err == nil {
		defer c.Close()
		err = c.Call(proto.MethodAgentList, nil, &res)
	} else if errors.Is(err, server.ErrNotRunning) {
		res, err = localAgents()
	}
	if err != nil && len(res.Agents) == 0 {
		return e.srvFail("agent list", err)
	}
	if *asJSON {
		return e.srvJSON(res)
	}
	printAgents(e, res)
	return ExitOK
}

func printAgents(e *Env, res proto.AgentListResult) {
	tw := tabwriter.NewWriter(e.Stdout, 0, 0, 2, ' ', 0)
	for _, a := range res.Agents {
		var notes []string
		if a.Unenforced {
			notes = append(notes, "unenforced")
		}
		if len(a.Tested) > 0 {
			notes = append(notes, "tested "+strings.Join(a.Tested, ","))
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\tinject=%s\t%s\t%s\n", a.Name, a.Display, a.Command, a.Injector,
			strings.Join(notes, " "), a.Source)
	}
	tw.Flush()
	for _, msg := range res.Errors {
		fmt.Fprintf(e.Stdout, "broken (skipped): %s\n", msg)
	}
}

// agentCheck validates one manifest file the way the server loads it.
func agentCheck(e *Env, args []string) int {
	if len(args) != 1 {
		return e.srvUsage("agent check", "usage: tm agent check FILE")
	}
	data, err := os.ReadFile(args[0])
	if err != nil {
		return e.srvUsage("agent check", err.Error())
	}
	m, err := agent.ParseManifest(data)
	if err == nil {
		if base := strings.TrimSuffix(filepath.Base(args[0]), ".toml"); base != m.Name {
			err = fmt.Errorf("file name %q must match name = %q", base, m.Name)
		}
	}
	if err == nil {
		_, err = detect.New(m.Rules)
	}
	if err == nil {
		// Render every template once, with a sample spec, so template
		// errors show up now rather than at launch.
		_, err = agent.FromManifest(m).Launch(agent.LaunchSpec{
			Role: agent.RoleThread, SessionID: "s-0", AgentSID: "00000000-0000-4000-8000-000000000000",
			Cwd: "/tmp", RuntimeDir: "/tmp/rt", BriefPath: "/tmp/brief.md", Kickoff: "hello",
			TMBin: "/usr/local/bin/tm", Socket: "/tmp/tm.sock",
			Access: agent.Access{Read: []string{"/p"}, NoWrite: []string{"/p"}},
		})
	}
	if err != nil {
		fmt.Fprintf(e.Stderr, "tm agent check: %s:\n", args[0])
		for _, line := range strings.Split(err.Error(), "\n") {
			fmt.Fprintf(e.Stderr, "  %s\n", line)
		}
		return ExitRefused
	}
	fmt.Fprintf(e.Stdout, "%s: ok (%d hooks, %d rules, %d todo mappings)\n", m.Name, len(m.Hooks), len(m.Rules), len(m.Todos))
	return ExitOK
}

func agentReload(e *Env, args []string) int {
	if len(args) != 0 {
		return e.srvUsage("agent reload", "usage: tm agent reload")
	}
	c, _, err := connect(false)
	if errors.Is(err, server.ErrNotRunning) {
		fmt.Fprintln(e.Stdout, "no server running; manifests are read when it starts")
		return ExitOK
	}
	if err != nil {
		return e.srvFail("agent reload", err)
	}
	defer c.Close()
	var res proto.AgentListResult
	if err := c.Call(proto.MethodAgentReload, nil, &res); err != nil {
		return e.srvFail("agent reload", err)
	}
	printAgents(e, res)
	if len(res.Errors) > 0 {
		return ExitRefused
	}
	return ExitOK
}

func agentExplain(e *Env, args []string) int {
	fs := flag.NewFlagSet("agent explain", flag.ContinueOnError)
	fs.SetOutput(e.Stderr)
	asJSON := fs.Bool("json", false, "print JSON")
	id, rest := splitID(args)
	if err := fs.Parse(rest); err != nil {
		return ExitUsage
	}
	if id == "" && fs.NArg() == 1 {
		id = fs.Arg(0)
	} else if id == "" || fs.NArg() > 0 {
		return e.srvUsage("agent explain", "usage: tm agent explain SESSION [--json]")
	}
	c, _, err := connect(false)
	if err != nil {
		return e.srvFail("agent explain", err)
	}
	defer c.Close()
	var res server.ExplainResult
	if err := c.Call(proto.MethodAgentExplain, proto.SessionIDParams{ID: id}, &res); err != nil {
		return e.srvFail("agent explain", err)
	}
	if *asJSON {
		return e.srvJSON(res)
	}
	printExplain(e, res)
	return ExitOK
}

func printExplain(e *Env, r server.ExplainResult) {
	w := e.Stdout
	ago := func(t time.Time) string {
		if t.IsZero() {
			return ""
		}
		return time.Since(t).Round(100*time.Millisecond).String() + " ago"
	}
	src := func(name string, v *agent.SourceView, note string) {
		if v == nil {
			fmt.Fprintf(w, "  %-14s —%s\n", name, note)
			return
		}
		s := string(v.State)
		if v.Reason != "" {
			s += "/" + v.Reason
		}
		if v.Rule != "" {
			s += " [" + v.Rule + "]"
		}
		fmt.Fprintf(w, "  %-14s %s  %s%s\n", name, s, ago(v.At), note)
	}
	res := r.Result
	state := string(res.State)
	if res.Reason != "" {
		state += "/" + res.Reason
	}
	fmt.Fprintf(w, "%s  %s  %s  (%s)\n", r.Session.ID, r.Session.Agent, state, res.Sources)
	if r.AgentSID != "" {
		fmt.Fprintf(w, "agent session  %s\n", r.AgentSID)
	}
	fmt.Fprintln(w, "sources, highest rank first:")
	src("exit", r.Exit, "")
	note := ""
	if r.StatusErr != "" {
		note = "  (not used: " + r.StatusErr + ")"
	}
	if r.StatusDoubt != "" {
		note = "  (not believed: " + r.StatusDoubt + ")"
	}
	src("status file", r.StatusFile, note)
	src("hooks", r.Hook, "")
	src("hook edge", r.HookEdge, "")
	src("jsonl tail", r.Tail, "")
	src("screen", r.Screen, "")
	if r.ScreenLast != nil && (r.Screen == nil || r.ScreenLast.Rule != r.Screen.Rule) {
		src("screen (raw)", r.ScreenLast, "  (debouncing)")
	}
	if len(r.Matches) > 0 {
		fmt.Fprintf(w, "matching rules  %s\n", strings.Join(r.Matches, ", "))
	}
	for name, n := range r.Counters {
		fmt.Fprintf(w, "counter        %s = %d\n", name, n)
	}
	if len(r.Todos) > 0 {
		fmt.Fprintln(w, "todos:")
		for _, t := range r.Todos {
			mark := map[agent.TodoStatus]string{agent.TodoCompleted: "[x]", agent.TodoInProgress: "[~]"}[t.Status]
			if mark == "" {
				mark = "[ ]"
			}
			fmt.Fprintf(w, "  %s %s\n", mark, t.Text)
		}
	}
	if len(r.Events) > 0 {
		fmt.Fprintln(w, "recent hook events:")
		for _, ev := range r.Events {
			fmt.Fprintf(w, "  #%-4d %-20s %-34s %s\n", ev.Seq, ev.Event, ev.Detail, strings.Join(ev.Signals, ", "))
		}
	}
	for _, k := range []string{"pid", "status_file", "version", "jsonl_tail", "identified", "queued_prompts"} {
		if v := r.Extra[k]; v != "" {
			fmt.Fprintf(w, "%-14s %s\n", strings.ReplaceAll(k, "_", " "), v)
		}
	}
}
