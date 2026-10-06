package session

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/theclifmeister/terminatr/internal/agent"
	"github.com/theclifmeister/terminatr/internal/proto"
)

// startMod is startBox in a session that runs the mod; live says the
// mod has reported (idle), so it delivers the queue.
func startMod(t *testing.T, box string, live bool, resolved chan<- PromptResolution) *Session {
	t.Helper()
	script := `printf '\n\342\224\200\342\224\200\342\224\200\n\342\235\257\302\240` + box + `\n\342\224\200\342\224\200\342\224\200\n'; exec cat`
	s, err := Start(Config{
		ID: "s-mod", Role: proto.RoleThread, Project: "p",
		Argv: []string{"/bin/sh", "-c", script},
		Cwd:  t.TempDir(),
		Env:  []string{"PATH=/usr/bin:/bin", "TERM=xterm-256color", "LANG=C.UTF-8"},
		Cols: 80, Rows: 24,
		Logf: t.Logf,
		Agent: &AgentConfig{
			Agent: claudeLike(t), Home: t.TempDir(), PromptHold: time.Hour, ModSocket: "/nowhere/mod.sock",
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
	if live {
		if err := s.ModState(agent.StateIdle, "", "session.start"); err != nil {
			t.Fatal(err)
		}
	}
	eventually(t, "the prompt box", func() bool { return strings.Contains(screen(t, s), "❯") })
	return s
}

// nextMod is the mod's poll, bounded.
func nextMod(t *testing.T, s *Session) ModPrompt {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	p, ok, err := s.NextModPrompt(ctx)
	if err != nil || !ok {
		t.Fatalf("NextModPrompt: %v %v", ok, err)
	}
	return p
}

func ack(t *testing.T, s *Session, id, result string) {
	t.Helper()
	if err := s.AckModPrompt(id, result, ""); err != nil {
		t.Fatalf("ack %s %s: %v", id, result, err)
	}
}

// TestModPromptBusyComposer: with the mod live, a prompt and a slash
// command go to the mod in order while the agent works and a draft sits
// in the box; nothing is pasted over the draft, and each stays queued
// until the mod acks it submitted.
func TestModPromptBusyComposer(t *testing.T) {
	s := startMod(t, "half typed draft", true, make(chan PromptResolution, 1))
	if err := s.ModState(agent.StateWorking, "", "turn.start"); err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{"Continue with step 2", "/remote-control tm-x"} {
		if via, err := s.Prompt(text); err != nil || via != "queued" {
			t.Fatalf("Prompt %q: %q %v", text, via, err)
		}
	}
	p := nextMod(t, s)
	if p.Kind != "prompt" || p.Text != "Continue with step 2" || p.ID == "" {
		t.Fatalf("first offer %+v", p)
	}
	if again := nextMod(t, s); again != p {
		t.Fatalf("offered again before the ack: %+v, want %+v", again, p)
	}
	ack(t, s, p.ID, ModTaken)
	if st, _ := s.AgentState(); st.Queued != 2 || st.Held != "" {
		t.Fatalf("taken: %+v", st)
	}
	ack(t, s, p.ID, ModSubmitted)
	c := nextMod(t, s)
	if c.Kind != "command" || c.Command != "remote-control" || c.Args != "tm-x" || c.ID == p.ID {
		t.Fatalf("second offer %+v", c)
	}
	ack(t, s, c.ID, ModTaken)
	ack(t, s, c.ID, ModSubmitted)
	if st, _ := s.AgentState(); st.Queued != 0 {
		t.Fatalf("after both: %+v", st)
	}
	if err := s.AckModPrompt(c.ID, ModSubmitted, ""); !errors.Is(err, ErrNoModPrompt) {
		t.Fatalf("a second ack: %v", err)
	}
	// Idle again with the draft still there: nothing was pasted.
	if err := s.ModState(agent.StateIdle, "", "turn.complete"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	if sc := screen(t, s); !strings.Contains(sc, "half typed draft") || strings.Contains(sc, "step 2") || strings.Contains(sc, "remote-control") {
		t.Fatalf("screen:\n%s", sc)
	}
}

// TestModPromptRestart: a mod that restarts after taking a prompt is
// offered the same one again (it acks it from its stored id); later
// prompts wait behind it.
func TestModPromptRestart(t *testing.T) {
	s := startMod(t, "", true, make(chan PromptResolution, 1))
	s.Prompt("first")
	s.Prompt("second")
	p := nextMod(t, s)
	ack(t, s, p.ID, ModTaken)
	// The mod reloads: its next poll gets the same head.
	if again := nextMod(t, s); again.ID != p.ID {
		t.Fatalf("after a restart: %+v, want %s", again, p.ID)
	}
	ack(t, s, p.ID, ModSubmitted)
	if q := nextMod(t, s); q.Text != "second" {
		t.Fatalf("then: %+v", q)
	}
	if strings.Contains(screen(t, s), "first") {
		t.Fatal("pasted although the mod had it")
	}
}

// TestModPromptFallback: an offer the mod doesn't take in time, and one
// it refuses, are pasted; the mod can't take one back after that.
func TestModPromptFallback(t *testing.T) {
	old := ModAckTimeout
	ModAckTimeout = 300 * time.Millisecond
	t.Cleanup(func() { ModAckTimeout = old })
	s := startMod(t, "", true, make(chan PromptResolution, 1))

	s.Prompt("never-taken")
	p := nextMod(t, s)
	eventually(t, "the paste after no ack", func() bool { return strings.Contains(screen(t, s), "never-taken") })
	if err := s.AckModPrompt(p.ID, ModTaken, ""); !errors.Is(err, ErrNoModPrompt) {
		t.Fatalf("a late take: %v", err)
	}

	// The agent is busy with the paste; the next prompt is the mod's.
	s.ModState(agent.StateWorking, "", "turn.start")
	s.Prompt("refused-one")
	r := nextMod(t, s)
	ack(t, s, r.ID, ModTaken)
	if err := s.AckModPrompt(r.ID, ModRefused, "blocked by a hook"); err != nil {
		t.Fatal(err)
	}
	if err := s.AckModPrompt(r.ID, ModTaken, ""); !errors.Is(err, ErrNoModPrompt) {
		t.Fatalf("taken after refused: %v", err)
	}
	if err := s.AckModPrompt(r.ID, "maybe", ""); !errors.Is(err, ErrModAck) {
		t.Fatalf("bad result: %v", err)
	}
	s.ModState(agent.StateIdle, "", "turn.complete")
	eventually(t, "the paste after a refusal", func() bool { return strings.Contains(screen(t, s), "refused-one") })
}

// TestModPromptNotLive: a session with the mod that never heard from it
// (old Claude, mod failed to load) pastes as before, and the mod's poll
// gets nothing.
func TestModPromptNotLive(t *testing.T) {
	s := startMod(t, "", false, make(chan PromptResolution, 1))
	s.Prompt("pasted-anyway")
	eventually(t, "the paste", func() bool { return strings.Contains(screen(t, s), "pasted-anyway") })
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	if p, ok, err := s.NextModPrompt(ctx); ok || err != nil {
		t.Fatalf("poll without a live mod: %+v %v %v", p, ok, err)
	}
}

// TestModPromptRefresh: a nudge is refreshed once, before the mod first
// sees it; a stale one is dropped and the next offered.
func TestModPromptRefresh(t *testing.T) {
	resolved := make(chan PromptResolution, 1)
	s := startMod(t, "", true, resolved)
	s.ModState(agent.StateWorking, "", "turn.start")
	s.PromptWith("stale-nudge", PromptOptions{Refresh: func() (string, bool) { return "", false }})
	calls := 0
	s.PromptWith("old-nudge", PromptOptions{Refresh: func() (string, bool) { calls++; return "new-nudge", true }})
	p := nextMod(t, s)
	if p.Text != "new-nudge" {
		t.Fatalf("offer %+v", p)
	}
	if r := <-resolved; r.Via != "stale" || r.Text != "stale-nudge" {
		t.Fatalf("resolution %+v", r)
	}
	nextMod(t, s)
	if calls != 1 {
		t.Fatalf("refreshed %d times", calls)
	}
}

func TestSlashCommand(t *testing.T) {
	for _, c := range []struct {
		text, name, args string
		ok               bool
	}{
		{"/remote-control tm-x", "remote-control", "tm-x", true},
		{"  /compact  ", "compact", "", true},
		{"/plugin:cmd a b", "plugin:cmd", "a b", true},
		{"/compact\nmore", "", "", false},
		{"/ space", "", "", false},
		{"/a/b", "", "", false},
		{"hello /compact", "", "", false},
	} {
		name, args, ok := slashCommand(c.text)
		if name != c.name || args != c.args || ok != c.ok {
			t.Errorf("%q: %q %q %v", c.text, name, args, ok)
		}
	}
}
