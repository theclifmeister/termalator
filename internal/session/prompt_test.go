package session

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/theclifmeister/terminatr/internal/agent"
	"github.com/theclifmeister/terminatr/internal/proto"
)

// chanAgent is the built-in claude manifest with a channel that records
// what it was sent (or fails with err).
type chanAgent struct {
	agent.Agent
	m   *agent.Manifest
	err error

	mu     sync.Mutex
	sent   []string
	tokens []string // PromptTarget.Token of each send
}

func (a *chanAgent) Manifest() *agent.Manifest { return a.m }

func (a *chanAgent) Prompt(_ context.Context, t agent.PromptTarget, text string) error {
	if a.err != nil {
		return a.err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.sent = append(a.sent, text)
	a.tokens = append(a.tokens, t.Token)
	return nil
}

func (a *chanAgent) Tokens() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.tokens...)
}

func (a *chanAgent) Sent() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.sent...)
}

func claudeLike(t *testing.T) *chanAgent {
	t.Helper()
	b, ok := agent.Builtin("claude")
	if !ok {
		t.Fatal("no built-in claude manifest")
	}
	m, err := agent.ParseManifest(b)
	if err != nil {
		t.Fatal(err)
	}
	return &chanAgent{Agent: agent.FromManifest(m), m: m}
}

// startBox runs a program that draws Claude's prompt box with box after
// the "❯" and a no-break space (typed text, unless empty), then echoes
// what it is sent. The agent reads idle (a SessionStart hook).
func startBox(t *testing.T, a agent.Agent, box string, hold time.Duration, resolved chan<- PromptResolution) *Session {
	t.Helper()
	script := `printf '\n\342\224\200\342\224\200\342\224\200\n\342\235\257\302\240` + box + `\n\342\224\200\342\224\200\342\224\200\n'; exec cat`
	s, err := Start(Config{
		ID: "s-test", Role: proto.RoleCoordinator, Project: "p",
		Argv: []string{"/bin/sh", "-c", script},
		Cwd:  t.TempDir(),
		Env:  []string{"PATH=/usr/bin:/bin", "TERM=xterm-256color", "LANG=C.UTF-8"},
		Cols: 80, Rows: 24,
		Logf: t.Logf,
		Agent: &AgentConfig{
			Agent: a, Home: t.TempDir(), PromptHold: hold,
			OnPromptResolved: func(_ *Session, r PromptResolution) { resolved <- r },
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Stop(time.Second) })
	if _, err := s.Hook("SessionStart", map[string]any{"source": "startup"}); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the prompt box", func() bool { return strings.Contains(screen(t, s), "❯") })
	return s
}

// TestPromptPastedIntoEmptyBox: the usual path still pastes at once.
func TestPromptPastedIntoEmptyBox(t *testing.T) {
	resolved := make(chan PromptResolution, 1)
	s := startBox(t, claudeLike(t), "", time.Hour, resolved)
	if via, err := s.Prompt("hello-paste"); err != nil || via != "queued" {
		t.Fatalf("Prompt: %q %v", via, err)
	}
	eventually(t, "the paste", func() bool { return strings.Contains(screen(t, s), "hello-paste") })
	if st, _ := s.AgentState(); st.Queued != 0 || st.Held != "" {
		t.Fatalf("after the paste: %+v", st)
	}
}

// TestHeldPromptDropped reproduces T43: text left in the prompt box (the
// user typed and walked away, or drives the agent over remote control)
// held a queued prompt forever, and the coordinator's nudges with it.
// Now the hold shows in the agent state and, after PromptHold, the
// prompt is dropped.
func TestHeldPromptDropped(t *testing.T) {
	resolved := make(chan PromptResolution, 1)
	s := startBox(t, claudeLike(t), "whats still open?", 1500*time.Millisecond, resolved)
	before := time.Now()
	if _, err := s.Prompt("[tm] 2 new inbox items"); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the hold", func() bool {
		st, _ := s.AgentState()
		return st.Held == HeldBox && st.Queued == 1
	})
	st, _ := s.AgentState()
	if st.QueuedSince.Before(before) || st.HeldSince.Before(before) {
		t.Fatalf("since: %+v", st)
	}
	select {
	case r := <-resolved:
		if r.Via != "dropped" || r.Why != HeldBox || r.Text != "[tm] 2 new inbox items" || r.Held < 1500*time.Millisecond {
			t.Fatalf("resolution %+v", r)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the held prompt was never resolved")
	}
	if st, _ := s.AgentState(); st.Queued != 0 || st.Held != "" {
		t.Fatalf("after the drop: %+v", st)
	}
	if strings.Contains(screen(t, s), "inbox items") {
		t.Fatal("a held prompt was pasted onto the user's text")
	}
}

