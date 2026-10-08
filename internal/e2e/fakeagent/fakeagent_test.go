//go:build unix

package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/creack/pty"
)

var binPath string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "fakeagent-bin")
	if err != nil {
		panic(err)
	}
	binPath = filepath.Join(dir, "fakeagent")
	out, err := exec.Command("go", "build", "-o", binPath, ".").CombinedOutput()
	if err != nil {
		fmt.Fprintf(os.Stderr, "build: %v\n%s", err, out)
		os.Exit(1)
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

// hookScript appends each payload to $HOOKLOG and answers SessionStart
// with additionalContext.
const hookScript = `#!/bin/sh
p=$(cat)
printf '%s\n' "$p" >> "$HOOKLOG"
case "$p" in *'"hook_event_name":"SessionStart"'*)
  printf '{"hookSpecificOutput":{"hookEventName":"SessionStart","additionalContext":"CTX-%s"}}' "$TERMINATR_SESSION";;
esac
`

// agent is one fake agent running under a PTY.
type agent struct {
	t       *testing.T
	home    string
	cwd     string
	hookLog string
	log     string
	cmd     *exec.Cmd
	pty     *os.File
	mu      sync.Mutex
	out     bytes.Buffer
	done    chan struct{}
	err     error
}

type agentOpts struct {
	args       []string
	untrusted  bool
	settings   string
	env        []string
	noBypassOK bool
}

func newEnv(t *testing.T) (home, cwd, plugin string) {
	t.Helper()
	base := t.TempDir()
	home = filepath.Join(base, "home")
	cwd = filepath.Join(base, "work")
	plugin = filepath.Join(base, "plugin")
	for _, d := range []string{home, cwd, filepath.Join(plugin, "hooks")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	cwd = realPath(cwd)
	script := filepath.Join(plugin, "hook.sh")
	if err := os.WriteFile(script, []byte(hookScript), 0o755); err != nil {
		t.Fatal(err)
	}
	h := fmt.Sprintf(`[{"hooks":[{"type":"command","command":%q,"timeout":5}]}]`, script)
	var events []string
	for _, e := range []string{"SessionStart", "UserPromptSubmit", "PreToolUse", "PostToolUse", "PostToolUseFailure",
		"PermissionRequest", "PermissionDenied", "Notification", "Stop", "StopFailure", "SubagentStart",
		"SubagentStop", "TaskCreated", "TaskCompleted", "PreCompact", "SessionEnd"} {
		events = append(events, fmt.Sprintf("%q: %s", e, h))
	}
	if err := os.WriteFile(filepath.Join(plugin, "hooks", "hooks.json"), []byte(`{"hooks": {`+strings.Join(events, ",")+`}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	return home, cwd, plugin
}

func start(t *testing.T, o agentOpts) *agent {
	t.Helper()
	home, cwd, plugin := newEnv(t)
	return startIn(t, home, cwd, plugin, o)
}

func startIn(t *testing.T, home, cwd, plugin string, o agentOpts) *agent {
	t.Helper()
	a := &agent{t: t, home: home, cwd: cwd, hookLog: filepath.Join(home, "hooks.jsonl"), log: filepath.Join(home, "fa.jsonl"), done: make(chan struct{})}
	if !o.untrusted {
		m := map[string]any{"projects": map[string]any{cwd: map[string]any{"hasTrustDialogAccepted": true}}}
		if !o.noBypassOK {
			m["bypassPermissionsModeAccepted"] = true
		}
		b, _ := json.Marshal(m)
		if err := os.WriteFile(filepath.Join(home, ".claude.json"), b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	args := []string{"--plugin-dir", plugin}
	if o.settings != "" {
		args = append(args, "--settings", o.settings)
	}
	args = append(args, o.args...)
	a.cmd = exec.Command(binPath, args...)
	a.cmd.Dir = cwd
	a.cmd.Env = append(os.Environ(), "HOME="+home, "HOOKLOG="+a.hookLog, "FAKEAGENT_LOG="+a.log,
		"FAKEAGENT_TRUST_DEBOUNCE_MS=300", "TERMINATR_SESSION=s1")
	a.cmd.Env = append(a.cmd.Env, o.env...)
	f, err := pty.StartWithSize(a.cmd, &pty.Winsize{Rows: 40, Cols: 120})
	if err != nil {
		t.Fatal(err)
	}
	a.pty = f
	go func() {
		buf := make([]byte, 8192)
		for {
			n, err := f.Read(buf)
			a.mu.Lock()
			a.out.Write(buf[:n])
			a.mu.Unlock()
			if err != nil {
				return
			}
		}
	}()
	go func() {
		a.err = a.cmd.Wait()
		close(a.done)
	}()
	t.Cleanup(func() {
		select {
		case <-a.done:
		default:
			a.cmd.Process.Kill()
			<-a.done
		}
		f.Close()
	})
	return a
}

func (a *agent) send(s string) {
	a.t.Helper()
	if _, err := a.pty.WriteString(s); err != nil {
		a.t.Fatal(err)
	}
}

// esc sends a lone Esc and lets it time out as one.
func (a *agent) esc() {
	a.send("\x1b")
	time.Sleep(80 * time.Millisecond)
}

func (a *agent) mark() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.out.Len()
}

// sendUntil sends keys until cond holds. Trust and bypass dialogs drop
// keys that arrive soon after they paint, as Claude's do, and on a slow
// machine "soon" is longer than any fixed sleep.
func (a *agent) sendUntil(keys string, cond func() bool) {
	a.t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			a.t.Fatalf("keys %q never took effect (events %v)", keys, a.events())
		}
		a.send(keys)
		for end := time.Now().Add(500 * time.Millisecond); time.Now().Before(end) && !cond(); {
			time.Sleep(20 * time.Millisecond)
		}
	}
}

func waitUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// waitScreen waits for s in the output written after mark.
func (a *agent) waitScreen(since int, s string) {
	a.t.Helper()
	waitUntil(a.t, "screen "+s, func() bool {
		a.mu.Lock()
		defer a.mu.Unlock()
		return bytes.Contains(a.out.Bytes()[since:], []byte(s))
	})
}

type rec map[string]any

func readJSONL(path string) []rec {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	var out []rec
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<24)
	for sc.Scan() {
		var r rec
		if json.Unmarshal(sc.Bytes(), &r) == nil {
			out = append(out, r)
		}
	}
	return out
}

func (a *agent) hooks() []rec { return readJSONL(a.hookLog) }

func (a *agent) events() []string {
	var ev []string
	for _, h := range a.hooks() {
		ev = append(ev, fmt.Sprint(h["hook_event_name"]))
	}
	return ev
}

func count(ev []string, name string) int {
	n := 0
	for _, e := range ev {
		if e == name {
			n++
		}
	}
	return n
}

func (a *agent) waitEvent(name string, n int) {
	a.t.Helper()
	waitUntil(a.t, fmt.Sprintf("%d× %s (have %v)", n, name, a.events()), func() bool { return count(a.events(), name) >= n })
}

func (a *agent) session() rec {
	b, err := os.ReadFile(filepath.Join(a.home, ".claude", "sessions", fmt.Sprintf("%d.json", a.cmd.Process.Pid)))
	if err != nil {
		return nil
	}
	var r rec
	_ = json.Unmarshal(b, &r)
	return r
}

func (a *agent) waitStatus(status, waitingFor string) {
	a.t.Helper()
	waitUntil(a.t, "status "+status+"/"+waitingFor, func() bool {
		s := a.session()
		wf, _ := s["waitingFor"].(string)
		return s != nil && s["status"] == status && wf == waitingFor
	})
}

func (a *agent) transcript(sid string) []rec {
	return readJSONL(transcriptPath(a.home, a.cwd, sid))
}

func (a *agent) sid() string {
	s, _ := a.session()["sessionId"].(string)
	return s
}

func (a *agent) waitExit() int {
	a.t.Helper()
	select {
	case <-a.done:
	case <-time.After(8 * time.Second):
		a.t.Fatal("agent did not exit")
	}
	if ee, ok := a.err.(*exec.ExitError); ok {
		return ee.ExitCode()
	}
	return 0
}

func (a *agent) logKinds(kind string) []rec {
	var out []rec
	for _, r := range readJSONL(a.log) {
		if r["kind"] == kind {
			out = append(out, r)
		}
	}
	return out
}

func TestTurnHookOrder(t *testing.T) {
	t.Parallel()
	sid := newUUID()
	a := start(t, agentOpts{args: []string{"--session-id", sid, "--model", "haiku", "--", "hello"}})
	a.waitEvent("Stop", 1)
	want := []string{"SessionStart", "UserPromptSubmit", "Stop"}
	if got := a.events(); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("events %v, want %v", got, want)
	}
	h := a.hooks()
	if h[0]["source"] != "startup" || h[0]["session_id"] != sid || h[0]["model"] != "haiku" || h[0]["permission_mode"] != "default" {
		t.Fatalf("SessionStart %v", h[0])
	}
	if h[1]["prompt"] != "hello" || h[2]["last_assistant_message"] != "ok: hello" {
		t.Fatalf("payloads %v %v", h[1], h[2])
	}
	if h[2]["transcript_path"] != transcriptPath(a.home, a.cwd, sid) || h[2]["cwd"] != a.cwd {
		t.Fatalf("common fields %v", h[2])
	}
	a.waitStatus("idle", "")
	if s := a.session(); s["sessionId"] != sid || s["version"] != "2.1.289" || s["name"] != "fake" || s["messagingSocketPath"] == "" {
		t.Fatalf("session file %v", s)
	}
	tr := a.transcript(sid)
	var types []string
	for _, r := range tr {
		types = append(types, fmt.Sprint(r["type"], "/", r["subtype"]))
	}
	if strings.Join(types, ",") != "user/<nil>,assistant/<nil>,system/turn_duration" {
		t.Fatalf("transcript %v", types)
	}
	if c := a.logKinds("context"); len(c) != 1 || c[0]["text"] != "CTX-s1" || c[0]["source"] != "startup" {
		t.Fatalf("context log %v", c)
	}
	if p := a.logKinds("prompt"); len(p) != 1 || p[0]["via"] != "kickoff" {
		t.Fatalf("prompt log %v", p)
	}
	m := a.mark()
	a.send("/exit\r")
	if code := a.waitExit(); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if ev := a.events(); ev[len(ev)-1] != "SessionEnd" || a.hooks()[len(ev)-1]["reason"] != "prompt_input_exit" {
		t.Fatalf("end %v", ev)
	}
	if a.session() != nil {
		t.Fatal("session file left behind")
	}
	_ = m
}

func TestPermissionEscIsSilent(t *testing.T) {
	t.Parallel()
	a := start(t, agentOpts{})
	a.waitStatus("idle", "")
	m := a.mark()
	a.send("run permission\r")
	a.waitScreen(m, "Do you want to create x.txt?")
	a.waitScreen(m, "❯ 1. Yes")
	a.waitScreen(m, "Esc to cancel · Tab to amend")
	a.waitStatus("waiting", "permission prompt")
	before := a.events()
	if before[len(before)-1] != "PermissionRequest" {
		t.Fatalf("events %v", before)
	}
	m = a.mark()
	a.esc()
	a.waitStatus("idle", "")
	a.waitScreen(m, "❯ run permission") // the prompt is put back
	time.Sleep(200 * time.Millisecond)
	if after := a.events(); len(after) != len(before) {
		t.Fatalf("hooks after Esc: %v", after[len(before):])
	}
	tr := a.transcript(a.sid())
	last := tr[len(tr)-1]
	if !strings.Contains(fmt.Sprint(last["message"]), "[Request interrupted by user for tool use]") {
		t.Fatalf("last transcript entry %v", last)
	}

	// Approve this time.
	a.send("\x15run permission\r")
	m = a.mark()
	a.waitStatus("waiting", "permission prompt")
	a.send("1")
	a.waitEvent("Stop", 1)
	ev := a.events()
	want := "UserPromptSubmit,PreToolUse,PermissionRequest,PostToolUse,Stop"
	if got := strings.Join(ev[len(ev)-5:], ","); got != want {
		t.Fatalf("approve events %v", got)
	}
	a.waitStatus("idle", "")
	_ = m
}

func TestEscWhileStreaming(t *testing.T) {
	t.Parallel()
	a := start(t, agentOpts{})
	a.waitStatus("idle", "")
	m := a.mark()
	a.send("run stream-long\r")
	a.waitScreen(m, "✢ Churning… (")
	a.waitScreen(m, "\x1b]0;◐ Fake Claude")
	a.waitStatus("busy", "")
	a.esc()
	a.waitStatus("idle", "")
	time.Sleep(150 * time.Millisecond)
	if ev := a.events(); count(ev, "Stop") != 0 {
		t.Fatalf("Stop after Esc: %v", ev)
	}
	tr := a.transcript(a.sid())
	if !strings.Contains(fmt.Sprint(tr[len(tr)-1]["message"]), "[Request interrupted by user]") {
		t.Fatalf("transcript %v", tr[len(tr)-1])
	}
}

func TestClearRotatesSession(t *testing.T) {
	t.Parallel()
	a := start(t, agentOpts{})
	a.waitStatus("idle", "")
	sid1 := a.sid()
	a.send("run todos\r")
	a.waitEvent("Stop", 1)
	if n := count(a.events(), "TaskCreated"); n != 3 || count(a.events(), "TaskCompleted") != 1 {
		t.Fatalf("task events %v", a.events())
	}
	t1, err := readTask(taskDir(a.home, sid1), "1")
	if err != nil || t1.Status != "completed" {
		t.Fatalf("task 1 %+v %v", t1, err)
	}
	a.send("/clear\r")
	a.waitEvent("SessionStart", 2)
	h := a.hooks()
	end, st := h[len(h)-2], h[len(h)-1]
	if end["hook_event_name"] != "SessionEnd" || end["reason"] != "clear" || end["session_id"] != sid1 {
		t.Fatalf("SessionEnd %v", end)
	}
	sid2, _ := st["session_id"].(string)
	if st["source"] != "clear" || sid2 == sid1 || sid2 == "" {
		t.Fatalf("SessionStart %v", st)
	}
	waitUntil(t, "session file sid", func() bool { return a.sid() == sid2 })
	a.send("run todos\r")
	a.waitEvent("Stop", 2)
	for _, r := range a.hooks() {
		if r["hook_event_name"] == "TaskCreated" && r["session_id"] == sid2 && r["task_subject"] == "alpha" && r["task_id"] != "1" {
			t.Fatalf("ids must restart: %v", r)
		}
	}
	if _, err := readTask(taskDir(a.home, sid2), "3"); err != nil {
		t.Fatal(err)
	}
	a.send("/compact\r")
	a.waitEvent("SessionStart", 3)
	h = a.hooks()
	if h[len(h)-2]["hook_event_name"] != "PreCompact" || h[len(h)-1]["source"] != "compact" || h[len(h)-1]["session_id"] != sid2 {
		t.Fatalf("compact %v %v", h[len(h)-2], h[len(h)-1])
	}
}

func TestSocketQueuesPrompt(t *testing.T) {
	t.Parallel()
	scripts := t.TempDir()
	if err := os.WriteFile(filepath.Join(scripts, "slow.toml"), []byte("[[step]]\ndo = \"sleep\"\nms = 800\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	a := start(t, agentOpts{env: []string{"FAKEAGENT_SCRIPTS=" + scripts}})
	a.waitStatus("idle", "")
	sock, _ := a.session()["messagingSocketPath"].(string)
	if fi, err := os.Stat(sock); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("socket %s: %v", sock, err)
	}
	m := a.mark()
	a.send("run slow\r")
	a.waitEvent("UserPromptSubmit", 1)
	c, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	fmt.Fprintln(c, `{"type":"auth","token":"x"}`)
	fmt.Fprintln(c, `{"type":"user","message":{"role":"user","content":"from socket"}}`)
	c.Close()
	a.waitScreen(m, "Press up to edit queued messages")
	a.waitEvent("Stop", 2)
	ev := a.events()
	if strings.Join(ev[1:], ",") != "UserPromptSubmit,Stop,UserPromptSubmit,Stop" {
		t.Fatalf("events %v", ev)
	}
	if a.hooks()[3]["prompt"] != "from socket" {
		t.Fatalf("socket prompt %v", a.hooks()[3])
	}
	if p := a.logKinds("prompt"); len(p) != 2 || p[1]["via"] != "socket" || p[0]["via"] != "typed" {
		t.Fatalf("prompt log %v", p)
	}
}

func TestTrustDialog(t *testing.T) {
	t.Parallel()
	a := start(t, agentOpts{untrusted: true, args: []string{"--", "hi"}})
	a.waitScreen(0, "Yes, I trust this folder")
	a.waitScreen(0, "❯ No, exit")
	a.waitScreen(0, "Enter to confirm · Esc to cancel")
	a.send("2") // within the debounce: dropped
	time.Sleep(100 * time.Millisecond)
	if a.session() != nil {
		t.Fatal("no session file before trust")
	}
	time.Sleep(300 * time.Millisecond)
	if len(a.hooks()) != 0 {
		t.Fatalf("hooks before trust: %v", a.events())
	}
	a.sendUntil("\x1b[B\r", func() bool { return count(a.events(), "SessionStart") > 0 })
	a.waitEvent("Stop", 1)
	if ev := a.events(); ev[0] != "SessionStart" || ev[1] != "UserPromptSubmit" {
		t.Fatalf("events %v", ev)
	}
	if !isTrusted(a.home, a.cwd) {
		t.Fatal("trust not recorded")
	}
}

func TestTrustRefused(t *testing.T) {
	t.Parallel()
	a := start(t, agentOpts{untrusted: true})
	a.waitScreen(0, "Yes, I trust this folder")
	time.Sleep(350 * time.Millisecond)
	a.send("\r") // default: No, exit
	if code := a.waitExit(); code != 1 {
		t.Fatalf("exit %d", code)
	}
	if len(a.hooks()) != 0 {
		t.Fatalf("hooks %v", a.events())
	}
}

func TestBypassAndYolo(t *testing.T) {
	t.Parallel()
	a := start(t, agentOpts{noBypassOK: true, args: []string{"--dangerously-skip-permissions"}})
	a.waitScreen(0, "Bypass Permissions")
	a.waitScreen(0, "Yes, I accept")
	// The session file says idle before this dialog shows; SessionStart
	// is what says the dialog was answered.
	a.sendUntil("2", func() bool { return count(a.events(), "SessionStart") > 0 })
	a.send("run permission\r")
	a.waitEvent("Stop", 1)
	ev := a.events()
	if count(ev, "PermissionRequest") != 0 || count(ev, "PostToolUse") != 1 {
		t.Fatalf("yolo events %v", ev)
	}
	if a.hooks()[0]["permission_mode"] != "bypassPermissions" || !bypassAccepted(a.home) {
		t.Fatal("bypass not recorded")
	}
}

func TestQuestion(t *testing.T) {
	t.Parallel()
	a := start(t, agentOpts{})
	a.waitStatus("idle", "")
	m := a.mark()
	a.send("run question\r")
	a.waitScreen(m, "☐ Color")
	a.waitScreen(m, "Enter to select · ↑/↓ to navigate · Esc to cancel")
	a.waitStatus("waiting", "input needed")
	a.send("\x1b[B\r")
	a.waitEvent("Stop", 1)
	var post rec
	for _, h := range a.hooks() {
		if h["hook_event_name"] == "PostToolUse" {
			post = h
		}
	}
	ans := post["tool_response"].(map[string]any)["answers"].(map[string]any)
	if ans["Do you prefer red or blue?"] != "Blue" {
		t.Fatalf("answers %v", ans)
	}
}

func TestDenyWrite(t *testing.T) {
	t.Parallel()
	home, cwd, plugin := newEnv(t)
	ro := filepath.Join(cwd, "ro")
	if err := os.Mkdir(ro, 0o755); err != nil {
		t.Fatal(err)
	}
	settings := filepath.Join(home, "settings.json")
	s := fmt.Sprintf(`{"permissions":{"allow":[],"deny":[%q]}}`, "Edit(/"+ro+"/**)")
	if err := os.WriteFile(settings, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
	a := startIn(t, home, cwd, plugin, agentOpts{settings: settings, env: []string{"FAKEAGENT_DENY_PATH=" + ro}})
	a.waitStatus("idle", "")
	m := a.mark()
	a.send("run deny-write\r")
	a.waitEvent("Stop", 1)
	a.waitScreen(m, "Error: File is in a directory that is denied by your permission settings.")
	if ev := a.events(); strings.Join(ev[1:], ",") != "UserPromptSubmit,PreToolUse,PostToolUseFailure,Stop" {
		t.Fatalf("events %v", ev)
	}
	if _, err := os.Stat(filepath.Join(ro, "denied.txt")); err == nil {
		t.Fatal("file written")
	}
	if w := a.logKinds("write"); len(w) != 1 || w[0]["allowed"] != false {
		t.Fatalf("write log %v", w)
	}
}

func TestSubagentKeepsBusy(t *testing.T) {
	t.Parallel()
	a := start(t, agentOpts{})
	a.waitStatus("idle", "")
	a.send("run subagent\r")
	a.waitEvent("Stop", 1)
	if s := a.session(); s["status"] != "busy" {
		t.Fatalf("status after first Stop %v", s)
	}
	a.waitEvent("Stop", 2)
	a.waitStatus("idle", "")
	ev := a.events()
	want := "SessionStart,UserPromptSubmit,PreToolUse,PostToolUse,SubagentStart,Stop,PreToolUse,PostToolUse,SubagentStop,UserPromptSubmit,Stop"
	if strings.Join(ev, ",") != want {
		t.Fatalf("events %v", ev)
	}
	h := a.hooks()
	if h[6]["agent_type"] != "general-purpose" || h[6]["agent_id"] != h[4]["agent_id"] {
		t.Fatalf("subagent tool %v", h[6])
	}
	if p, _ := h[9]["prompt"].(string); !strings.HasPrefix(p, "<task-notification>") {
		t.Fatalf("notification prompt %q", p)
	}
}

func TestSessionIDReuseAndResume(t *testing.T) {
	t.Parallel()
	home, cwd, _ := newEnv(t)
	sid := newUUID()
	if err := ensureTranscript(transcriptPath(home, cwd, sid)); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) (string, int) {
		cmd := exec.Command(binPath, args...)
		cmd.Dir = cwd
		cmd.Env = append(os.Environ(), "HOME="+home)
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		err := cmd.Run()
		code := 0
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
		}
		return stderr.String(), code
	}
	if msg, code := run("--session-id", sid); code != 1 || !strings.Contains(msg, "Error: Session ID "+sid+" is already in use.") {
		t.Fatalf("reuse: %d %q", code, msg)
	}
	if msg, code := run("--resume", "nope"); code != 1 || !strings.Contains(msg, "No conversation found with session ID: nope") {
		t.Fatalf("resume missing: %d %q", code, msg)
	}
	if out, err := exec.Command(binPath, "--version").Output(); err != nil || string(out) != "2.1.289 (Claude Code)\n" {
		t.Fatalf("version %q %v", out, err)
	}

	// --resume "" opens the picker and waits.
	cmd := exec.Command(binPath, "--resume", "")
	cmd.Dir = cwd
	cmd.Env = append(os.Environ(), "HOME="+home)
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		t.Fatalf("picker exited: %v", err)
	case <-time.After(300 * time.Millisecond):
	}
	cmd.Process.Kill()
	<-done
	if !strings.Contains(out.String(), "fake picker") {
		t.Fatalf("picker output %q", out.String())
	}
}

func TestResumeKeepsID(t *testing.T) {
	t.Parallel()
	home, cwd, plugin := newEnv(t)
	sid := newUUID()
	if err := ensureTranscript(transcriptPath(home, cwd, sid)); err != nil {
		t.Fatal(err)
	}
	a := startIn(t, home, cwd, plugin, agentOpts{args: []string{"--resume", sid}})
	a.waitEvent("SessionStart", 1)
	if h := a.hooks()[0]; h["source"] != "resume" || h["session_id"] != sid {
		t.Fatalf("resume %v", h)
	}
	// What the kernel sends when the PTY goes away. (Closing our master
	// here would not close it while the reader goroutine blocks on it.)
	if err := a.cmd.Process.Signal(syscall.SIGHUP); err != nil {
		t.Fatal(err)
	}
	a.waitExit()
	if h := a.hooks(); h[len(h)-1]["reason"] != "other" {
		t.Fatalf("end %v", h[len(h)-1])
	}
}
