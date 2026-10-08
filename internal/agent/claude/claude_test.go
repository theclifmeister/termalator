package claude

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/theclifmeister/terminatr/internal/agent"
	"github.com/theclifmeister/terminatr/internal/guard"
)

func load(t *testing.T) agent.Agent {
	t.Helper()
	r, err := agent.Load("")
	if err != nil {
		t.Fatal(err)
	}
	a, ok := r.Get("claude")
	if !ok {
		t.Fatal("no claude agent")
	}
	if _, ok := a.(*Agent); !ok {
		t.Fatalf("claude is %T, want the Go agent", a)
	}
	if agent.ManifestOf(a) == nil {
		t.Fatal("ManifestOf(claude) is nil")
	}
	return a
}

func TestPromptOverMessagingSocket(t *testing.T) {
	a := load(t)
	if a.Injector() != agent.InjectPaste {
		t.Fatalf("the built-in manifest pastes; injector %q", a.Injector())
	}
	// A manifest that opts into the channel still pastes: the socket is
	// only for the server's own prompts.
	m := *agent.ManifestOf(a)
	m.Inject.Prompt = agent.InjectChannel
	if inj := (&Agent{Agent: agent.FromManifest(&m), m: &m}).Injector(); inj != agent.InjectPaste {
		t.Fatalf("inject.prompt = channel: injector %q", inj)
	}
	dir, err := os.MkdirTemp("/tmp", "tmclaude")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	sock := filepath.Join(dir, "m.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	// accept reads the lines of the next connection until it closes.
	accept := func() <-chan []string {
		got := make(chan []string, 1)
		go func() {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			defer c.Close()
			var lines []string
			sc := bufio.NewScanner(c)
			for sc.Scan() {
				lines = append(lines, sc.Text())
			}
			got <- lines
		}()
		return got
	}
	type line struct {
		Type    string
		Token   string
		Message struct{ Role, Content string }
	}
	parse := func(lines []string) []line {
		t.Helper()
		var out []line
		for _, l := range lines {
			var x line
			if err := json.Unmarshal([]byte(l), &x); err != nil {
				t.Fatal(err)
			}
			out = append(out, x)
		}
		return out
	}

	// Without a token: the user line alone.
	got := accept()
	target := agent.PromptTarget{Version: "2.1.289", Fields: map[string]string{SocketField: sock}}
	if err := a.Prompt(context.Background(), target, "hello\nworld"); err != nil {
		t.Fatal(err)
	}
	msgs := parse(<-got)
	if len(msgs) != 1 || msgs[0].Type != "user" || msgs[0].Message.Role != "user" || msgs[0].Message.Content != "hello\nworld" {
		t.Fatalf("lines %+v", msgs)
	}

	// With the hooks' token (2.1.291): the auth line comes first.
	got = accept()
	target.Version, target.Token = "2.1.291", "child-token"
	if err := a.Prompt(context.Background(), target, "hi"); err != nil {
		t.Fatal(err)
	}
	msgs = parse(<-got)
	if len(msgs) != 2 || msgs[0].Type != "auth" || msgs[0].Token != "child-token" || msgs[1].Type != "user" || msgs[1].Message.Content != "hi" {
		t.Fatalf("lines %+v", msgs)
	}

	// Every failure is an error, so the core falls back to paste.
	if err := a.Prompt(context.Background(), agent.PromptTarget{Version: "2.1.289"}, "x"); !errors.Is(err, ErrNoSocket) {
		t.Fatalf("no socket: %v", err)
	}
	target.Version = "3.0.0"
	if err := a.Prompt(context.Background(), target, "x"); !errors.Is(err, agent.ErrUntestedVersion) {
		t.Fatalf("untested version: %v", err)
	}
	target.Version = "2.1.289"
	target.Fields[SocketField] = filepath.Join(dir, "gone.sock")
	if err := a.Prompt(context.Background(), target, "x"); err == nil {
		t.Fatal("dead socket: no error")
	}
}

// TestProbe: a connect-only liveness check. A missing socket or a
// refused connection is gone; a listening socket is live and gets no
// line.
func TestProbe(t *testing.T) {
	a := load(t).(agent.Prober)
	dir, err := os.MkdirTemp("/tmp", "tmclaude")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	sock := filepath.Join(dir, "m.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	got := make(chan string, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		b, _ := io.ReadAll(c)
		got <- string(b)
	}()
	target := agent.PromptTarget{Version: "2.1.291", Fields: map[string]string{SocketField: sock}}
	if live, err := a.Probe(context.Background(), target); live != agent.Live || err != nil {
		t.Fatalf("listening: %q %v", live, err)
	}
	if b := <-got; b != "" {
		t.Fatalf("the probe wrote %q", b)
	}

	// The socket file left behind by a dead process refuses.
	ln.(*net.UnixListener).SetUnlinkOnClose(false)
	ln.Close()
	if live, err := a.Probe(context.Background(), target); live != agent.Gone || !errors.Is(err, syscall.ECONNREFUSED) {
		t.Fatalf("refused: %q %v", live, err)
	}
	os.Remove(sock)
	if live, err := a.Probe(context.Background(), target); live != agent.Gone || !errors.Is(err, syscall.ENOENT) {
		t.Fatalf("no socket file: %q %v", live, err)
	}
	if live, err := a.Probe(context.Background(), agent.PromptTarget{}); live != agent.LiveUnknown || !errors.Is(err, ErrNoSocket) {
		t.Fatalf("no socket in the status file: %q %v", live, err)
	}
}

// TestGuard: PreToolUse keeps the tool's input through the hook's trim (so
// the guard judges Bash and file tools with mods off), and the guard's
// refusal is answered as a deny.
func TestGuard(t *testing.T) {
	a := load(t)
	payload := a.Sources().Hook.Trim(map[string]any{
		"hook_event_name": "PreToolUse", "session_id": "x", "tool_name": "Bash",
		"tool_input": map[string]any{"command": "gh pr merge 1"},
	})
	var judged string
	env := agent.HookEnv{Guard: func(tool string, input map[string]any) *guard.Denial {
		judged = tool + ": " + input["command"].(string)
		return guard.Rules{On: true, Role: "thread", Rules: []string{"merge"}, Tools: agent.GuardTools(nil, "claude")}.Judge(tool, input)
	}}
	_, res, err := a.Hook(agent.HookEvent{Event: "PreToolUse", Payload: payload}, env)
	if err != nil {
		t.Fatal(err)
	}
	if judged != "Bash: gh pr merge 1" {
		t.Errorf("judged %q", judged)
	}
	if !strings.Contains(string(res.Stdout), `"permissionDecision":"deny"`) {
		t.Errorf("no deny: %s", res.Stdout)
	}
	if p := a.Sources().Hook.Trim(map[string]any{"hook_event_name": "PostToolUse", "tool_input": map[string]any{}}); p["tool_input"] != nil {
		t.Error("tool_input kept beyond PreToolUse")
	}
}
