// Package models knows which agents are installed and which models each
// one offers the user (docs/SPEC.md §8.2, Models). tm ships no model
// list: it asks the installed agent through its manifest's
// [list_models], keeps the answer in <home>/state/models/<agent>.json,
// learns the models the user's account refuses from the sessions, and
// lays the user's settings (config.toml [agents.<name>]) over that. An
// agent it can't ask has no models: threads then run its own default.
package models

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/theclifmeister/terminatr/internal/agent"
	"github.com/theclifmeister/terminatr/internal/plat/proc"
)

// ErrTimeout is a probe the agent didn't answer in time.
var ErrTimeout = errors.New("the agent didn't answer in time")

// Probe asks the agent at path for its models, as l says: it runs path
// with l's args in env (nil: tm's own), writes l's lines and reads the
// JSON lines it prints until the replies are in, then stops it. No
// prompt is sent, so no model is called.
func Probe(ctx context.Context, path string, l *agent.ModelLister, env []string) (agent.Listing, error) {
	ctx, cancel := context.WithTimeout(ctx, time.Duration(l.TimeoutSeconds())*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, l.Args...)
	cmd.Env = env
	cmd.Dir = os.TempDir() // no project's files
	cmd.WaitDelay = time.Second
	// Its own process group, so what the agent started stops with it.
	proc.Group(cmd)
	cmd.Cancel = func() error { return proc.KillGroup(cmd.Process.Pid) }
	var stderr tail
	cmd.Stderr = &stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return agent.Listing{}, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return agent.Listing{}, err
	}
	if err := cmd.Start(); err != nil {
		return agent.Listing{}, err
	}
	defer func() {
		_ = proc.KillGroup(cmd.Process.Pid)
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}()
	go func() {
		for _, line := range l.Send {
			if _, err := io.WriteString(stdin, line+"\n"); err != nil {
				return
			}
		}
		// stdin stays open: some agents stop reading at its end.
	}()
	st := l.NewListState()
	r := bufio.NewReaderSize(stdout, 1<<16)
	for !st.Done() {
		line, err := r.ReadBytes('\n')
		if len(bytes.TrimSpace(line)) > 0 {
			var obj map[string]any
			if json.Unmarshal(line, &obj) == nil {
				st.Take(obj)
			}
		}
		if err != nil {
			if ctx.Err() != nil {
				return agent.Listing{}, ErrTimeout
			}
			if msg := stderr.String(); msg != "" {
				return agent.Listing{}, fmt.Errorf("the agent stopped before it answered: %s", msg)
			}
			return agent.Listing{}, errors.New("the agent stopped before it answered")
		}
	}
	return st.Listing()
}

// Version runs the agent's version_args and returns the version they
// print, "" when the manifest has none or the agent doesn't say.
func Version(ctx context.Context, path string, m *agent.Manifest, env []string) string {
	if len(m.Identify.VersionArgs) == 0 {
		return ""
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, m.Identify.VersionArgs...)
	cmd.Env = env
	cmd.WaitDelay = time.Second
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return agent.ParseVersion(string(out))
}

// tail keeps the last line of what a probe wrote to stderr, for its
// error.
type tail struct{ b []byte }

func (t *tail) Write(p []byte) (int, error) {
	t.b = append(t.b, p...)
	if len(t.b) > 4096 {
		t.b = t.b[len(t.b)-4096:]
	}
	return len(p), nil
}

func (t *tail) String() string {
	lines := strings.Split(strings.TrimSpace(string(t.b)), "\n")
	s := strings.TrimSpace(lines[len(lines)-1])
	if r := []rune(s); len(r) > 200 {
		s = string(r[:199]) + "…"
	}
	return s
}
