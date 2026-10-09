package server

import (
	"encoding/json"
	"log"
	"net/http"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/theclifmeister/terminatr/internal/agent"
	"github.com/theclifmeister/terminatr/internal/caller"
	"github.com/theclifmeister/terminatr/internal/plat/ipc"
	"github.com/theclifmeister/terminatr/internal/proto"
	"github.com/theclifmeister/terminatr/internal/session"
	"github.com/theclifmeister/terminatr/internal/thread"
)

// TestModTools: a thread session's mod calls its tools over its socket;
// each runs as the session's thread, in its cwd, as the command it
// stands for, and what isn't a tool's input is refused before anything
// runs.
func TestModTools(t *testing.T) {
	rt, err := os.MkdirTemp(ipc.ShortDir(), "tmmod")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(rt) })
	b, _ := agent.Builtin("claude")
	m, err := agent.ParseManifest(b)
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var ran []proto.CLIRunParams
	var as []caller.Caller
	fail := ""
	s := &Server{log: log.New(os.Stderr, "", 0), sessions: map[string]*session.Session{}, records: map[string]SessionRecord{}}
	s.opts.RunCLI = func(p proto.CLIRunParams, c caller.Caller) proto.CLIRunResult {
		mu.Lock()
		defer mu.Unlock()
		ran, as = append(ran, p), append(as, c)
		if fail != "" && p.Args[len(p.Args)-1] == fail {
			return proto.CLIRunResult{Code: 1, Stderr: "tm: refused: no step " + fail + "\n"}
		}
		return proto.CLIRunResult{Stdout: strings.Join(p.Args, " ") + " ok\n"}
	}
	s.taskOf.Store("proj/t-0001", "T5")
	s.mu.Lock()
	sock, err := s.listenMod("s-1", rt)
	s.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	sess, err := session.Start(session.Config{
		ID: "s-1", Role: proto.RoleThread, Argv: []string{testSh(t), "-c", "exec cat"}, Cwd: rt,
		Env: []string{"PATH=/usr/bin:/bin"}, Cols: 80, Rows: 24, Logf: t.Logf,
		Agent: &session.AgentConfig{Agent: agent.FromManifest(m), Home: rt, ModSocket: sock},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sess.Stop(time.Second) })
	c := modClient(sock)
	call := func(name, body string) (int, ModToolResult) {
		t.Helper()
		resp, err := c.Post("http://terminatr/v1/tools/"+name, "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatalf("POST %s: %v", name, err)
		}
		defer resp.Body.Close()
		var r ModToolResult
		if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		return resp.StatusCode, r
	}
	took := func() []proto.CLIRunParams {
		mu.Lock()
		defer mu.Unlock()
		got := ran
		ran = nil
		return got
	}

	if code, _ := call("status", `{"activity":"x"}`); code != http.StatusGone {
		t.Fatalf("no session: %d", code)
	}
	s.mu.Lock()
	s.sessions["s-1"] = sess
	s.records["s-1"] = SessionRecord{ID: "s-1", Role: proto.RoleCoordinator, Project: "proj", Cwd: rt}
	s.mu.Unlock()
	if code, _ := call("status", `{"activity":"x"}`); code != http.StatusForbidden {
		t.Fatalf("coordinator: %d", code)
	}
	s.mu.Lock()
	s.records["s-1"] = SessionRecord{ID: "s-1", Role: proto.RoleThread, Project: "proj", Thread: "t-0001", Cwd: rt}
	s.mu.Unlock()

	// status: flags in the value form, so a leading "-" stays a value.
	code, r := call("status", `{"percent":40,"activity":"-writing tests","needs_you":"which port?"}`)
	if code != http.StatusOK || r.Text == "" {
		t.Fatalf("status: %d %+v", code, r)
	}
	got := took()
	want := []string{"status", "--percent=40", "--activity=-writing tests", "--needs-you=which port?"}
	if len(got) != 1 || !reflect.DeepEqual(got[0].Args, want) || got[0].Cwd != rt {
		t.Fatalf("status ran %+v", got)
	}
	if as[0] != (caller.Caller{Kind: caller.Thread, Project: "proj", Thread: "t-0001"}) {
		t.Fatalf("caller %+v", as[0])
	}

	// steps: one command per step, in order, on the thread's own task.
	if code, r = call("steps", `{"check":[1,2],"add":["-v flag"]}`); code != http.StatusOK {
		t.Fatalf("steps: %d %+v", code, r)
	}
	var argv [][]string
	for _, p := range took() {
		argv = append(argv, p.Args)
	}
	if !reflect.DeepEqual(argv, [][]string{
		{"task", "steps", "T5", "check", "1"}, {"task", "steps", "T5", "check", "2"}, {"task", "steps", "T5", "add", "--", "-v flag"},
	}) {
		t.Fatalf("steps ran %q", argv)
	}
	// The first refusal stops the rest and says what got done.
	fail = "2"
	if code, r = call("steps", `{"check":[1,2,3]}`); code != http.StatusUnprocessableEntity || !strings.Contains(r.Error, "check 1 ok") || !strings.Contains(r.Error, "no step 2") {
		t.Fatalf("refused step: %d %+v", code, r)
	}
	if n := len(took()); n != 2 {
		t.Fatalf("ran %d after a refusal", n)
	}
	fail = ""

	// report: the sections, written out as tm report reads them.
	code, r = call("report", `{"pr":"https://github.com/o/r/pull/7","report":"Did it.\n\n### Detail\nmore","next":["- Merge PR #7"],"check":["run tm x"],"remember":["one"],"attach":["out.png"]}`)
	if code != http.StatusOK {
		t.Fatalf("report: %d %+v", code, r)
	}
	got = took()
	if len(got) != 1 || !reflect.DeepEqual(got[0].Args, []string{"report", "--attach=out.png"}) {
		t.Fatalf("report ran %+v", got)
	}
	rep, err := thread.Validate(string(got[0].Stdin))
	if err != nil {
		t.Fatalf("%v\n%s", err, got[0].Stdin)
	}
	if rep.PR != "https://github.com/o/r/pull/7" || !reflect.DeepEqual(rep.Next, []string{"Merge PR #7"}) ||
		!reflect.DeepEqual(rep.Check, []string{"- run tm x"}) || !reflect.DeepEqual(rep.Remember, []string{"- one"}) ||
		!strings.Contains(rep.Text, "### Detail") {
		t.Fatalf("report %+v", rep)
	}

	// done, with its summary as one argument.
	if code, _ = call("done", `{"summary":"--all of it"}`); code != http.StatusOK {
		t.Fatalf("done: %d", code)
	}
	if got = took(); !reflect.DeepEqual(got[0].Args, []string{"done", "--", "--all of it"}) {
		t.Fatalf("done ran %+v", got)
	}

	for _, bad := range []struct{ name, body string }{
		{"status", `{}`},
		{"status", `{"percent":101}`},
		{"status", `{"activity":"x","mood":"good"}`},
		{"status", `not json`},
		{"steps", `{}`},
		{"steps", `{"check":[0]}`},
		{"steps", `{"add":["two\nlines"]}`},
		{"report", `{"report":"","next":[]}`},
		{"report", `{"report":"x\n## Next\n- y","next":[]}`},
		{"report", `{"report":"x","next":["a\nb"]}`},
		{"report", `{"report":"x","next":[],"pr":"github.com/o/r/pull/1"}`},
		{"report", `{"report":"x","next":[],"attach":[""]}`},
	} {
		if code, r := call(bad.name, bad.body); code != http.StatusBadRequest || r.Error == "" {
			t.Fatalf("%s %s: %d %+v", bad.name, bad.body, code, r)
		}
	}
	if code, _ := call("merge", `{}`); code != http.StatusNotFound {
		t.Fatalf("unknown tool: %d", code)
	}
	if n := len(took()); n != 0 {
		t.Fatalf("bad inputs ran %d commands", n)
	}

	// A thread with no task has no steps to tick.
	s.taskOf.Store("proj/t-0001", "")
	if code, r := call("steps", `{"check":[1]}`); code != http.StatusBadRequest || !strings.Contains(r.Error, "no task") {
		t.Fatalf("no task: %d %+v", code, r)
	}

	// tool.run (tm mcp): the thread is found from the caller's pid, in
	// the session's process tree.
	took()
	res, perr := s.toolRunRPC(proto.ToolRunParams{Name: "status", Input: json.RawMessage(`{"activity":"mcp"}`)}, sess.PID())
	if perr != nil {
		t.Fatalf("tool.run: %v", perr)
	}
	if rr := res.(proto.ToolRunResult); rr.IsError || !strings.Contains(rr.Text, "status --activity=mcp") {
		t.Fatalf("tool.run: %+v", rr)
	}
	if got := took(); len(got) != 1 || got[0].Cwd != rt {
		t.Fatalf("tool.run ran %+v", got)
	}
	res, _ = s.toolRunRPC(proto.ToolRunParams{Name: "status", Input: json.RawMessage(`{}`)}, sess.PID())
	if rr := res.(proto.ToolRunResult); !rr.IsError {
		t.Fatalf("tool.run bad input: %+v", rr)
	}
	if _, perr := s.toolRunRPC(proto.ToolRunParams{Name: "done"}, 1); perr == nil {
		t.Fatal("tool.run from outside a thread")
	}
}
