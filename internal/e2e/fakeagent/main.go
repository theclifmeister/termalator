//go:build unix

// Command fakeagent is a scripted stand-in for Claude Code 2.1.289, as the
// t-0004 spike recorded it (docs/research/claude.md, docs/SPEC.md §16.3).
// Tests run it under the real manifests/claude.toml with only
// launch.command pointed here, so it takes the same flags and has the same
// side effects: plugin hooks, the session status file, the transcript, the
// task files, the trust file, the messaging socket and the screens.
//
//	fakeagent [flags] [-- PROMPT]
//
// A prompt "run NAME" runs the script NAME.toml (from $FAKEAGENT_SCRIPTS or
// the embedded scripts/); any other prompt answers "ok: PROMPT". Every
// observable action is logged as JSONL to $FAKEAGENT_LOG.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
	"time"
)

// app is the whole agent. mu guards the screen and session state; hookMu
// keeps hook runs in order across goroutines.
type app struct {
	opts      options
	version   string
	home      string
	cwd       string
	logicalWD string
	pid       int
	logPath   string
	sockPath  string
	hooks     []hookCmd
	deny      []string
	born      time.Time
	startedAt int64
	ctx       context.Context

	hookMu sync.Mutex
	logMu  sync.Mutex
	outMu  sync.Mutex

	mu          sync.Mutex
	sid         string
	hooksOn     bool // set once startup screens are done
	fileOn      bool // set once the workspace is trusted: the session file exists
	started     bool // startup done: the worker may run jobs
	exiting     bool
	conv        []*convLine
	input       []rune
	pasted      bool
	suggestion  string
	dialog      *dialog
	awaitKey    chan struct{}
	queue       []job
	running     *job
	cancel      context.CancelFunc
	inTool      bool
	spinning    bool
	spinLabel   string
	turnStart   time.Time
	bg          int
	alwaysAllow map[string]bool
	notify      *step
	lastText    string
	remote      bool        // Remote Control is on (--remote-control, /remote-control)
	cx          *codexState // the Codex flavour (codex.go); nil as Claude

	written       sessionState // what the session file holds
	real          sessionState // the last computed state
	overridden    bool
	statusSince   int64
	restoreTerm   func()
	redraw        chan struct{}
	wake          chan struct{}
	exitOnce      sync.Once
	restoreOnce   sync.Once
	listener      net.Listener
	trustDebounce time.Duration
}

// sessionState is what the session file says.
type sessionState struct {
	status, waitingFor, sid string
	remote                  bool
}

func main() {
	if isCodex() {
		codexMain()
		return
	}
	opts := parseArgs(os.Args[1:])
	version := os.Getenv("FAKEAGENT_VERSION")
	if version == "" {
		version = "2.1.289"
	}
	if opts.version {
		fmt.Printf("%s (Claude Code)\n", version)
		return
	}
	home := os.Getenv("HOME")
	if home == "" {
		home, _ = os.UserHomeDir()
	}
	wd, err := os.Getwd()
	if err != nil {
		fmt.Fprintln(os.Stderr, "Error:", err)
		os.Exit(1)
	}
	a := &app{
		opts:        opts,
		remote:      opts.remote != nil,
		version:     version,
		home:        home,
		logicalWD:   wd,
		cwd:         realPath(wd),
		pid:         os.Getpid(),
		born:        time.Now(),
		startedAt:   nowMS(),
		ctx:         context.Background(),
		alwaysAllow: map[string]bool{},
		redraw:      make(chan struct{}, 1),
		wake:        make(chan struct{}, 1),
		restoreTerm: func() {},
	}
	a.logPath = os.Getenv("FAKEAGENT_LOG")
	if a.logPath == "" {
		a.logPath = filepath.Join(home, ".fakeagent", "log.jsonl")
	}
	a.trustDebounce = 500 * time.Millisecond
	if v, err := strconv.Atoi(os.Getenv("FAKEAGENT_TRUST_DEBOUNCE_MS")); err == nil && v >= 0 {
		a.trustDebounce = time.Duration(v) * time.Millisecond
	}

	switch {
	case opts.resume != nil && *opts.resume == "":
		a.picker()
	case opts.resume != nil:
		a.sid = *opts.resume
		if _, err := os.Stat(a.transcript()); err != nil {
			fmt.Fprintf(os.Stderr, "No conversation found with session ID: %s\n", a.sid)
			os.Exit(1)
		}
	case opts.sessionID != "":
		a.sid = opts.sessionID
		if _, err := os.Stat(a.transcript()); err == nil {
			fmt.Fprintf(os.Stderr, "Error: Session ID %s is already in use.\n", a.sid)
			os.Exit(1)
		}
	default:
		a.sid = newUUID()
	}
	if err := ensureTranscript(a.transcript()); err != nil {
		fmt.Fprintln(os.Stderr, "Error:", err)
		os.Exit(1)
	}
	a.loadConfig()
	a.logStart()
	a.listen()
	a.mu.Lock()
	a.syncSessionLocked()
	a.mu.Unlock()

	a.restoreTerm = setupTerm()
	go a.signals()
	go a.renderLoop()
	go a.inputLoop()
	a.requestRedraw()

	a.startup()
	a.worker()
}

