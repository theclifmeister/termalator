package claude

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/theclifmeister/terminatr/internal/agent"
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
	got := make(chan string, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		line, _ := bufio.NewReader(c).ReadString('\n')
		got <- line
	}()
	target := agent.PromptTarget{Version: "2.1.289", Fields: map[string]string{SocketField: sock}}
	if err := a.Prompt(context.Background(), target, "hello\nworld"); err != nil {
		t.Fatal(err)
	}
	var msg struct {
		Type    string
		Message struct{ Role, Content string }
	}
	if err := json.Unmarshal([]byte(<-got), &msg); err != nil {
		t.Fatal(err)
	}
	if msg.Type != "user" || msg.Message.Role != "user" || msg.Message.Content != "hello\nworld" {
		t.Fatalf("line %+v", msg)
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