// TestHeldPromptChannel: a server prompt that may take the channel goes
// through it once held for PromptHold, with the token the hooks
// reported, and reads as sent (the channel can't confirm delivery); one
// whose channel fails is dropped with the error.
func TestHeldPromptChannel(t *testing.T) {
	a := claudeLike(t)
	resolved := make(chan PromptResolution, 1)
	s := startBox(t, a, "draft", 500*time.Millisecond, resolved)
	s.SetPromptToken("child-token")
	if _, err := s.PromptWith("[tm] nudge", PromptOptions{Channel: true}); err != nil {
		t.Fatal(err)
	}
	select {
	case r := <-resolved:
		if r.Via != "sent" || r.Err != nil {
			t.Fatalf("resolution %+v", r)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("never resolved")
	}
	if got := a.Sent(); len(got) != 1 || got[0] != "[tm] nudge" {
		t.Fatalf("channel got %q", got)
	}
	if got := a.Tokens(); len(got) != 1 || got[0] != "child-token" {
		t.Fatalf("channel tokens %q", got)
	}

	a.err = context.DeadlineExceeded
	if _, err := s.PromptWith("[tm] again", PromptOptions{Channel: true}); err != nil {
		t.Fatal(err)
	}
	select {
	case r := <-resolved:
		if r.Via != "dropped" || r.Err == nil {
			t.Fatalf("resolution %+v", r)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("never resolved")
	}
}

// TestPromptNotHeldWhileWorking: a prompt waiting for a working agent
// waits its turn, however long; the bound only counts idle time.
func TestPromptNotHeldWhileWorking(t *testing.T) {
	resolved := make(chan PromptResolution, 1)
	s := startBox(t, claudeLike(t), "draft", 300*time.Millisecond, resolved)
	if _, err := s.Hook("UserPromptSubmit", map[string]any{"prompt": "go"}); err != nil {
		t.Fatal(err)
	}
	eventually(t, "working", func() bool {
		st, _ := s.AgentState()
		return st.State == agent.StateWorking
	})
	if _, err := s.Prompt("later"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Second)
	select {
	case r := <-resolved:
		t.Fatalf("resolved while working: %+v", r)
	default:
	}
	if st, _ := s.AgentState(); st.Queued != 1 || st.Held != "" {
		t.Fatalf("while working: %+v", st)
	}
}

// TestPromptRefresh: Refresh runs at delivery; its text is what is
// pasted, and a stale prompt is not delivered at all.
func TestPromptRefresh(t *testing.T) {
	resolved := make(chan PromptResolution, 1)
	s := startBox(t, claudeLike(t), "", time.Hour, resolved)
	if _, err := s.PromptWith("old-text", PromptOptions{Refresh: func() (string, bool) { return "", false }}); err != nil {
		t.Fatal(err)
	}
	select {
	case r := <-resolved:
		if r.Via != "stale" || r.Text != "old-text" {
			t.Fatalf("resolution %+v", r)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the stale prompt was never resolved")
	}
	if _, err := s.PromptWith("old-text", PromptOptions{Refresh: func() (string, bool) { return "new-text", true }}); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the refreshed paste", func() bool { return strings.Contains(screen(t, s), "new-text") })
	if strings.Contains(screen(t, s), "old-text") {
		t.Fatal("the stale text was pasted")
	}
}