// loadConfig reads hooks from every --plugin-dir and --settings, and the
// settings' deny rules.
func (a *app) loadConfig() {
	var denyRules []string
	for _, dir := range a.opts.pluginDirs {
		groups, err := pluginHooks(dir)
		if err != nil {
			a.log("error", map[string]any{"text": err.Error()})
			continue
		}
		a.hooks = append(a.hooks, compileHooks(groups, eventNames(groups))...)
	}
	for _, s := range a.opts.settings {
		st, err := readSettings(s)
		if err != nil {
			a.log("error", map[string]any{"text": err.Error()})
			continue
		}
		denyRules = append(denyRules, st.Permissions.Deny...)
		a.hooks = append(a.hooks, compileHooks(st.Hooks, eventNames(st.Hooks))...)
	}
	a.deny = denyDirs(denyRules, a.home, a.cwd)
}

func (a *app) logStart() {
	f := map[string]any{
		"argv":       os.Args,
		"pid":        a.pid,
		"session_id": a.sid,
		"cwd":        a.cwd,
		"version":    a.version,
		"model":      a.opts.model,
		"yolo":       a.opts.yolo,
		"brief_path": a.opts.briefPath,
		"brief_size": -1,
	}
	if a.opts.resume != nil {
		f["resume"] = *a.opts.resume
	}
	if a.opts.briefPath != "" {
		if b, err := os.ReadFile(a.opts.briefPath); err == nil {
			f["brief_size"] = len(b)
		} else {
			f["brief_error"] = err.Error()
		}
	}
	a.log("start", f)
}

// picker is what `--resume` without an id does: an interactive chooser.
// Tests assert it never happens, so it only says so and waits.
func (a *app) picker() {
	a.log("start", map[string]any{"argv": os.Args, "pid": a.pid, "resume": ""})
	a.log("picker", map[string]any{})
	fmt.Println("fake picker")
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGHUP, syscall.SIGTERM, syscall.SIGINT)
	<-ch
	os.Exit(0)
}

// startup shows the trust and bypass screens when needed, then starts
// the session (SessionStart) and queues the kickoff.
func (a *app) startup() {
	if !isTrusted(a.home, a.cwd, a.logicalWD) {
		d := &dialog{kind: "trust", subject: a.cwd, options: []string{"No, exit", "Yes, I trust this folder"}, debounce: a.trustDebounce}
		if n, _ := a.waitDialog(context.Background(), d); n != 2 {
			a.exit(1, "")
		}
		_ = acceptTrust(a.home, a.cwd)
	}
	a.mu.Lock()
	a.fileOn = true
	a.syncSessionLocked()
	a.mu.Unlock()
	if a.opts.yolo && !bypassAccepted(a.home) {
		d := &dialog{kind: "bypass", options: []string{"No, exit", "Yes, I accept"}, debounce: a.trustDebounce}
		if n, _ := a.waitDialog(context.Background(), d); n != 2 {
			a.exit(1, "")
		}
		_ = acceptBypass(a.home)
	}
	a.mu.Lock()
	a.hooksOn = true
	a.mu.Unlock()
	source := "startup"
	if a.opts.resume != nil {
		source = "resume"
	}
	a.sessionStart(source)
	a.mu.Lock()
	if a.opts.prompt != "" {
		a.queue = append([]job{{kind: "prompt", text: a.opts.prompt, via: "kickoff"}}, a.queue...)
	}
	a.started = true
	a.syncSessionLocked()
	a.mu.Unlock()
	a.poke()
	a.requestRedraw()
}

// signals handles SIGHUP/SIGTERM (session end) and SIGWINCH (redraw).
func (a *app) signals() {
	ch := make(chan os.Signal, 4)
	signal.Notify(ch, syscall.SIGHUP, syscall.SIGTERM, syscall.SIGWINCH, syscall.SIGINT)
	for s := range ch {
		switch s {
		case syscall.SIGWINCH:
			a.requestRedraw()
		case syscall.SIGINT:
			// Raw mode delivers Ctrl+C as a key; ignore a stray signal.
		default:
			a.exit(0, "other")
		}
	}
}

