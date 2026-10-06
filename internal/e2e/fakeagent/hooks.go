package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"syscall"
	"time"
)

// hookGroup is one entry of a hooks.json event list.
type hookGroup struct {
	Matcher string `json:"matcher"`
	Hooks   []struct {
		Type    string  `json:"type"`
		Command string  `json:"command"`
		Timeout float64 `json:"timeout"`
	} `json:"hooks"`
}

// hookCmd is one command hook, ready to run.
type hookCmd struct {
	event   string
	matcher *regexp.Regexp // nil matches everything
	command string
	timeout time.Duration
}

// toolEvents are the events whose matcher is a regex on tool_name.
var toolEvents = map[string]bool{
	"PreToolUse": true, "PostToolUse": true, "PostToolUseFailure": true,
	"PermissionRequest": true, "PermissionDenied": true,
}

// pluginHooks reads DIR/hooks/hooks.json.
func pluginHooks(dir string) (map[string][]hookGroup, error) {
	b, err := os.ReadFile(filepath.Join(dir, "hooks", "hooks.json"))
	if err != nil {
		return nil, err
	}
	var f struct {
		Hooks map[string][]hookGroup `json:"hooks"`
	}
	if err := json.Unmarshal(b, &f); err != nil {
		return nil, err
	}
	return f.Hooks, nil
}

// compileHooks flattens hook groups into commands, in order.
func compileHooks(groups map[string][]hookGroup, events []string) []hookCmd {
	var out []hookCmd
	for _, ev := range events {
		for _, g := range groups[ev] {
			var re *regexp.Regexp
			if toolEvents[ev] && g.Matcher != "" && g.Matcher != "*" {
				re, _ = regexp.Compile("^(?:" + g.Matcher + ")$")
			}
			for _, h := range g.Hooks {
				if h.Type != "command" || h.Command == "" {
					continue
				}
				t := time.Duration(h.Timeout * float64(time.Second))
				if t <= 0 {
					t = 60 * time.Second
				}
				out = append(out, hookCmd{event: ev, matcher: re, command: h.Command, timeout: t})
			}
		}
	}
	return out
}

// eventNames lists the keys of groups in a stable order.
func eventNames(groups map[string][]hookGroup) []string {
	var names []string
	for k := range groups {
		names = append(names, k)
	}
	sort.Strings(names)
	return names
}

// runHookCmd runs one command hook with payload on stdin and returns its
// stdout and exit code (-1 when it could not run or timed out).
func runHookCmd(h hookCmd, payload []byte, dir string, env []string) (string, int) {
	ctx, cancel := context.WithTimeout(context.Background(), h.timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", h.command)
	cmd.Dir = dir
	cmd.Env = env
	cmd.Stdin = bytes.NewReader(payload)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = time.Second
	err := cmd.Run()
	code := 0
	if err != nil {
		code = -1
		var ee *exec.ExitError
		if errors.As(err, &ee) && ctx.Err() == nil {
			code = ee.ExitCode()
		}
	}
	return out.String(), code
}

// fireHook runs every hook for event, in order, unless ctx is cancelled.
// It returns the stdouts and errCancelled when ctx ended before or during
// the run.
func (a *app) fireHook(ctx context.Context, event string, extra map[string]any) ([]string, error) {
	if ctx.Err() != nil {
		return nil, errCancelled
	}
	a.mu.Lock()
	payload := a.basePayloadLocked(event)
	enabled := a.hooksOn
	a.mu.Unlock()
	for k, v := range extra {
		payload[k] = v
	}
	if !enabled {
		return nil, nil
	}
	b, _ := json.Marshal(payload)
	a.hookMu.Lock()
	var outs []string
	for _, h := range a.hooks {
		if h.event != event {
			continue
		}
		if h.matcher != nil {
			name, _ := payload["tool_name"].(string)
			if !h.matcher.MatchString(name) {
				continue
			}
		}
		out, code := runHookCmd(h, b, a.cwd, a.childEnv())
		outs = append(outs, out)
		a.log("hook", map[string]any{"event": event, "payload": json.RawMessage(b), "stdout": out, "exit_code": code})
	}
	a.hookMu.Unlock()
	if ctx.Err() != nil {
		return outs, errCancelled
	}
	return outs, nil
}

// basePayloadLocked has the fields every hook payload carries.
func (a *app) basePayloadLocked(event string) map[string]any {
	mode := "default"
	if a.opts.yolo {
		mode = "bypassPermissions"
	}
	return map[string]any{
		"session_id":      a.sid,
		"transcript_path": transcriptPath(a.home, a.cwd, a.sid),
		"cwd":             a.cwd,
		"permission_mode": mode,
		"hook_event_name": event,
	}
}

// sessionStart fires SessionStart and logs any injected context.
func (a *app) sessionStart(source string) {
	extra := map[string]any{"source": source}
	if a.opts.model != "" {
		extra["model"] = a.opts.model
	}
	outs, _ := a.fireHook(context.Background(), "SessionStart", extra)
	for _, out := range outs {
		var r struct {
			HookSpecificOutput struct {
				AdditionalContext *string `json:"additionalContext"`
			} `json:"hookSpecificOutput"`
		}
		if json.Unmarshal(bytes.TrimSpace([]byte(out)), &r) == nil && r.HookSpecificOutput.AdditionalContext != nil {
			a.log("context", map[string]any{"source": source, "text": *r.HookSpecificOutput.AdditionalContext})
		}
	}
}

// childEnv is the environment of hooks and run steps: ours, plus what
// Claude adds for its children.
func (a *app) childEnv() []string {
	env := os.Environ()
	env = append(env, "CLAUDECODE=1", "CLAUDE_CODE_ENTRYPOINT=cli", "CLAUDE_PROJECT_DIR="+a.cwd)
	if a.sockPath != "" {
		env = append(env, "CLAUDE_CODE_MESSAGING_SOCKET="+a.sockPath, "CLAUDE_CODE_MESSAGING_TOKEN="+a.messagingToken())
	}
	return env
}
