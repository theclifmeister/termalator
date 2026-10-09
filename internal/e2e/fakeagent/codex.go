package main

// The Codex flavour: the fake as Codex CLI 0.160 (T91, T98-T104),
// chosen when it runs as "codex" (a symlink) or FAKEAGENT_FLAVOR=codex.
// Tests run it under the real manifests/codex.toml and its Go agent with
// only launch.command swapped, so it takes Codex's argv: the `resume`
// and `queue` subcommands, -c overrides (the hooks, their trust hashes,
// the trusted folder), -m and the approval flags. It keeps no status
// file and no socket: state comes from the hooks, the rollout and the
// screens, which are drawn in Codex's shape (the fixtures in
// internal/agent/testdata/codex).
//
// What it does as Codex does:
//   - hooks come only from -c hooks={…}; one whose hash is missing from
//     hooks.state, or wrong, is untrusted: "Hooks need review" shows, and
//     "Continue without trusting" runs only the trusted ones;
//   - SessionStart fires at the first prompt (source startup or resume),
//     and at the next prompt after /clear (clear) or /compact (compact);
//   - /clear fires no SessionEnd; the thread it left is unloaded later
//     (a minute live, FAKEAGENT_CODEX_UNLOAD_MS here, 1 s by default),
//     with SessionEnd reason "other" and that thread's id;
//   - each turn writes task_started, token_count and task_complete to
//     the rollout; Esc (or an approval's "No") fires Interrupt and writes
//     turn_aborted;
//   - `codex queue --thread=<id> --message=<text>` queues for a thread
//     that has a rollout; the TUI runs a message for its own thread as a
//     prompt, and one for a thread /clear left out of sight;
//   - a plan-mode question fires no hook.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/BurntSushi/toml"
)

const (
	codexVersion     = "0.160.0"
	codexModel       = "gpt-6-luna"
	codexPlaceholder = "Ask Codex to do anything"
	// codexHookSource names hooks from -c flags in hooks.state keys.
	codexHookSource = "/<session-flags>/config.toml"
	codexContext    = 258400
)

// hookSourceIn is codexHookSource as Codex names it in a session in cwd:
// on Windows made absolute on cwd's drive (C:\<session-flags>\config.toml,
// Codex 0.162).
func hookSourceIn(cwd string) string {
	if filepath.Separator == '/' {
		return codexHookSource
	}
	vol := filepath.VolumeName(cwd)
	if vol == "" {
		vol = "C:"
	}
	return vol + filepath.FromSlash(codexHookSource)
}

// isCodex reports whether the fake runs as Codex.
func isCodex() bool {
	return strings.TrimSuffix(filepath.Base(os.Args[0]), ".exe") == "codex" || os.Getenv("FAKEAGENT_FLAVOR") == "codex"
}

// codexOpts are Codex's command line.
type codexOpts struct {
	version      bool
	queue        bool
	thread       string
	message      string
	resume       *string
	config       []string // -c values, in order
	model        string
	yolo         bool
	approveForMe bool
	approval     string // -a: the fake asks in every policy but yolo and --approve-for-me
	prompt       string
}

// parseCodexArgs reads argv (without the program name). Empty
// arguments (a template that rendered nothing) are skipped.
func parseCodexArgs(args []string) codexOpts {
	var o codexOpts
	var pos []string
	value := func(i *int, arg, name string) (string, bool) {
		if v, ok := strings.CutPrefix(arg, name+"="); ok {
			return v, true
		}
		if arg == name && *i+1 < len(args) {
			*i++
			return args[*i], true
		}
		return "", false
	}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "" {
			continue
		}
		if arg == "--" {
			for _, p := range args[i+1:] {
				if p != "" {
					pos = append(pos, p)
				}
			}
			break
		}
		if v, ok := value(&i, arg, "-c"); ok {
			o.config = append(o.config, v)
			continue
		}
		if v, ok := value(&i, arg, "--config"); ok {
			o.config = append(o.config, v)
			continue
		}
		if v, ok := value(&i, arg, "-a"); ok {
			o.approval = v
			continue
		}
		if v, ok := value(&i, arg, "--ask-for-approval"); ok {
			o.approval = v
			continue
		}
		if v, ok := value(&i, arg, "-m"); ok {
			o.model = v
			continue
		}
		if v, ok := value(&i, arg, "--model"); ok {
			o.model = v
			continue
		}
		if o.queue {
			if v, ok := value(&i, arg, "--thread"); ok {
				o.thread = v
				continue
			}
			if v, ok := value(&i, arg, "--message"); ok {
				o.message = v
				continue
			}
		}
		switch arg {
		case "--version", "-V":
			o.version = true
			continue
		case "--dangerously-bypass-approvals-and-sandbox":
			o.yolo = true
			continue
		case "--approve-for-me":
			o.approveForMe = true
			continue
		}
		if strings.HasPrefix(arg, "-") {
			continue
		}
		switch {
		case len(pos) == 0 && o.resume == nil && !o.queue && arg == "resume":
			id := ""
			o.resume = &id
		case o.resume != nil && *o.resume == "" && len(pos) == 0:
			*o.resume = arg
		case len(pos) == 0 && o.resume == nil && !o.queue && arg == "queue":
			o.queue = true
		default:
			pos = append(pos, arg)
		}
	}
	o.prompt = strings.Join(pos, " ")
	return o
}

