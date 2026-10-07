package cli

import (
	"bufio"
	"bytes"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/theclifmeister/terminatr/internal/proto"
)

// hookEnv runs `tm hook` in-process against a socket in a short temp dir.
func hookEnv(t *testing.T, payload string) (*Env, *bytes.Buffer, *bytes.Buffer, string) {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "tmhook")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	sock := filepath.Join(dir, "tm.sock")
	t.Setenv("TERMINATR_HOME", filepath.Join(dir, "home"))
	t.Setenv("TERMINATR_SOCKET", sock)
	var out, errb bytes.Buffer
	vars := map[string]string{"TERMINATR_SESSION": "s-1", "CLAUDE_CODE_MESSAGING_TOKEN": "child-token"}
	e := &Env{Stdin: strings.NewReader(payload), Stdout: &out, Stderr: &errb, Getenv: func(k string) string { return vars[k] }}
	return e, &out, &errb, sock
}

// fakeServer answers hook connections with stdout, after delay; got
// receives each request's params.
func fakeServer(t *testing.T, sock string, delay time.Duration, stdout string, got chan<- proto.HookEventParams) {
	t.Helper()
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				br := bufio.NewReader(c)
				var hello proto.Hello
				line, _ := br.ReadBytes('\n')
				json.Unmarshal(line, &hello)
				b, _ := json.Marshal(proto.Hello{Protocol: proto.Protocol})
				c.Write(append(b, '\n'))
				var req proto.Request
				line, _ = br.ReadBytes('\n')
				json.Unmarshal(line, &req)
				var p proto.HookEventParams
				json.Unmarshal(req.Params, &p)
				if got != nil {
					got <- p
				}
				time.Sleep(delay)
				res, _ := json.Marshal(proto.HookEventResult{Stdout: stdout})
				b, _ = json.Marshal(proto.Response{ID: req.ID, Result: res})
				c.Write(append(b, '\n'))
			}()
		}
	}()
}

func runHookTimed(t *testing.T, e *Env, args ...string) time.Duration {
	t.Helper()
	start := time.Now()
	if code, ok := e.Run(append([]string{"hook"}, args...)); !ok || code != 0 {
		t.Fatalf("tm hook exit %d (handled %v)", code, ok)
	}
	return time.Since(start)
}

func TestHookDelivers(t *testing.T) {
	big := strings.Repeat("x", 200<<10) // far past a macOS datagram
	payload := `{"hook_event_name":"SessionStart","session_id":"a","source":"clear","prompt":"` + big + `","secret_field":"y"}`
	e, out, errb, sock := hookEnv(t, payload)
	got := make(chan proto.HookEventParams, 1)
	fakeServer(t, sock, 0, `{"ctx":1}`, got)
	runHookTimed(t, e, "--agent", "claude")
	p := <-got
	if p.Session != "s-1" || p.Agent != "claude" || p.Event != "SessionStart" || p.PPID == 0 {
		t.Fatalf("envelope %+v", p)
	}
	// The manifest's inject.token_env goes along, outside the payload.
	if p.Token != "child-token" {
		t.Errorf("token %q, want the hook's CLAUDE_CODE_MESSAGING_TOKEN", p.Token)
	}
	// Trimmed with the manifest's [hook] rules before it left.
	if _, ok := p.Payload["secret_field"]; ok {
		t.Error("untrimmed field sent")
	}
	if s, _ := p.Payload["prompt"].(string); len(s) != 500 {
		t.Errorf("prompt truncated to %d bytes, want 500", len(s))
	}
	if out.String() != `{"ctx":1}` || errb.Len() != 0 {
		t.Fatalf("stdout %q stderr %q", out, errb)
	}
}

