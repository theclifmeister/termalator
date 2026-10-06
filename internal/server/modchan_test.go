package server

import (
	"context"
	"encoding/json"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/theclifmeister/terminatr/internal/agent"
	"github.com/theclifmeister/terminatr/internal/proto"
	"github.com/theclifmeister/terminatr/internal/session"
)

// modClient posts to a mod socket as the mod's $.http.fetch does.
func modClient(path string) *http.Client {
	return &http.Client{Timeout: 2 * time.Second, Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", path)
		},
	}}
}

func postMod(t *testing.T, c *http.Client, path, body string) int {
	t.Helper()
	resp, err := c.Post("http://terminatr"+path, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST %s: %v", path, err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

// TestModChannel: a session with the mod reports its state over its own
// socket; the server takes it, refuses what isn't a state, and closes
// the socket with the session.
func TestModChannel(t *testing.T) {
	rt, err := os.MkdirTemp("/tmp", "tmmod")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(rt) })
	b, _ := agent.Builtin("claude")
	m, err := agent.ParseManifest(b)
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{log: log.New(os.Stderr, "", 0), sessions: map[string]*session.Session{}}
	s.mu.Lock()
	sock, err := s.listenMod("s-1", rt)
	s.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	sess, err := session.Start(session.Config{
		ID: "s-1", Role: proto.RoleThread, Argv: []string{"/bin/sh", "-c", "exec cat"}, Cwd: rt,
		Env: []string{"PATH=/usr/bin:/bin"}, Cols: 80, Rows: 24, Logf: t.Logf,
		Agent: &session.AgentConfig{Agent: agent.FromManifest(m), Home: rt, ModSocket: sock},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sess.Stop(time.Second) })
	c := modClient(sock)

	// Before the session is registered, a report has nowhere to go.
	if code := postMod(t, c, "/v1/state", `{"state":"idle","event":"session.start"}`); code != http.StatusGone {
		t.Fatalf("no session: %d", code)
	}
	s.mu.Lock()
	s.sessions["s-1"] = sess
	s.mu.Unlock()

	if code := postMod(t, c, "/v1/state", `{"state":"working","event":"turn.start"}`); code != http.StatusNoContent {
		t.Fatalf("working: %d", code)
	}
	if st, _ := sess.AgentState(); st.State != agent.StateWorking || st.Sources != "mod" {
		t.Fatalf("after turn.start: %+v", st.Merged)
	}
	if code := postMod(t, c, "/v1/state", `{"state":"blocked","reason":"question","event":"tool.call"}`); code != http.StatusNoContent {
		t.Fatalf("blocked: %d", code)
	}
	e, _ := sess.Explain()
	if e.Result.State != agent.StateBlocked || e.Mod == nil || !e.Mod.Live || e.Mod.Event != "tool.call" || e.Extra["mod_socket"] != sock {
		t.Fatalf("explain: %+v mod %+v extra %v", e.Result, e.Mod, e.Extra)
	}
	for _, bad := range []string{`{"state":"asleep"}`, `not json`, `{"state":"idle","reason":"` + strings.Repeat("x", 100) + `"}`} {
		if code := postMod(t, c, "/v1/state", bad); code != http.StatusBadRequest {
			t.Fatalf("%s: %d", bad, code)
		}
	}
	if code := postMod(t, c, "/v1/other", `{}`); code != http.StatusNotFound {
		t.Fatalf("other path: %d", code)
	}
	if fi, err := os.Stat(sock); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("socket: %v %v", fi, err)
	}

	// The session ends: its socket goes with it.
	s.mu.Lock()
	s.closeModLocked("s-1")
	s.mu.Unlock()
	if _, err := c.Post("http://terminatr/v1/state", "application/json", strings.NewReader(`{"state":"idle"}`)); err == nil {
		t.Fatal("the socket still answers after the session")
	}
}

// TestModPrompts: the mod's long poll gets the head of the prompt queue
// once it is live, and its acks move the queue on; what isn't an ack is
// refused.
func TestModPrompts(t *testing.T) {
	rt, err := os.MkdirTemp("/tmp", "tmmod")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(rt) })
	b, _ := agent.Builtin("claude")
	m, err := agent.ParseManifest(b)
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{log: log.New(os.Stderr, "", 0), sessions: map[string]*session.Session{}}
	s.mu.Lock()
	sock, err := s.listenMod("s-1", rt)
	s.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	sess, err := session.Start(session.Config{
		ID: "s-1", Role: proto.RoleThread, Argv: []string{"/bin/sh", "-c", "exec cat"}, Cwd: rt,
		Env: []string{"PATH=/usr/bin:/bin"}, Cols: 80, Rows: 24, Logf: t.Logf,
		Agent: &session.AgentConfig{Agent: agent.FromManifest(m), Home: rt, ModSocket: sock},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sess.Stop(time.Second) })
	c := modClient(sock)
	c.Timeout = 10 * time.Second
	poll := func(wait string) (int, session.ModPrompt) {
		t.Helper()
		resp, err := c.Get("http://terminatr/v1/prompts?wait=" + wait)
		if err != nil {
			t.Fatalf("GET: %v", err)
		}
		defer resp.Body.Close()
		var p session.ModPrompt
		if resp.StatusCode == http.StatusOK {
			if err := json.NewDecoder(resp.Body).Decode(&p); err != nil {
				t.Fatal(err)
			}
		}
		return resp.StatusCode, p
	}

	if code, _ := poll("0"); code != http.StatusGone {
		t.Fatalf("no session: %d", code)
	}
	s.mu.Lock()
	s.sessions["s-1"] = sess
	s.mu.Unlock()
	postMod(t, c, "/v1/state", `{"state":"working","event":"turn.start"}`)
	if code, _ := poll("0"); code != http.StatusNoContent {
		t.Fatalf("empty queue: %d", code)
	}

	// A prompt queued while the poll waits reaches it.
	go func() {
		time.Sleep(200 * time.Millisecond)
		sess.Prompt("/compact keep the plan")
	}()
	code, p := poll("5")
	if code != http.StatusOK || p.Kind != "command" || p.Command != "compact" || p.Args != "keep the plan" {
		t.Fatalf("poll: %d %+v", code, p)
	}
	for _, c2 := range []struct {
		path, body string
		want       int
	}{
		{"/v1/prompts/" + p.ID + "/ack", `{"result":"maybe"}`, http.StatusBadRequest},
		{"/v1/prompts/" + p.ID + "/ack", `nope`, http.StatusBadRequest},
		{"/v1/prompts/other/ack", `{"result":"taken"}`, http.StatusNotFound},
		{"/v1/prompts/" + p.ID + "/ack", `{"result":"taken"}`, http.StatusNoContent},
		{"/v1/prompts/" + p.ID + "/ack", `{"result":"submitted"}`, http.StatusNoContent},
		{"/v1/prompts/" + p.ID + "/ack", `{"result":"submitted"}`, http.StatusNotFound},
	} {
		if code := postMod(t, c, c2.path, c2.body); code != c2.want {
			t.Fatalf("POST %s %s: %d, want %d", c2.path, c2.body, code, c2.want)
		}
	}
	if st, _ := sess.AgentState(); st.Queued != 0 {
		t.Fatalf("queued after the ack: %d", st.Queued)
	}
}