// codexConfig merges the -c overrides into one table. A value that
// isn't TOML is an error: tm only passes TOML.
func codexConfig(values []string) (map[string]any, error) {
	cfg := map[string]any{}
	for _, v := range values {
		m := map[string]any{}
		if _, err := toml.Decode(v, &m); err != nil {
			return nil, fmt.Errorf("-c %s: %w", v, err)
		}
		merge(cfg, m)
	}
	return cfg, nil
}

func merge(dst, src map[string]any) {
	for k, v := range src {
		if sm, ok := v.(map[string]any); ok {
			if dm, ok := dst[k].(map[string]any); ok {
				merge(dm, sm)
				continue
			}
		}
		dst[k] = v
	}
}

// codexState is what only the Codex flavour has.
type codexState struct {
	opts         codexOpts
	cfg          map[string]any
	model        string
	codexHome    string
	rollout      string   // the thread's rollout, once it has one
	pendingStart string   // SessionStart's source at the next prompt
	left         []string // threads a /clear left
	untrusted    int      // hooks that need review
}

// codexMain runs the fake as Codex.
func codexMain() {
	o := parseCodexArgs(os.Args[1:])
	if o.version {
		fmt.Printf("codex-cli %s\n", codexVersion)
		return
	}
	home := os.Getenv("HOME")
	if home == "" {
		home, _ = os.UserHomeDir()
	}
	cx := &codexState{opts: o, model: o.model, codexHome: os.Getenv("CODEX_HOME")}
	if cx.codexHome == "" {
		cx.codexHome = filepath.Join(home, ".codex")
	}
	if cx.model == "" {
		cx.model = codexModel
	}
	logPath := os.Getenv("FAKEAGENT_LOG")
	if logPath == "" {
		logPath = filepath.Join(home, ".fakeagent", "log.jsonl")
	}
	if o.queue {
		os.Exit(cx.queueCmd(logPath))
	}
	if len(os.Args) > 1 && os.Args[1] == "sandbox" {
		os.Exit(sandboxCmd(logPath, os.Args[2:]))
	}
	cfg, err := codexConfig(o.config)
	if err != nil {
		fmt.Fprintln(os.Stderr, "Error:", err)
		os.Exit(1)
	}
	cx.cfg = cfg
	wd, err := os.Getwd()
	if err != nil {
		fmt.Fprintln(os.Stderr, "Error:", err)
		os.Exit(1)
	}
	a := &app{
		opts:        options{yolo: o.yolo, model: cx.model, prompt: o.prompt},
		cx:          cx,
		version:     codexVersion,
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
		logPath:     logPath,
	}
	a.trustDebounce = 500 * time.Millisecond
	if v, err := strconv.Atoi(os.Getenv("FAKEAGENT_TRUST_DEBOUNCE_MS")); err == nil && v >= 0 {
		a.trustDebounce = time.Duration(v) * time.Millisecond
	}
	if o.resume != nil {
		if *o.resume == "" {
			a.picker()
		}
		a.sid = *o.resume
		cx.rollout = findRollout(cx.codexHome, a.sid)
		if cx.rollout == "" {
			fmt.Fprintf(os.Stderr, "ERROR: No saved session found with ID %s\n", a.sid)
			os.Exit(1)
		}
	} else {
		a.sid = newUUID()
	}
	a.hooks, cx.untrusted = codexHooks(cfg, false, hookSourceIn(a.cwd))
	if n, err := strconv.Atoi(os.Getenv("FAKEAGENT_UNTRUSTED_HOOKS")); err == nil {
		cx.untrusted += n // the user's own, never trusted for the session
	}
	a.codexLogStart()

	a.restoreTerm = setupTerm()
	go a.signals()
	go a.renderLoop()
	go a.inputLoop()
	go a.pollQueue()
	a.requestRedraw()

	a.codexStartup()
	a.worker()
}