// exit ends the process. A non-empty reason fires SessionEnd first
// (only once hooks are on).
func (a *app) exit(code int, reason string) {
	a.exitOnce.Do(func() {
		if reason != "" {
			_, _ = a.fireHook(context.Background(), "SessionEnd", map[string]any{"reason": reason})
		}
		a.log("exit", map[string]any{"code": code, "reason": reason})
		os.Remove(a.sessionFile())
		if a.listener != nil {
			a.listener.Close()
			os.Remove(a.sockPath)
		}
		a.leaveScreen()
		os.Exit(code)
	})
	select {}
}

// crash exits at once: no hooks, the session file stays behind.
func (a *app) crash() {
	a.log("exit", map[string]any{"code": 1, "reason": "crash"})
	a.leaveScreen()
	os.Exit(1)
}

func (a *app) leaveScreen() {
	a.outMu.Lock()
	a.exiting = true
	a.restoreOnce.Do(a.restoreTerm)
	a.outMu.Unlock()
}

// transcript is the current session's transcript path.
func (a *app) transcript() string { return transcriptPath(a.home, a.cwd, a.sid) }

// sessionFile is ~/.claude/sessions/<pid>.json.
func (a *app) sessionFile() string {
	return filepath.Join(a.home, ".claude", "sessions", strconv.Itoa(a.pid)+".json")
}

// statusLocked is the session status the agent is really in.
func (a *app) statusLocked() string {
	st, _ := a.realStatusLocked()
	return st
}

func (a *app) realStatusLocked() (string, string) {
	if d := a.dialog; d != nil {
		switch d.kind {
		case "permission":
			return "waiting", "permission prompt"
		case "question":
			return "waiting", "input needed"
		}
	}
	if a.running != nil || a.bg > 0 || (a.started && len(a.queue) > 0) {
		return "busy", ""
	}
	return "idle", ""
}

// syncSessionLocked rewrites the session file when the state changed.
// A status override (the status step) holds until the next real change.
func (a *app) syncSessionLocked() {
	st, wf := a.realStatusLocked()
	now := sessionState{status: st, waitingFor: wf, sid: a.sid, remote: a.remote}
	if a.overridden && now == a.real {
		return
	}
	a.real = now
	a.overridden = false
	a.writeSessionLocked(now)
}

// overrideStatusLocked writes a status that is not the real one.
func (a *app) overrideStatusLocked(status, waitingFor string) {
	a.overridden = true
	a.writeSessionLocked(sessionState{status: status, waitingFor: waitingFor, sid: a.sid, remote: a.remote})
}

func (a *app) writeSessionLocked(s sessionState) {
	if !a.fileOn {
		return // like Claude: no session file until the workspace is trusted
	}
	if s == a.written && a.statusSince != 0 {
		return
	}
	if s.status != a.written.status || s.waitingFor != a.written.waitingFor || a.statusSince == 0 {
		a.statusSince = nowMS()
	}
	a.written = s
	m := map[string]any{
		"pid":                 a.pid,
		"sessionId":           s.sid,
		"cwd":                 a.cwd,
		"startedAt":           a.startedAt,
		"status":              s.status,
		"version":             a.version,
		"messagingSocketPath": a.sockPath,
		"statusUpdatedAt":     a.statusSince,
		"name":                "fake",
	}
	if s.status == "waiting" && s.waitingFor != "" {
		m["waitingFor"] = s.waitingFor
	}
	m["bridgeSessionId"] = nil // as 2.1.289: null while Remote Control is off
	if s.remote {
		m["bridgeSessionId"] = "session_fake"
	}
	b, _ := json.Marshal(m)
	_ = writeAtomic(a.sessionFile(), b, 0o600)
}

// log appends one record to the test log.
func (a *app) log(kind string, fields map[string]any) {
	rec := map[string]any{"t": time.Now().Format(time.RFC3339Nano), "kind": kind}
	for k, v := range fields {
		rec[k] = v
	}
	a.logMu.Lock()
	defer a.logMu.Unlock()
	_ = appendJSONL(a.logPath, rec)
}

// transcriptAppend adds an entry with the common fields.
func (a *app) transcriptAppend(entry map[string]any) {
	if a.cx != nil {
		return // Codex writes its rollout (codex.go)
	}
	a.mu.Lock()
	entry["sessionId"] = a.sid
	path := a.transcript()
	a.mu.Unlock()
	entry["timestamp"] = stamp()
	entry["uuid"] = newUUID()
	entry["cwd"] = a.cwd
	entry["version"] = a.version
	_ = appendJSONL(path, entry)
}
