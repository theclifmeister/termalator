package codex

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/theclifmeister/terminatr/internal/agent"
)

// fakeCodex puts a codex on PATH that writes its arguments, NUL
// separated, to the returned file, then runs body.
func fakeCodex(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	args := filepath.Join(dir, "args")
	script := "#!/bin/sh\nprintf '%s\\0' \"$@\" > '" + args + "'\n" + body + "\n"
	if err := os.WriteFile(filepath.Join(dir, "codex"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return args
}

func readArgs(t *testing.T, path string) []string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	return strings.Split(strings.TrimSuffix(string(b), "\x00"), "\x00")
}

// TestPrompt: the prompt is queued for the session's thread, and every
// case the core must paste instead is an error.
func TestPrompt(t *testing.T) {
	a := load(t)
	if a.Injector() != agent.InjectChannel {
		t.Fatalf("injector %q, want channel", a.Injector())
	}
	target := agent.PromptTarget{SessionID: "s-1", AgentSID: "01a11c21-6753-78d3-acf7-54e1f5a71473"}
	ctx := context.Background()

	args := fakeCodex(t, `echo "Queued message x for thread y."`)
	text := "-x first line\nsecond line"
	if err := a.Prompt(ctx, target, text); err != nil {
		t.Fatal(err)
	}
	want := []string{"queue", "--thread=" + target.AgentSID, "--message=" + text}
	if got := readArgs(t, args); !slices.Equal(got, want) {
		t.Errorf("args %q, want %q", got, want)
	}

	// Not run at all: no thread id yet, or a slash command.
	os.Remove(args)
	if err := a.Prompt(ctx, agent.PromptTarget{SessionID: "s-1"}, "hi"); !errors.Is(err, ErrNoThread) {
		t.Errorf("no thread id: %v", err)
	}
	for _, s := range []string{"/clear", "  /compact now"} {
		if err := a.Prompt(ctx, target, s); !errors.Is(err, ErrSlashCommand) {
			t.Errorf("%q: %v", s, err)
		}
	}
	if readArgs(t, args) != nil {
		t.Error("codex ran")
	}

	// Codex fails: its error line, past the warnings, is in the error.
	fakeCodex(t, `echo "WARNING: proceeding" >&2; echo "Error: failed to queue session message: no rollout found" >&2; exit 1`)
	err := a.Prompt(ctx, target, "hi")
	if err == nil || !strings.Contains(err.Error(), "Error: failed to queue session message: no rollout found") {
		t.Errorf("failure: %v", err)
	}

	// Codex hangs: the timeout ends it.
	fakeCodex(t, `exec sleep 10`)
	start := time.Now()
	if err := a.Prompt(ctx, target, "hi"); err == nil {
		t.Error("a hung codex queue passed")
	}
	if d := time.Since(start); d > queueTimeout+time.Second {
		t.Errorf("took %s", d)
	}

	// No codex at all.
	t.Setenv("PATH", t.TempDir())
	if err := a.Prompt(ctx, target, "hi"); err == nil {
		t.Error("no codex passed")
	}
}