func (a *app) codexLogStart() {
	cx := a.cx
	keys := make([]string, 0, len(cx.cfg))
	for k := range cx.cfg {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	f := map[string]any{
		"argv": os.Args, "pid": a.pid, "session_id": a.sid, "cwd": a.cwd, "version": a.version,
		"model": cx.model, "yolo": cx.opts.yolo, "approve_for_me": cx.opts.approveForMe,
		"config": keys, "hooks": len(a.hooks), "untrusted_hooks": cx.untrusted, "brief_size": -1,
	}
	if di, ok := cx.cfg["developer_instructions"].(string); ok {
		f["brief_size"] = len(di)
	}
	if p, ok := cx.cfg["default_permissions"].(string); ok {
		f["permissions"] = p
	}
	if cx.opts.resume != nil {
		f["resume"] = *cx.opts.resume
	}
	a.log("start", f)
}

// codexHooks compiles -c hooks={…}: every command handler hooks.state
// trusts (all of them with trustAll), and how many it doesn't.
// source names the flags in hooks.state keys (hookSourceIn).
func codexHooks(cfg map[string]any, trustAll bool, source string) ([]hookCmd, int) {
	hooks, _ := cfg["hooks"].(map[string]any)
	state, _ := hooks["state"].(map[string]any)
	var events []string
	for ev := range hooks {
		if ev != "state" {
			events = append(events, ev)
		}
	}
	sort.Strings(events)
	var out []hookCmd
	untrusted := 0
	for _, ev := range events {
		for gi, g := range tables(hooks[ev]) {
			for hi, h := range tables(g["hooks"]) {
				command, _ := h["command"].(string)
				timeout := 600
				if t, ok := h["timeout"].(int64); ok {
					timeout = int(t)
				}
				key := fmt.Sprintf("%s:%s:%d:%d", source, snakeCase(ev), gi, hi)
				entry, _ := state[key].(map[string]any)
				if h["type"] != "command" || command == "" {
					continue
				}
				if !trustAll && entry["trusted_hash"] != codexTrustHash(snakeCase(ev), command, timeout) {
					untrusted++
					continue
				}
				out = append(out, hookCmd{event: ev, command: command, timeout: time.Duration(timeout) * time.Second})
			}
		}
	}
	return out, untrusted
}

// tables reads a TOML array of tables, inline or not.
func tables(v any) []map[string]any {
	switch v := v.(type) {
	case []map[string]any:
		return v
	case []any:
		var out []map[string]any
		for _, e := range v {
			if m, ok := e.(map[string]any); ok {
				out = append(out, m)
			}
		}
		return out
	}
	return nil
}

// codexTrustHash is Codex 0.160's trust hash of a hook group with one
// command handler and no matcher: the SHA-256 of its JSON, keys sorted,
// no spaces, no HTML escaping. Written apart from internal/agent/codex
// on purpose, so a slip there shows as "Hooks need review" here.
func codexTrustHash(event, command string, timeout int) string {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(map[string]any{"event_name": event, "hooks": []any{map[string]any{
		"async": false, "command": command, "timeout": timeout, "type": "command",
	}}})
	sum := sha256.Sum256(bytes.TrimSpace(b.Bytes()))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func snakeCase(s string) string {
	var b strings.Builder
	for i, r := range s {
		if unicode.IsUpper(r) {
			if i > 0 {
				b.WriteByte('_')
			}
			r = unicode.ToLower(r)
		}
		b.WriteRune(r)
	}
	return b.String()
}

// codexTrusted reports whether -c projects trusts the folder: cwd, an
// ancestor, or the main repo's root (Codex keys trust by it, even in a
// linked worktree), or the user accepted the dialog before.
func (a *app) codexTrusted() bool {
	dirs := []string{a.cwd, a.logicalWD}
	if out, err := exec.Command("git", "-C", a.cwd, "rev-parse", "--path-format=absolute", "--git-common-dir").Output(); err == nil {
		dirs = append(dirs, filepath.Dir(strings.TrimSpace(string(out))))
	}
	trusted := map[string]bool{}
	projects, _ := a.cx.cfg["projects"].(map[string]any)
	for k, v := range projects {
		if p, ok := v.(map[string]any); ok && p["trust_level"] == "trusted" {
			trusted[realPath(k)] = true
		}
	}
	if b, err := os.ReadFile(filepath.Join(a.cx.codexHome, "fake-trusted")); err == nil {
		for _, l := range strings.Split(string(b), "\n") {
			if l != "" {
				trusted[l] = true
			}
		}
	}
	for _, d := range dirs {
		for d := realPath(d); ; d = filepath.Dir(d) {
			if trusted[d] {
				return true
			}
			if d == filepath.Dir(d) {
				break
			}
		}
	}
	return false
}

// codexStartup shows the startup screens in Codex's order (folder
// trust, hooks review, update), then waits for the first prompt.
func (a *app) codexStartup() {
	cx := a.cx
	if !a.codexTrusted() {
		d := &dialog{kind: "cxtrust", subject: a.cwd, options: []string{"Trust and continue", "Back to Agent Command Center"}, debounce: a.trustDebounce}
		if n, _ := a.waitDialog(context.Background(), d); n != 1 {
			a.exit(1, "")
		}
		f, err := os.OpenFile(filepath.Join(cx.codexHome, "fake-trusted"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if err == nil {
			fmt.Fprintln(f, a.cwd)
			f.Close()
		}
	}
	if cx.untrusted > 0 {
		d := &dialog{kind: "hooksreview", subject: strconv.Itoa(cx.untrusted),
			options: []string{"Review hooks", "Trust all and continue", "Continue without trusting (hooks won't run)"}, debounce: a.trustDebounce}
		n, _ := a.waitDialog(context.Background(), d)
		a.log("hooks-review", map[string]any{"choice": n, "untrusted": cx.untrusted})
		if n == 2 {
			a.hooks, _ = codexHooks(cx.cfg, true, hookSourceIn(a.cwd))
		}
	}
	if os.Getenv("FAKEAGENT_CODEX_UPDATE") != "" && cx.cfg["check_for_update_on_startup"] != false {
		d := &dialog{kind: "update", options: []string{"Update now (runs `brew upgrade --cask codex`)", "Skip", "Skip until next version"}}
		n, _ := a.waitDialog(context.Background(), d)
		a.log("update", map[string]any{"choice": n})
	}
	a.mu.Lock()
	a.hooksOn = true
	cx.pendingStart = "startup"
	if cx.opts.resume != nil {
		cx.pendingStart = "resume"
	}
	if a.opts.prompt != "" {
		a.queue = append([]job{{kind: "prompt", text: a.opts.prompt, via: "kickoff"}}, a.queue...)
	}
	a.started = true
	a.mu.Unlock()
	a.poke()
	a.requestRedraw()
}

// codexBegin opens a turn: the thread's rollout on its first prompt,
// SessionStart when one is due, then task_started.
func (a *app) codexBegin(ctx context.Context) error {
	cx := a.cx
	a.mu.Lock()
	if cx.rollout == "" {
		now := time.Now()
		cx.rollout = filepath.Join(cx.codexHome, "sessions", now.Format("2006/01/02"),
			"rollout-"+now.Format("2006-01-02T15-04-05")+"-"+a.sid+".jsonl")
		a.mu.Unlock()
		a.rolloutAppend("session_meta", map[string]any{"id": a.sid, "cwd": a.cwd, "cli_version": codexVersion})
		a.mu.Lock()
	}
	source := cx.pendingStart
	cx.pendingStart = ""
	a.mu.Unlock()
	if source != "" {
		a.sessionStart(source)
	}
	a.rolloutAppend("event_msg", map[string]any{"type": "task_started", "model_context_window": codexContext})
	if ctx.Err() != nil {
		return errCancelled
	}
	return nil
}

// codexEnd closes a turn in the rollout: usage, then task_complete.
func (a *app) codexEnd(last string) {
	a.mu.Lock()
	n := len(a.conv)
	a.mu.Unlock()
	in := 12000 + 100*n
	use := map[string]any{"input_tokens": in, "cached_input_tokens": in / 2, "output_tokens": 200, "reasoning_output_tokens": 64, "total_tokens": in + 200}
	a.rolloutAppend("event_msg", map[string]any{"type": "token_count",
		"info":        map[string]any{"total_token_usage": use, "last_token_usage": use, "model_context_window": codexContext},
		"rate_limits": map[string]any{"primary": map[string]any{"used_percent": 7.0, "window_minutes": 300, "resets_in_seconds": 9000}}})
	a.rolloutAppend("event_msg", map[string]any{"type": "task_complete", "last_agent_message": last})
}

// codexCancelled is Esc on a turn: the Interrupt hook, turn_aborted.
func (a *app) codexCancelled() {
	_, _ = a.fireHook(context.Background(), "Interrupt", map[string]any{})
	a.rolloutAppend("event_msg", map[string]any{"type": "turn_aborted", "reason": "interrupted"})
}

// rolloutAppend writes one rollout line.
func (a *app) rolloutAppend(typ string, payload map[string]any) {
	a.mu.Lock()
	path := a.cx.rollout
	a.mu.Unlock()
	if path == "" {
		return
	}
	_ = appendJSONL(path, map[string]any{"timestamp": stamp(), "type": typ, "payload": payload})
}

// findRollout finds a thread's rollout under CODEX_HOME/sessions.
func findRollout(codexHome, id string) string {
	var found string
	_ = filepath.WalkDir(filepath.Join(codexHome, "sessions"), func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(p, "-"+id+".jsonl") {
			found = p
		}
		return nil
	})
	return found
}

func queueFile(codexHome, thread string) string {
	return filepath.Join(codexHome, "fake-queue", thread+".jsonl")
}

// queueCmd is `codex queue`: the message goes into the thread's durable
// queue, if the thread exists.
func (cx *codexState) queueCmd(logPath string) int {
	rec := map[string]any{"t": time.Now().Format(time.RFC3339Nano), "kind": "queue", "thread": cx.opts.thread, "text": cx.opts.message}
	defer func() { _ = appendJSONL(logPath, rec) }()
	if cx.opts.thread == "" || cx.opts.message == "" {
		rec["error"] = "usage"
		fmt.Fprintln(os.Stderr, "ERROR: --thread and --message are required")
		return 2
	}
	if findRollout(cx.codexHome, cx.opts.thread) == "" {
		rec["error"] = "no thread"
		fmt.Fprintf(os.Stderr, "WARNING: proceeding, even though we could not update PATH\nERROR: thread not found: %s\n", cx.opts.thread)
		return 1
	}
	if err := appendJSONL(queueFile(cx.codexHome, cx.opts.thread), map[string]any{"message": cx.opts.message}); err != nil {
		rec["error"] = err.Error()
		fmt.Fprintln(os.Stderr, "ERROR:", err)
		return 1
	}
	return 0
}

// pollQueue takes queued messages: the current thread's run as prompts
// (after the running turn), a left thread's run out of sight.
func (a *app) pollQueue() {
	for range time.Tick(100 * time.Millisecond) {
		// Taken under the lock, so /clear can't move a thread between
		// the ids read and its queue taken.
		a.mu.Lock()
		cur := takeQueue(a.cx.codexHome, a.sid)
		hidden := map[string][]string{}
		for _, t := range a.cx.left {
			hidden[t] = takeQueue(a.cx.codexHome, t)
		}
		a.mu.Unlock()
		for _, m := range cur {
			a.enqueue(job{kind: "prompt", text: m, via: "queue"})
		}
		for t, ms := range hidden {
			for _, m := range ms {
				a.log("queue-hidden", map[string]any{"thread": t, "text": m})
			}
		}
	}
}

// takeQueue reads and empties a thread's queue.
func takeQueue(codexHome, thread string) []string {
	p := queueFile(codexHome, thread)
	taking := p + fmt.Sprintf(".taking-%d", os.Getpid())
	if os.Rename(p, taking) != nil {
		return nil
	}
	b, _ := os.ReadFile(taking)
	os.Remove(taking)
	var out []string
	for _, l := range strings.Split(string(b), "\n") {
		var m struct{ Message string }
		if json.Unmarshal([]byte(l), &m) == nil && m.Message != "" {
			out = append(out, m.Message)
		}
	}
	return out
}

// codexClear is /clear: a new thread whose SessionStart (clear) waits
// for its first prompt. The thread left stays loaded, and is unloaded
// later with its own SessionEnd. "cleared" is logged once the thread
// left takes its queue out of sight.
func (a *app) codexClear() {
	a.mu.Lock()
	old, oldRollout := a.sid, a.cx.rollout
	a.cx.left = append(a.cx.left, a.sid)
	go a.codexUnload(old, oldRollout)
	a.sid = newUUID()
	a.cx.rollout = ""
	a.cx.pendingStart = "clear"
	a.conv = nil
	a.mu.Unlock()
	a.log("cleared", map[string]any{"left": old})
	a.requestRedraw()
}

// codexCompact is /compact, as 0.160 runs it (seen live, T105): a turn
// in the rollout (task_started, task_complete) with PreCompact and
// PostCompact, same thread, no Stop; SessionStart (compact) waits for
// the next prompt.
func (a *app) codexCompact() {
	a.mu.Lock()
	label, spin := a.spinLabel, a.spinning
	a.spinLabel, a.spinning = "Compacting", true
	a.mu.Unlock()
	a.requestRedraw()
	a.rolloutAppend("event_msg", map[string]any{"type": "task_started", "model_context_window": codexContext})
	defer a.rolloutAppend("event_msg", map[string]any{"type": "task_complete", "last_agent_message": nil})
	_, _ = a.fireHook(context.Background(), "PreCompact", map[string]any{"trigger": "manual"})
	_ = sleepCtx(context.Background(), 300*time.Millisecond)
	a.rolloutAppend("compacted", map[string]any{"message": "compacted"})
	_, _ = a.fireHook(context.Background(), "PostCompact", map[string]any{"trigger": "manual"})
	a.mu.Lock()
	a.cx.pendingStart = "compact"
	a.spinLabel, a.spinning = label, spin
	a.conv = append(a.conv, &convLine{prefix: "• ", text: "Context compacted"})
	a.mu.Unlock()
	a.requestRedraw()
}

// codexNew is /new: where to run, then as /clear.
func (a *app) codexNew() {
	d := &dialog{kind: "newdlg", options: []string{
		"Current checkout  Keep using the current working directory",
		"New worktree      Create an isolated managed checkout"}}
	if n, _ := a.waitDialog(context.Background(), d); n == 1 {
		a.codexClear()
	}
}

// codexPrefix maps the conversation's Claude prefixes to Codex's.
func codexPrefix(p string) string {
	switch p {
	case "⏺ ":
		return "• "
	case "> ":
		return "› "
	case "  ⎿  ":
		return "  └ "
	}
	return p
}

// codexFrameLocked is a whole screen in Codex's layout: the header, the
// conversation, the working line, the composer (or a dialog), the model
// line and the shortcuts hint. No window title.
func (a *app) codexFrameLocked(now time.Time) string {
	cols, rows := termSize()
	header := []string{"  >_ OpenAI Codex (v" + a.version + ")"}
	header = append(header, wrap("     "+a.cwd, cols)...)
	header = append(header, "")
	var bottom []string
	if d := a.dialog; d != nil {
		bottom = append(bottom, "")
		for _, l := range d.lines() {
			bottom = append(bottom, wrap(l, cols)...)
		}
	} else {
		if a.spinning {
			secs := int(now.Sub(a.turnStart) / time.Second)
			bottom = append(bottom, fmt.Sprintf("• %s (%ds • esc to interrupt)", cmp(a.spinLabel, "Working"), secs), "")
		} else {
			bottom = append(bottom, "")
		}
		if len(a.input) == 0 {
			bottom = append(bottom, "› "+dimOn+codexPlaceholder+dimOff)
		} else {
			for i, l := range strings.Split(string(a.input), "\n") {
				p := "  "
				if i == 0 {
					p = "› "
				}
				bottom = append(bottom, wrap(p+l, cols)...)
			}
		}
		bottom = append(bottom, "", "  "+a.cx.model+" medium · "+a.cwd, "  ? for shortcuts")
	}
	var conv []string
	if a.dialog == nil || (a.dialog.kind != "cxtrust" && a.dialog.kind != "hooksreview" && a.dialog.kind != "update") {
		for _, l := range a.conv {
			conv = append(conv, wrap(codexPrefix(l.prefix)+strings.TrimPrefix(l.visible(now), l.prefix), cols)...)
		}
	}
	avail := rows - len(header) - len(bottom)
	if avail < 0 {
		header = nil
		avail = rows - len(bottom)
	}
	if avail < 0 {
		bottom = bottom[len(bottom)-rows:]
		avail = 0
	}
	if len(conv) > avail {
		conv = conv[len(conv)-avail:]
	}
	lines := append(append(header, conv...), bottom...)
	return "\x1b[H\x1b[2J" + strings.Join(lines, "\r\n")
}

func cmp(s, def string) string {
	if s == "" || s == "Churning…" {
		return def
	}
	return s
}

// codexDialogLines are the Codex dialogs' rows (codexq is drawn with
// the Claude ones), "›" on the selected option.
func (d *dialog) codexLines() []string {
	opts := func(indent string) []string {
		var out []string
		for i, o := range d.options {
			mark := "  "
			if i == d.sel {
				mark = "› "
			}
			out = append(out, fmt.Sprintf("%s%s%d. %s", indent, mark, i+1, o))
		}
		return out
	}
	var out []string
	switch d.kind {
	case "cxtrust":
		out = append(out, "  Folder access", "  "+d.subject, "",
			"  Trust this folder? Codex can read, edit, and run files here, subject to your permission settings. Folder settings",
			"  can run code automatically, even without a model request. Continue only if you trust these files. Your trust",
			"  decision will be saved.", "")
		out = append(out, opts("")...)
		out = append(out, "", "  enter continue · esc back")
	case "hooksreview":
		out = append(out, "  Hooks need review", "  "+d.subject+" hooks are new or changed.",
			"  Hooks can run outside the sandbox after you trust them.", "")
		out = append(out, opts("")...)
		out = append(out, "", "  enter confirm · esc skip")
	case "update":
		out = append(out, "  Update available · "+codexVersion+" → 0.160.1",
			"  Release notes: https://github.com/openai/codex/releases/latest", "")
		out = append(out, opts("")...)
		out = append(out, "", "  enter continue · esc skip")
	case "approval":
		out = append(out, "  Would you like to run the following command?", "", "  Environment: local", "",
			"  Reason: "+d.title, "", "  $ "+d.subject, "")
		out = append(out, opts("")...)
		out = append(out, "", "  Press enter to confirm or esc to cancel")
	case "newdlg":
		out = append(out, "  Where should the new conversation run?", "")
		out = append(out, opts("")...)
		out = append(out, "", "  enter select · esc back")
	}
	return out
}

// codexApprove is Codex's approval of a command: none under yolo, or
// with --approve-for-me (its reviewer decides; the fake approves), or
// once "don't ask again" covers it. "No" (or Esc) ends the turn, with
// the Interrupt hook.
func (a *app) codexApprove(ctx context.Context, tool, reason string, input map[string]any, id string) (bool, error) {
	cmd := toolArg(input)
	a.mu.Lock()
	skip := a.cx.opts.yolo || a.alwaysAllow[tool+"\x00"+cmd]
	review := a.cx.opts.approveForMe
	a.mu.Unlock()
	if skip {
		return true, nil
	}
	if review {
		a.log("auto-review", map[string]any{"tool": tool, "command": cmd})
		return true, nil
	}
	if _, err := a.fireHook(ctx, "PermissionRequest", map[string]any{"tool_name": tool, "tool_input": input, "tool_use_id": id}); err != nil {
		return false, err
	}
	if reason == "" {
		reason = "Do you want to allow this command?"
	}
	d := &dialog{kind: "approval", title: reason, subject: cmd, options: []string{
		"Yes, proceed (y)",
		"Yes, and don't ask again for commands that start with `" + cmd + "` (p)",
		"No, and tell Codex what to do differently (esc)"}}
	n, err := a.waitDialog(ctx, d)
	if err != nil {
		return false, err
	}
	switch n {
	case 2:
		a.mu.Lock()
		a.alwaysAllow[tool+"\x00"+cmd] = true
		a.mu.Unlock()
	case 3:
		a.say("✗ ", "You canceled the request to run "+cmd)
		a.mu.Lock()
		a.cancelTurnLocked()
		a.mu.Unlock()
		return false, errCancelled
	}
	return true, nil
}

// codexQuestion is request_user_input in plan mode: Codex's menu, with
// "None of the above" last, and no hook at all.
func (a *app) codexQuestion(ctx context.Context, st step, opts []string) error {
	all := append(append([]string(nil), opts...), "None of the above")
	d := &dialog{kind: "codexq", question: st.Question, options: all, textOpt: len(all) - 1, at: max(st.At, 1), of: max(st.Of, 1)}
	a.setInTool(true)
	n, err := a.waitDialog(ctx, d)
	if err != nil {
		return err
	}
	answer := all[n-1]
	if n-1 == d.textOpt {
		a.mu.Lock()
		answer = string(d.text)
		a.mu.Unlock()
	}
	a.log("answer", map[string]any{"question": st.Question, "answer": answer})
	a.say("  └ ", st.Question+" → "+answer)
	a.setInTool(false)
	return nil
}

// codexUnload ends a thread /clear left: SessionEnd (reason "other")
// with its id and rollout.
func (a *app) codexUnload(thread, rollout string) {
	d := time.Second
	if ms, err := strconv.Atoi(os.Getenv("FAKEAGENT_CODEX_UNLOAD_MS")); err == nil {
		d = time.Duration(ms) * time.Millisecond
	}
	time.Sleep(d)
	_, _ = a.fireHook(context.Background(), "SessionEnd", map[string]any{"reason": "other", "session_id": thread, "transcript_path": rollout})
}

// sandboxCmd is `codex sandbox -P <name> -C <dir> -c … -- <cmd…>` as tm's
// coordinator probe runs it (internal/agent/codex/probe.go): it doesn't
// run the command but prints what tm's probe would, judging the profile
// as Codex 0.160 does: tm's socket reachable when the profile allows it,
// TCP refused when network.mode is "limited" and features.network_proxy
// is on. FAKEAGENT_CODEX_SANDBOX plays a newer Codex: "ignore-proxy"
// (the feature renamed: the network is open) or "reject" (a -c key
// refused).
func sandboxCmd(logPath string, args []string) int {
	rec := map[string]any{"t": time.Now().Format(time.RFC3339Nano), "kind": "sandbox", "argv": args}
	defer func() { _ = appendJSONL(logPath, rec) }()
	var name string
	var values, cmd []string
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--":
			cmd = args[i+1:]
			i = len(args)
		case args[i] == "-P" && i+1 < len(args):
			name = args[i+1]
			i++
		case args[i] == "-c" && i+1 < len(args):
			values = append(values, args[i+1])
			i++
		case args[i] == "-C" && i+1 < len(args):
			i++
		}
	}
	if os.Getenv("FAKEAGENT_CODEX_SANDBOX") == "reject" {
		rec["verdict"] = "rejected"
		fmt.Fprintln(os.Stderr, "Error loading config.toml: unknown configuration field `features.network_proxy` in -c/--config override")
		return 1
	}
	cfg, err := codexConfig(values)
	if err != nil || len(cmd) != 4 {
		fmt.Fprintln(os.Stderr, "Error:", err, cmd)
		return 1
	}
	sock := cmd[2]
	unix, tcp := "refused", "refused"
	perms, _ := cfg["permissions"].(map[string]any)
	prof, _ := perms[name].(map[string]any)
	netw, _ := prof["network"].(map[string]any)
	if socks, _ := netw["unix_sockets"].(map[string]any); socks[sock] == "allow" && netw["enabled"] == true {
		unix = "ok"
		features, _ := cfg["features"].(map[string]any)
		limited := netw["mode"] == "limited" && features["network_proxy"] == true
		if !limited || os.Getenv("FAKEAGENT_CODEX_SANDBOX") == "ignore-proxy" {
			tcp = "ok"
		}
	}
	rec["verdict"] = "unix=" + unix + " tcp=" + tcp
	fmt.Printf("unix=%s tcp=%s\n", unix, tcp)
	return 0
}