// The hook never holds the agent up and never says anything when the
// server is missing, stale or wedged (docs/SPEC.md §8.2).
func TestHookDeadlines(t *testing.T) {
	cases := []struct {
		name  string
		setup func(t *testing.T, sock string)
		event string
		max   time.Duration
	}{
		{"server down", func(*testing.T, string) {}, "Stop", 100 * time.Millisecond},
		{"stale socket", func(t *testing.T, sock string) {
			ln, _ := net.Listen("unix", sock)
			ln.(*net.UnixListener).SetUnlinkOnClose(false)
			ln.Close()
		}, "Stop", 100 * time.Millisecond},
		{"wedged, no response expected", func(t *testing.T, sock string) {
			fakeServer(t, sock, 5*time.Second, "late", nil)
		}, "Stop", hookAck + 150*time.Millisecond},
		{"wedged, response expected", func(t *testing.T, sock string) {
			fakeServer(t, sock, 5*time.Second, "late", nil)
		}, "SessionStart", hookContext + 150*time.Millisecond},
		{"never says hello", func(t *testing.T, sock string) {
			ln, err := net.Listen("unix", sock)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { ln.Close() })
			go func() {
				for {
					c, err := ln.Accept()
					if err != nil {
						return
					}
					defer c.Close()
				}
			}()
		}, "SessionStart", hookContext + 150*time.Millisecond},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e, out, errb, sock := hookEnv(t, `{"hook_event_name":"`+c.event+`"}`)
			c.setup(t, sock)
			if d := runHookTimed(t, e, "--agent", "claude"); d > c.max {
				t.Errorf("took %v, limit %v", d, c.max)
			}
			if out.Len() != 0 || errb.Len() != 0 {
				t.Errorf("stdout %q stderr %q", out, errb)
			}
		})
	}
}

func TestHookIgnoresJunk(t *testing.T) {
	for _, in := range []string{"", "not json", "[1,2]", `{"hook_event_name":1}`} {
		e, out, errb, sock := hookEnv(t, in)
		fakeServer(t, sock, 0, "x", nil)
		runHookTimed(t, e, "--agent", "claude")
		if errb.Len() != 0 || (in != `{"hook_event_name":1}` && out.Len() != 0) {
			t.Errorf("%q: stdout %q stderr %q", in, out, errb)
		}
	}
	// No --agent, or not inside a session: nothing is sent.
	e, out, _, sock := hookEnv(t, `{"hook_event_name":"Stop"}`)
	got := make(chan proto.HookEventParams, 1)
	fakeServer(t, sock, 0, "x", got)
	runHookTimed(t, e)
	if out.Len() != 0 || len(got) != 0 {
		t.Fatal("sent without --agent")
	}
}

// FuzzHookInput: whatever the agent writes on stdin, tm hook exits 0 and
// stays silent on stderr.
func FuzzHookInput(f *testing.F) {
	f.Add([]byte(`{"hook_event_name":"PostToolUse","tool_name":"TaskCreate","tool_input":{"subject":"a"}}`))
	f.Add([]byte(`{"hook_event_name":"SessionStart","source":"clear"}`))
	f.Add([]byte("\xff\xfe{"))
	f.Fuzz(func(t *testing.T, in []byte) {
		var out, errb bytes.Buffer
		e := &Env{Stdin: bytes.NewReader(in), Stdout: &out, Stderr: &errb,
			Getenv: func(k string) string { return map[string]string{"TERMINATR_SESSION": "s-1"}[k] }}
		t.Setenv("TERMINATR_SOCKET", "/nonexistent/tm.sock")
		if code, _ := e.Run([]string{"hook", "--agent", "claude"}); code != 0 || errb.Len() != 0 {
			t.Fatalf("exit %d stderr %q", code, errb.String())
		}
	})
}

// SessionStart's context outlasts the 500 ms of other responses: a slow
// server inside hookContext still delivers it, and one past it does not.
func TestHookSessionStartLimit(t *testing.T) {
	if hookContext < 2*time.Second || hookContext <= hookResponse {
		t.Fatalf("hookContext %v, hookResponse %v", hookContext, hookResponse)
	}
	e, out, _, sock := hookEnv(t, `{"hook_event_name":"SessionStart","source":"clear"}`)
	fakeServer(t, sock, hookResponse+500*time.Millisecond, `{"ctx":1}`, nil)
	runHookTimed(t, e, "--agent", "claude")
	if out.String() != `{"ctx":1}` {
		t.Errorf("slow SessionStart context lost: %q", out)
	}
}
